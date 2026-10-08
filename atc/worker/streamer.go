package worker

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"net/url"
	"sync/atomic"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/v8/atc"
	"github.com/concourse/concourse/v8/atc/compression"
	"github.com/concourse/concourse/v8/atc/db"
	"github.com/concourse/concourse/v8/atc/metric"
	"github.com/concourse/concourse/v8/atc/runtime"
	"github.com/concourse/concourse/v8/tracing"
	"github.com/hashicorp/go-multierror"
	"go.opentelemetry.io/otel/attribute"
)

type Streamer struct {
	compression compression.Compression
	limitInMB   float64
	p2p         P2PConfig

	resourceCacheFactory db.ResourceCacheFactory
}

type P2PConfig struct {
	Enabled bool
	Timeout time.Duration
}

func NewStreamer(cacheFactory db.ResourceCacheFactory, compression compression.Compression, limitInMB float64, p2p P2PConfig) Streamer {
	return Streamer{
		resourceCacheFactory: cacheFactory,
		compression:          compression,
		limitInMB:            limitInMB,
		p2p:                  p2p,
	}
}

// unknownBytes marks a transfer whose byte count could not be determined.
const unknownBytes int64 = -1

// streamResult describes how one transfer operation was carried out. It feeds
// the volume streaming metrics and the end-of-stream log line.
type streamResult struct {
	// route is the metric route label: the path taken and why.
	route string
	// bytes is the number of compressed bytes moved on route, or unknownBytes.
	bytes int64
	// srcGroup and dstGroup are the workers' P2P streaming groups, empty when
	// ungrouped. srcGroup is metric.UnknownStreamingGroup when the source is
	// not a worker volume.
	srcGroup string
	dstGroup string
}

// Stream copies src into dst over P2P or through this web node, then registers
// dst as a resource cache when src was one. It emits the volume streaming
// metrics and log line for the transfer; they cover the transfer only, not the
// cache registration.
func (s Streamer) Stream(ctx context.Context, src runtime.Artifact, dst runtime.Volume) error {
	loggerData := lager.Data{
		"to":          dst.DBVolume().WorkerName(),
		"to-handle":   dst.Handle(),
		"from":        src.Source(),
		"from-handle": src.Handle(),
	}
	logger := lagerctx.FromContext(ctx).Session("stream", loggerData)
	logger.Info("start")

	// Only a worker volume names a worker as source; other artifacts must not
	// become a per-worker label value.
	srcVolume, isSrcVolume := src.(runtime.Volume)
	var srcWorker string
	if isSrcVolume {
		srcWorker = srcVolume.DBVolume().WorkerName()
	}

	start := time.Now()
	result, err := s.stream(ctx, src, dst)
	duration := time.Since(start)

	metric.VolumeStreaming{
		Route:     result.route,
		Duration:  duration,
		Err:       err,
		Bytes:     result.bytes,
		SrcWorker: srcWorker,
		DstWorker: dst.DBVolume().WorkerName(),
		SrcGroup:  result.srcGroup,
		DstGroup:  result.dstGroup,
	}.Emit(logger)

	// The exact worker pair with its byte count is recorded here and in the
	// build's streaming-volume event, never as metric labels (W^2 series).
	logger.Info("end", lager.Data{
		"route":     result.route,
		"bytes":     result.bytes,
		"duration":  duration.String(),
		"src-group": result.srcGroup,
		"dst-group": result.dstGroup,
	})
	if err != nil {
		return err
	}

	if !isSrcVolume {
		return nil
	}

	metric.Metrics.VolumesStreamed.Inc()

	resourceCacheID := srcVolume.DBVolume().GetResourceCacheID()
	if atc.EnableCacheStreamedVolumes && resourceCacheID != 0 {
		logger.Debug("initialize-streamed-resource-cache", lager.Data{"resource-cache-id": resourceCacheID})
		usedResourceCache, found, err := s.resourceCacheFactory.FindResourceCacheByID(resourceCacheID)
		if err != nil {
			logger.Error("stream-to-failed-to-find-resource-cache", err)
			return err
		}
		if !found {
			logger.Info("stream-resource-cache-not-found-should-not-happen", lager.Data{
				"resource-cache-id": resourceCacheID,
				"volume":            srcVolume.Handle(),
			})
			return StreamingResourceCacheNotFoundError{
				Handle:          srcVolume.Handle(),
				ResourceCacheID: resourceCacheID,
			}
		}

		_, err = dst.InitializeStreamedResourceCache(ctx,
			usedResourceCache,
			srcVolume.DBVolume().WorkerResourceCacheID())
		if err != nil {
			logger.Error("failed-to-init-resource-cache-on-dest-worker", err)
			return err
		}

		metric.Metrics.StreamedResourceCaches.Inc()
	}
	return nil
}

// stream picks the transfer route and performs it. The result's route says
// which path was taken and why; its bytes are what moved on that path.
func (s Streamer) stream(ctx context.Context, src runtime.Artifact, dst runtime.Volume) (streamResult, error) {
	logger := lagerctx.FromContext(ctx)

	// Groups are read before any early return so every route is labelled.
	result := streamResult{
		route:    metric.VolumeStreamingRouteATCDisabled,
		bytes:    unknownBytes,
		srcGroup: metric.UnknownStreamingGroup,
		dstGroup: dst.DBVolume().P2PStreamingGroup(),
	}
	if srcVolume, ok := src.(runtime.Volume); ok {
		result.srcGroup = srcVolume.DBVolume().P2PStreamingGroup()
	}

	if !s.p2p.Enabled {
		return s.streamThroughATC(ctx, src, dst, result)
	}
	result.route = metric.VolumeStreamingRouteATCUnsupported
	p2pSrc, ok := src.(runtime.P2PVolume)
	if !ok {
		return s.streamThroughATC(ctx, src, dst, result)
	}
	p2pDst, ok := dst.(runtime.P2PVolume)
	if !ok {
		return s.streamThroughATC(ctx, src, dst, result)
	}

	if result.srcGroup != result.dstGroup {
		result.route = metric.VolumeStreamingRouteATCGroupMismatch
		logger.Debug("p2p-streaming-groups-differ", lager.Data{
			"src-worker":  p2pSrc.DBVolume().WorkerName(),
			"dest-worker": p2pDst.DBVolume().WorkerName(),
			"src-group":   result.srcGroup,
			"dest-group":  result.dstGroup,
		})
		return s.streamThroughATC(ctx, src, dst, result)
	}

	result.route = metric.VolumeStreamingRouteP2P
	bytesSent, err := s.p2pStream(ctx, p2pSrc, p2pDst)
	if err == nil {
		result.bytes = bytesSent
		return result, nil
	}

	// P2P streaming failed - fallback to streaming through ATC (web node).
	// Only the retry's bytes are accounted, so the fallback row keeps meaning
	// "bytes relayed through the web node"; the wasted attempt is logged.
	result.route = metric.VolumeStreamingRouteATCFallback
	logger.Error("p2p-stream-failed-falling-back-to-atc", err, lager.Data{
		"src-worker":     p2pSrc.DBVolume().WorkerName(),
		"dest-worker":    p2pDst.DBVolume().WorkerName(),
		"src-group":      result.srcGroup,
		"dest-group":     result.dstGroup,
		"p2p-bytes-sent": bytesSent,
	})

	metric.Metrics.VolumesStreamedViaFallback.Inc()
	return s.streamThroughATC(ctx, src, dst, result)
}

// streamThroughATC relays src into dst through this web node, counting the
// compressed bytes that pass through. The route in result is kept; its byte
// count becomes what was relayed, a partial count when the transfer failed.
func (s Streamer) streamThroughATC(ctx context.Context, src runtime.Artifact, dst runtime.Volume, result streamResult) (streamResult, error) {
	traceAttrs := tracing.Attrs{
		"dest-worker": dst.DBVolume().WorkerName(),
		"route":       result.route,
	}
	if srcVolume, ok := src.(runtime.Volume); ok {
		traceAttrs["origin-volume"] = srcVolume.Handle()
		traceAttrs["origin-worker"] = srcVolume.DBVolume().WorkerName()
	}
	ctx, span := tracing.StartSpan(ctx, "volume.StreamThroughATC", traceAttrs)

	relayed := &countingReadCloser{}
	var err error
	defer func() {
		span.SetAttributes(attribute.Int64("bytes", relayed.Count()))
		tracing.End(span, err)
	}()

	var out io.ReadCloser
	out, err = src.StreamOut(ctx, ".", s.compression)
	if err != nil {
		result.bytes = 0
		return result, err
	}
	defer out.Close()

	relayed.ReadCloser = out
	err = dst.StreamIn(ctx, ".", s.compression, s.limitInMB, relayed)
	result.bytes = relayed.Count()
	return result, err
}

// p2pStream streams src straight to dst between the two workers. It returns
// the compressed bytes the source worker reported sending, negative when the
// worker did not report, and the transfer error if any.
func (s Streamer) p2pStream(ctx context.Context, src runtime.P2PVolume, dst runtime.P2PVolume) (bytesSent int64, err error) {
	getCtx, getCancel := context.WithTimeout(ctx, 5*time.Second)
	defer getCancel()

	streamInUrl, err := dst.GetStreamInP2PURL(getCtx, ".")
	if err != nil {
		return 0, err
	}

	// Verify stream-in url
	rawUrl, err := url.Parse(streamInUrl)
	if err != nil {
		return 0, fmt.Errorf("invalid stream-in-url: %w", err)
	}
	// If stream limit is set to greater than 1 byte, append the limit to stream-in url
	if s.limitInMB > float64(1)/1024/1024 {
		query := rawUrl.Query()
		query.Add("limit", fmt.Sprintf("%f", s.limitInMB))
		rawUrl.RawQuery = query.Encode()
	}
	streamInUrl = rawUrl.String()

	ctx, outSpan := tracing.StartSpan(ctx, "volume.P2pStreamOut", tracing.Attrs{
		"origin-volume": src.Handle(),
		"origin-worker": src.DBVolume().WorkerName(),
		"dest-worker":   dst.DBVolume().WorkerName(),
		"stream-in-url": streamInUrl,
	})
	defer func() {
		outSpan.SetAttributes(attribute.Int64("bytes", bytesSent))
		tracing.End(outSpan, err)
	}()

	putCtx := ctx
	if s.p2p.Timeout != 0 {
		var putCancel context.CancelFunc
		putCtx, putCancel = context.WithTimeout(putCtx, s.p2p.Timeout)
		defer putCancel()
	}

	return src.StreamP2POut(putCtx, ".", streamInUrl, s.compression)
}

func (s Streamer) StreamFile(ctx context.Context, artifact runtime.Artifact, path string) (io.ReadCloser, error) {
	out, err := artifact.StreamOut(ctx, path, s.compression)
	if err != nil {
		return nil, err
	}

	compressionReader, err := s.compression.NewReader(out)
	if err != nil {
		return nil, err
	}
	tarReader := tar.NewReader(compressionReader)

	_, err = tarReader.Next()
	if err != nil {
		return nil, err
	}

	return fileReadMultiCloser{
		Reader: tarReader,
		closers: []io.Closer{
			out,
			compressionReader,
		},
	}, nil
}

type fileReadMultiCloser struct {
	io.Reader
	closers []io.Closer
}

func (frc fileReadMultiCloser) Close() error {
	var closeErrors error

	for _, closer := range frc.closers {
		err := closer.Close()
		if err != nil {
			closeErrors = multierror.Append(closeErrors, err)
		}
	}

	return closeErrors
}

// countingReadCloser counts the bytes read through an io.ReadCloser. The count
// is atomic because the HTTP transport reads a request body on its own
// goroutine and may still be draining it when the request returns early.
type countingReadCloser struct {
	io.ReadCloser
	n atomic.Int64
}

// Read forwards to the wrapped reader and adds the bytes returned to the count.
func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// Count returns the number of bytes read so far.
func (c *countingReadCloser) Count() int64 {
	return c.n.Load()
}
