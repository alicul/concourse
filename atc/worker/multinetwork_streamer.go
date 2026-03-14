package worker

import (
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
)

// MultiNetworkStreamer handles volume streaming in multi-network environments
type MultiNetworkStreamer struct {
	compression          compression.Compression
	limitInMB            float64
	p2p                  P2PConfig
	router               P2PRouter
	resourceCacheFactory db.ResourceCacheFactory
	networkTopologyFactory db.NetworkTopologyFactory
}

// P2PMultiNetworkConfig extends P2PConfig for multi-network support
type P2PMultiNetworkConfig struct {
	P2PConfig
	MultiNetworkEnabled bool
	RelayEnabled        bool
	TopologyRefreshInterval time.Duration
}

// NewMultiNetworkStreamer creates a new multi-network streamer
func NewMultiNetworkStreamer(
	cacheFactory db.ResourceCacheFactory,
	networkTopologyFactory db.NetworkTopologyFactory,
	compression compression.Compression,
	limitInMB float64,
	p2pConfig P2PConfig,
	router P2PRouter,
) *MultiNetworkStreamer {
	return &MultiNetworkStreamer{
		resourceCacheFactory:   cacheFactory,
		networkTopologyFactory: networkTopologyFactory,
		compression:            compression,
		limitInMB:              limitInMB,
		p2p:                    p2pConfig,
		router:                 router,
	}
}

// Stream performs volume streaming with multi-network support
func (s *MultiNetworkStreamer) Stream(ctx context.Context, src runtime.Artifact, dst runtime.Volume) error {
	loggerData := lager.Data{
		"to":          dst.DBVolume().WorkerName(),
		"to-handle":   dst.Handle(),
		"from":        src.Source(),
		"from-handle": src.Handle(),
	}
	logger := lagerctx.FromContext(ctx).Session("stream-multi-network", loggerData)
	logger.Info("start")
	defer logger.Info("end")

	// Track streaming metadata for metrics
	labels := s.extractStreamingLabels(ctx, src, dst)
	startTime := time.Now()
	var streamingType string
	var success bool
	var sizeBytes int64

	defer func() {
		duration := time.Since(startTime)
		metric.RecordVolumeStreamed(labels, sizeBytes, duration, success)
	}()

	// Perform the streaming
	err := s.streamWithRouting(ctx, src, dst, &streamingType, &sizeBytes)
	success = (err == nil)
	labels.StreamingType = streamingType

	if err != nil {
		logger.Error("streaming-failed", err, lager.Data{"type": streamingType})
		return err
	}

	// Handle resource cache if applicable
	srcVolume, isSrcVolume := src.(runtime.Volume)
	if !isSrcVolume {
		return nil
	}

	metric.VolumesStreamed.Inc()

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

// streamWithRouting performs streaming with intelligent routing
func (s *MultiNetworkStreamer) streamWithRouting(
	ctx context.Context,
	src runtime.Artifact,
	dst runtime.Volume,
	streamingType *string,
	sizeBytes *int64,
) error {
	logger := lagerctx.FromContext(ctx)

	if !s.p2p.Enabled {
		*streamingType = metric.StreamingTypeATCMediated
		return s.streamThroughATC(ctx, src, dst, sizeBytes)
	}

	// Check if both source and destination support P2P
	p2pSrc, srcSupportsP2P := src.(runtime.P2PVolume)
	p2pDst, dstSupportsP2P := dst.(runtime.P2PVolume)

	if !srcSupportsP2P || !dstSupportsP2P {
		logger.Debug("p2p-not-supported-by-volumes")
		*streamingType = metric.StreamingTypeATCMediated
		return s.streamThroughATC(ctx, src, dst, sizeBytes)
	}

	// Find the best route
	srcWorkerName := p2pSrc.DBVolume().WorkerName()
	dstWorkerName := p2pDst.DBVolume().WorkerName()

	route, err := s.router.FindRoute(ctx, srcWorkerName, dstWorkerName)
	if err != nil {
		logger.Error("failed-to-find-route", err)
		*streamingType = metric.StreamingTypeATCMediated
		return s.streamThroughATC(ctx, src, dst, sizeBytes)
	}

	// Execute streaming based on route type
	switch route.Type {
	case P2PRouteDirect:
		*streamingType = metric.StreamingTypeP2PDirect
		err = s.p2pStreamDirect(ctx, p2pSrc, p2pDst, route, sizeBytes)

	case P2PRouteRelay:
		*streamingType = metric.StreamingTypeP2PRelay
		err = s.p2pStreamRelay(ctx, p2pSrc, p2pDst, route, sizeBytes)

	default:
		*streamingType = metric.StreamingTypeATCMediated
		return s.streamThroughATC(ctx, src, dst, sizeBytes)
	}

	// If P2P fails, fallback to ATC
	if err != nil {
		logger.Error("p2p-stream-failed-falling-back", err, lager.Data{
			"route-type": route.Type,
			"src-worker": srcWorkerName,
			"dst-worker": dstWorkerName,
		})

		metric.P2PStreamingFailures.WithLabelValues(
			srcWorkerName, dstWorkerName, "streaming_error", route.NetworkSegment,
		).Inc()

		metric.Metrics.VolumesStreamedViaFallback.Inc()
		*streamingType = metric.StreamingTypeATCMediated
		return s.streamThroughATC(ctx, src, dst, sizeBytes)
	}

	// Record successful P2P streaming
	metric.P2PStreamingSuccess.WithLabelValues(
		srcWorkerName, dstWorkerName, *streamingType, route.NetworkSegment,
	).Inc()

	return nil
}

// p2pStreamDirect performs direct P2P streaming
func (s *MultiNetworkStreamer) p2pStreamDirect(
	ctx context.Context,
	src runtime.P2PVolume,
	dst runtime.P2PVolume,
	route *P2PRoute,
	sizeBytes *int64,
) error {
	logger := lagerctx.FromContext(ctx)
	srcWorkerName := src.DBVolume().WorkerName()
	dstWorkerName := dst.DBVolume().WorkerName()

	logger.Info("p2p-stream-direct", lager.Data{
		"src-worker": srcWorkerName,
		"dst-worker": dstWorkerName,
		"segment":    route.NetworkSegment,
	})

	// Track active stream
	metric.ActiveP2PStreams.WithLabelValues(
		srcWorkerName, dstWorkerName, "direct",
	).Inc()
	defer metric.ActiveP2PStreams.WithLabelValues(
		srcWorkerName, dstWorkerName, "direct",
	).Dec()

	// Get stream-in URL from destination
	getCtx, getCancel := context.WithTimeout(ctx, 5*time.Second)
	defer getCancel()

	streamInUrl, err := dst.GetStreamInP2PURL(getCtx, ".")
	if err != nil {
		return fmt.Errorf("failed to get stream-in URL: %w", err)
	}

	// If using a specific route URL, modify the stream-in URL
	if route.DirectURL != "" {
		streamInUrl = s.modifyStreamInURL(streamInUrl, route.DirectURL)
	}

	// Add size limit if configured
	streamInUrl = s.addSizeLimitToURL(streamInUrl)

	// Perform P2P streaming
	putCtx := ctx
	if s.p2p.Timeout != 0 {
		var putCancel context.CancelFunc
		putCtx, putCancel = context.WithTimeout(putCtx, s.p2p.Timeout)
		defer putCancel()
	}

	// Track bandwidth utilization
	if route.Bandwidth > 0 {
		metric.P2PBandwidthUtilization.WithLabelValues(
			srcWorkerName, dstWorkerName, route.NetworkSegment,
		).Set(float64(route.Bandwidth))
	}

	return src.StreamP2POut(putCtx, ".", streamInUrl, s.compression)
}

// p2pStreamRelay performs P2P streaming through a relay worker
func (s *MultiNetworkStreamer) p2pStreamRelay(
	ctx context.Context,
	src runtime.P2PVolume,
	dst runtime.P2PVolume,
	route *P2PRoute,
	sizeBytes *int64,
) error {
	logger := lagerctx.FromContext(ctx)
	srcWorkerName := src.DBVolume().WorkerName()
	dstWorkerName := dst.DBVolume().WorkerName()

	logger.Info("p2p-stream-relay", lager.Data{
		"src-worker":   srcWorkerName,
		"dst-worker":   dstWorkerName,
		"relay-worker": route.RelayWorker,
	})

	// Track relay stream
	metric.P2PRelayStreams.WithLabelValues(
		srcWorkerName, dstWorkerName, route.RelayWorker, "", "",
	).Inc()

	// Track active stream
	metric.ActiveP2PStreams.WithLabelValues(
		srcWorkerName, dstWorkerName, "relay",
	).Inc()
	defer metric.ActiveP2PStreams.WithLabelValues(
		srcWorkerName, dstWorkerName, "relay",
	).Dec()

	// For now, implement relay as two-hop streaming
	// In a full implementation, the relay worker would proxy the stream

	// Step 1: Stream from source to relay
	// Step 2: Stream from relay to destination
	// This is a simplified implementation - a production version would
	// establish a streaming proxy through the relay worker

	return fmt.Errorf("relay streaming not yet fully implemented")
}

// streamThroughATC performs traditional ATC-mediated streaming
func (s *MultiNetworkStreamer) streamThroughATC(
	ctx context.Context,
	src runtime.Artifact,
	dst runtime.Volume,
	sizeBytes *int64,
) error {
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

	// Track the size if possible
	if counter, ok := out.(io.Reader); ok {
		countingReader := &countingReader{Reader: counter, count: sizeBytes}
		return dst.StreamIn(ctx, ".", s.compression, s.limitInMB, countingReader)
	}

	return dst.StreamIn(ctx, ".", s.compression, s.limitInMB, out)
}

// modifyStreamInURL modifies the stream-in URL to use a specific endpoint
func (s *MultiNetworkStreamer) modifyStreamInURL(originalURL, routeURL string) string {
	// Parse both URLs
	origParsed, err := url.Parse(originalURL)
	if err != nil {
		return originalURL
	}

	routeParsed, err := url.Parse(routeURL)
	if err != nil {
		return originalURL
	}

	// Use the host from the route URL but keep the path from the original
	origParsed.Host = routeParsed.Host
	return origParsed.String()
}

// addSizeLimitToURL adds size limit parameter to the URL
func (s *MultiNetworkStreamer) addSizeLimitToURL(streamURL string) string {
	if s.limitInMB <= float64(1)/1024/1024 {
		return streamURL
	}

	parsed, err := url.Parse(streamURL)
	if err != nil {
		return streamURL
	}

	query := parsed.Query()
	query.Add("limit", fmt.Sprintf("%f", s.limitInMB))
	parsed.RawQuery = query.Encode()

	return parsed.String()
}

// extractStreamingLabels extracts labels for metrics from the streaming context
func (s *MultiNetworkStreamer) extractStreamingLabels(
	ctx context.Context,
	src runtime.Artifact,
	dst runtime.Volume,
) metric.P2PStreamingLabels {
	labels := metric.P2PStreamingLabels{
		DestinationWorker: dst.DBVolume().WorkerName(),
	}

	if srcVolume, ok := src.(runtime.Volume); ok {
		labels.SourceWorker = srcVolume.DBVolume().WorkerName()
	}

	// Extract step and pipeline information from context if available
	// This would typically come from the build context
	if ctxValue := ctx.Value("step_type"); ctxValue != nil {
		labels.StepType = ctxValue.(string)
	}
	if ctxValue := ctx.Value("step_name"); ctxValue != nil {
		labels.StepName = ctxValue.(string)
	}
	if ctxValue := ctx.Value("pipeline_name"); ctxValue != nil {
		labels.PipelineName = ctxValue.(string)
	}
	if ctxValue := ctx.Value("job_name"); ctxValue != nil {
		labels.JobName = ctxValue.(string)
	}
	if ctxValue := ctx.Value("team_name"); ctxValue != nil {
		labels.TeamName = ctxValue.(string)
	}

	return labels
}

// countingReader counts bytes as they are read
type countingReader struct {
	io.Reader
	count *int64
}

func (r *countingReader) Read(p []byte) (n int, err error) {
	n, err = r.Reader.Read(p)
	if r.count != nil {
		*r.count += int64(n)
	}
	return
}