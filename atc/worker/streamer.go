package worker

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"net/url"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/tracing"
	"github.com/hashicorp/go-multierror"
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

func NewStreamer(c compression.Compression, limitInMB float64, p2p P2PConfig, rCF db.ResourceCacheFactory) Streamer {
	return Streamer{
		compression:          c,
		limitInMB:            limitInMB,
		resourceCacheFactory: rCF,
		p2p:                  p2p,
	}
}

func (s Streamer) Stream(ctx context.Context, src runtime.Artifact, dst runtime.Volume) error {
	loggerData := lager.Data{
		"to":          dst.DBVolume().WorkerName(),
		"to-handle":   dst.Handle(),
		"from":        src.Source(),
		"from-handle": src.Handle(),
	}
	logger := lagerctx.FromContext(ctx).Session("stream", loggerData)
	logger.Info("start")
	defer logger.Info("end")

	// Start timing the streaming operation
	startTime := time.Now()

	err := s.stream(ctx, src, dst)

	// Calculate duration
	duration := time.Since(startTime).Seconds()

	if err != nil {
		// Log duration even on failure for debugging
		logger.Error("stream-failed", err, lager.Data{"duration_seconds": duration})
		return err
	}

	srcVolume, isSrcVolume := src.(runtime.Volume)
	if !isSrcVolume {
		return nil
	}

	// Track volume size if available
	if volumeSize := srcVolume.DBVolume().Size(); volumeSize > 0 {
		metric.Emit(metric.Event{
			Name:  "volume streaming size",
			Value: float64(volumeSize),
		})
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

func (s Streamer) stream(ctx context.Context, src runtime.Artifact, dst runtime.Volume) error {
	logger := lagerctx.FromContext(ctx)

	// Determine if we can use P2P
	canUseP2P := false
	var p2pSrc runtime.P2PVolume
	var p2pDst runtime.P2PVolume

	if s.p2p.Enabled {
		if src1, ok := src.(runtime.P2PVolume); ok {
			if dst1, ok := dst.(runtime.P2PVolume); ok {
				canUseP2P = true
				p2pSrc = src1
				p2pDst = dst1
			}
		}
	}

	if !canUseP2P {
		// Use ATC streaming directly
		return s.streamThroughATCWithMetrics(ctx, src, dst, "direct")
	}

	// Try P2P streaming
	startTime := time.Now()

	// Track in-progress P2P streaming
	metric.Emit(metric.Event{
		Name:       "volume streaming in progress",
		State:      "inc",
		Attributes: metric.Attributes{"method": "p2p"},
	})
	defer metric.Emit(metric.Event{
		Name:       "volume streaming in progress",
		State:      "dec",
		Attributes: metric.Attributes{"method": "p2p"},
	})

	err := s.p2pStream(ctx, p2pSrc, p2pDst)
	duration := time.Since(startTime).Seconds()

	if err != nil {
		// P2P streaming failed
		logger.Error("p2p-stream-failed-falling-back-to-atc", err, lager.Data{
			"src-worker":      p2pSrc.DBVolume().WorkerName(),
			"dest-worker":     p2pDst.DBVolume().WorkerName(),
			"duration_seconds": duration,
		})

		// Emit P2P failure metrics
		metric.Metrics.VolumeStreamingP2PFailure.Inc()
		metric.Emit(metric.Event{
			Name:       "volume streaming duration",
			Value:      duration,
			Attributes: metric.Attributes{"method": "p2p", "status": "failure"},
		})
		metric.Metrics.VolumesStreamedViaFallback.Inc()

		// Fallback to ATC streaming
		return s.streamThroughATCWithMetrics(ctx, src, dst, "fallback")
	}

	// P2P streaming succeeded
	metric.Metrics.VolumeStreamingP2PSuccess.Inc()
	metric.Emit(metric.Event{
		Name:       "volume streaming duration",
		Value:      duration,
		Attributes: metric.Attributes{"method": "p2p", "status": "success"},
	})

	return nil
}

func (s Streamer) streamThroughATCWithMetrics(ctx context.Context, src runtime.Artifact, dst runtime.Volume, reason string) error {
	startTime := time.Now()

	// Track in-progress ATC streaming
	metric.Emit(metric.Event{
		Name:       "volume streaming in progress",
		State:      "inc",
		Attributes: metric.Attributes{"method": "atc"},
	})
	defer metric.Emit(metric.Event{
		Name:       "volume streaming in progress",
		State:      "dec",
		Attributes: metric.Attributes{"method": "atc"},
	})

	err := s.streamThroughATC(ctx, src, dst)
	duration := time.Since(startTime).Seconds()

	if err != nil {
		// ATC streaming failed
		metric.Metrics.VolumeStreamingATCFailure.Inc()
		metric.Emit(metric.Event{
			Name:       "volume streaming duration",
			Value:      duration,
			Attributes: metric.Attributes{"method": "atc", "status": "failure"},
		})
		return err
	}

	// ATC streaming succeeded
	metric.Metrics.VolumeStreamingATCSuccess.Inc()
	metric.Emit(metric.Event{
		Name:       "volume streaming duration",
		Value:      duration,
		Attributes: metric.Attributes{"method": "atc", "status": "success"},
	})

	return nil
}

func (s Streamer) streamThroughATC(ctx context.Context, src runtime.Artifact, dst runtime.Volume) error {
	traceAttrs := tracing.Attrs{
		"dest-worker": dst.DBVolume().WorkerName(),
	}
	if srcVolume, ok := src.(runtime.Volume); ok {
		traceAttrs["origin-volume"] = srcVolume.Handle()
		traceAttrs["origin-worker"] = srcVolume.DBVolume().WorkerName()
	}
	out, err := src.StreamOut(ctx, ".", s.compression)

	if err != nil {
		return err
	}

	defer out.Close()

	return dst.StreamIn(ctx, ".", s.compression.Encoding(), out, s.limitInMB)
}

func (s Streamer) p2pStream(ctx context.Context, src runtime.P2PVolume, dst runtime.P2PVolume) error {
	ctx, span := tracing.StartSpan(ctx, "streamer.p2pStream", tracing.Attrs{
		"origin-volume": src.Handle(),
		"origin-worker": src.DBVolume().WorkerName(),
		"dest-volume":   dst.Handle(),
		"dest-worker":   dst.DBVolume().WorkerName(),
	})
	defer span.End()

	logger := lagerctx.FromContext(ctx).Session("p2p-stream")

	destStreamInURL, err := dst.GetStreamInP2PURL(ctx, ".")
	if err != nil {
		logger.Error("failed-to-get-stream-in-p2p-url", err)
		return fmt.Errorf("failed to get stream-in P2P URL: %w", err)
	}

	u, err := url.Parse(destStreamInURL)
	if err != nil {
		logger.Error("failed-to-parse-stream-in-p2p-url", err, lager.Data{"url": destStreamInURL})
		return fmt.Errorf("failed to parse stream-in P2P URL: %w", err)
	}
	qs := u.Query()
	qs.Set("limit", fmt.Sprintf("%f", s.limitInMB))
	u.RawQuery = qs.Encode()
	destStreamInURL = u.String()

	// add configurable timeout to volume p2p streaming through env var
	if s.p2p.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.p2p.Timeout)
		defer cancel()
	}

	logger.Debug("streaming-to-p2p-url", lager.Data{"url": destStreamInURL})
	return src.StreamP2POut(ctx, destStreamInURL, s.compression.Encoding())
}

type StreamingResourceCacheNotFoundError struct {
	Handle          string
	ResourceCacheID int
}

func (e StreamingResourceCacheNotFoundError) Error() string {
	return fmt.Sprintf("cache not found for volume %s (resource cache id %d)",
		e.Handle,
		e.ResourceCacheID)
}

func tarStreamFrom(src io.Reader) io.Reader {
	pr, pw := io.Pipe()

	go func() {
		var outErr error
		defer func() {
			pw.CloseWithError(outErr)
		}()

		tarWriter := tar.NewWriter(pw)
		defer func() {
			err := tarWriter.Close()
			if outErr == nil {
				outErr = err
			}
		}()

		buf := make([]byte, 1024*1024)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				outErr = tarWriter.Write(buf[0:n])
				if outErr != nil {
					return
				}
			}
			if err == io.EOF {
				return
			}
			if err != nil {
				outErr = multierror.Append(outErr, err)
				return
			}
		}
	}()

	return pr
}