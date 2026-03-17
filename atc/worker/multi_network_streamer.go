package worker

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/routing"
	"github.com/concourse/concourse/tracing"
)

// MultiNetworkStreamer extends Streamer with multi-network support
type MultiNetworkStreamer struct {
	Streamer
	routingEngine *routing.Engine
	enabled       bool
}

// NewMultiNetworkStreamer creates a new multi-network aware streamer
func NewMultiNetworkStreamer(
	compression compression.Compression,
	limitInMB float64,
	p2p P2PConfig,
	resourceCacheFactory db.ResourceCacheFactory,
	networkTopologyFactory db.NetworkTopologyFactory,
) *MultiNetworkStreamer {
	// Create base streamer
	baseStreamer := NewStreamer(compression, limitInMB, p2p, resourceCacheFactory)

	// Create routing components
	connectivityTester := routing.NewConnectivityTester(
		lagerctx.NewLogger("connectivity-tester"),
		networkTopologyFactory,
		5*time.Second,
	)

	routingEngine := routing.NewEngine(
		lagerctx.NewLogger("routing-engine"),
		networkTopologyFactory,
		connectivityTester,
	)

	return &MultiNetworkStreamer{
		Streamer:      baseStreamer,
		routingEngine: routingEngine,
		enabled:       true, // Can be made configurable
	}
}

// Stream overrides the base Stream method to use multi-network routing
func (s *MultiNetworkStreamer) Stream(ctx context.Context, src runtime.Artifact, dst runtime.Volume) error {
	if !s.enabled {
		// Fall back to original streamer if multi-network is disabled
		return s.Streamer.Stream(ctx, src, dst)
	}

	loggerData := lager.Data{
		"to":          dst.DBVolume().WorkerName(),
		"to-handle":   dst.Handle(),
		"from":        src.Source(),
		"from-handle": src.Handle(),
	}
	logger := lagerctx.FromContext(ctx).Session("multi-network-stream", loggerData)
	logger.Info("start")
	defer logger.Info("end")

	// Start timing the streaming operation
	startTime := time.Now()

	// Use routing engine to find best route
	err := s.streamWithRouting(ctx, src, dst)

	// Calculate duration
	duration := time.Since(startTime).Seconds()

	if err != nil {
		logger.Error("stream-failed", err, lager.Data{"duration_seconds": duration})
		return err
	}

	// Handle resource cache initialization (same as base streamer)
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

	// Handle resource cache (same as base implementation)
	return s.handleResourceCache(ctx, srcVolume, dst)
}

func (s *MultiNetworkStreamer) streamWithRouting(ctx context.Context, src runtime.Artifact, dst runtime.Volume) error {
	logger := lagerctx.FromContext(ctx)

	// Get source and destination workers
	srcWorker := ""
	if srcVolume, ok := src.(runtime.Volume); ok {
		srcWorker = srcVolume.DBVolume().WorkerName()
	}
	dstWorker := dst.DBVolume().WorkerName()

	if srcWorker == "" {
		// Source is not a volume (might be a resource), fall back to ATC streaming
		return s.streamThroughATCWithMetrics(ctx, src, dst, "no-source-worker")
	}

	// Find best route using routing engine
	route, err := s.routingEngine.FindRoute(ctx, srcWorker, dstWorker)
	if err != nil {
		logger.Error("route-selection-failed", err)
		// Fall back to ATC streaming if routing fails
		return s.streamThroughATCWithMetrics(ctx, src, dst, "routing-failed")
	}

	logger.Info("route-selected", lager.Data{
		"method":   route.Method,
		"priority": route.Priority,
		"relay":    route.RelayWorker,
	})

	// Stream based on selected route
	switch route.Method {
	case routing.RouteMethodDirect:
		return s.streamDirectMultiNetwork(ctx, src, dst, route)
	case routing.RouteMethodRelay:
		// Will be implemented in PR #3
		logger.Info("relay-not-yet-implemented")
		return s.streamThroughATCWithMetrics(ctx, src, dst, "relay-not-implemented")
	case routing.RouteMethodATC:
		return s.streamThroughATCWithMetrics(ctx, src, dst, "no-p2p-route")
	default:
		return fmt.Errorf("unknown route method: %s", route.Method)
	}
}

func (s *MultiNetworkStreamer) streamDirectMultiNetwork(ctx context.Context, src runtime.Artifact, dst runtime.Volume, route *routing.Route) error {
	logger := lagerctx.FromContext(ctx)

	// Check P2P capability
	p2pSrc, srcOk := src.(runtime.P2PVolume)
	p2pDst, dstOk := dst.(runtime.P2PVolume)

	if !srcOk || !dstOk {
		logger.Info("p2p-not-supported")
		return s.streamThroughATCWithMetrics(ctx, src, dst, "p2p-not-supported")
	}

	// Try each endpoint in order of priority
	var lastErr error
	for _, endpoint := range route.Endpoints {
		logger.Debug("trying-endpoint", lager.Data{
			"url":      endpoint.URL,
			"segment":  endpoint.NetworkSegment,
			"priority": endpoint.Priority,
		})

		startTime := time.Now()

		// Track in-progress P2P streaming
		metric.Emit(metric.Event{
			Name:       "volume streaming in progress",
			State:      "inc",
			Attributes: metric.Attributes{"method": "p2p"},
		})

		err := s.p2pStreamToEndpoint(ctx, p2pSrc, p2pDst, endpoint.URL)

		metric.Emit(metric.Event{
			Name:       "volume streaming in progress",
			State:      "dec",
			Attributes: metric.Attributes{"method": "p2p"},
		})

		duration := time.Since(startTime).Seconds()

		if err == nil {
			// Success!
			logger.Info("p2p-stream-success", lager.Data{
				"endpoint": endpoint.URL,
				"duration": duration,
				"segment":  endpoint.NetworkSegment,
			})

			metric.Metrics.VolumeStreamingP2PSuccess.Inc()
			metric.Emit(metric.Event{
				Name:       "volume streaming duration",
				Value:      duration,
				Attributes: metric.Attributes{"method": "p2p", "status": "success"},
			})

			// Emit network-specific metrics
			metric.Emit(metric.Event{
				Name:  "p2p streaming by network",
				Value: 1,
				Attributes: metric.Attributes{
					"segment": endpoint.NetworkSegment,
					"status":  "success",
				},
			})

			return nil
		}

		// This endpoint failed, try next
		logger.Error("endpoint-failed", err, lager.Data{
			"endpoint": endpoint.URL,
			"segment":  endpoint.NetworkSegment,
		})
		lastErr = err
	}

	// All endpoints failed, fall back to ATC
	logger.Error("all-p2p-endpoints-failed", lastErr)
	metric.Metrics.VolumeStreamingP2PFailure.Inc()
	metric.Metrics.VolumesStreamedViaFallback.Inc()

	return s.streamThroughATCWithMetrics(ctx, src, dst, "p2p-all-failed")
}

func (s *MultiNetworkStreamer) p2pStreamToEndpoint(ctx context.Context, src runtime.P2PVolume, dst runtime.P2PVolume, endpoint string) error {
	ctx, span := tracing.StartSpan(ctx, "streamer.p2pStreamToEndpoint", tracing.Attrs{
		"origin-volume": src.Handle(),
		"origin-worker": src.DBVolume().WorkerName(),
		"dest-volume":   dst.Handle(),
		"dest-worker":   dst.DBVolume().WorkerName(),
		"endpoint":      endpoint,
	})
	defer span.End()

	logger := lagerctx.FromContext(ctx).Session("p2p-stream-to-endpoint")

	// Parse and modify the endpoint to include streaming parameters
	u, err := url.Parse(endpoint)
	if err != nil {
		logger.Error("failed-to-parse-endpoint", err, lager.Data{"endpoint": endpoint})
		return fmt.Errorf("failed to parse endpoint: %w", err)
	}

	// Build stream-in URL from the P2P endpoint
	// Convert from http://worker:7788 to http://worker:7788/volumes/:handle/stream-in
	streamInURL := fmt.Sprintf("%s/volumes/%s/stream-in", endpoint, dst.Handle())
	u, err = url.Parse(streamInURL)
	if err != nil {
		return fmt.Errorf("failed to build stream-in URL: %w", err)
	}

	// Add query parameters
	qs := u.Query()
	qs.Set("path", ".")
	qs.Set("limit", fmt.Sprintf("%f", s.limitInMB))
	qs.Set("encoding", s.compression.Encoding())
	u.RawQuery = qs.Encode()
	streamInURL = u.String()

	// Apply timeout if configured
	if s.p2p.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.p2p.Timeout)
		defer cancel()
	}

	logger.Debug("streaming-to-endpoint", lager.Data{"url": streamInURL})
	return src.StreamP2POut(ctx, streamInURL, s.compression.Encoding())
}

func (s *MultiNetworkStreamer) handleResourceCache(ctx context.Context, srcVolume runtime.Volume, dst runtime.Volume) error {
	// Same as base implementation
	resourceCacheID := srcVolume.DBVolume().GetResourceCacheID()
	if resourceCacheID == 0 {
		return nil
	}

	logger := lagerctx.FromContext(ctx)
	logger.Debug("initialize-streamed-resource-cache", lager.Data{"resource-cache-id": resourceCacheID})

	usedResourceCache, found, err := s.resourceCacheFactory.FindResourceCacheByID(resourceCacheID)
	if err != nil {
		logger.Error("stream-to-failed-to-find-resource-cache", err)
		return err
	}
	if !found {
		logger.Info("stream-resource-cache-not-found", lager.Data{
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
	return nil
}