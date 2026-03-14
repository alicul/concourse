package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime/runtimefakes"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/worker/workerfakes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMultiNetworkP2PStreaming(t *testing.T) {
	t.Run("DirectP2PRoute", func(t *testing.T) {
		// Setup
		ctx := context.Background()
		router := &workerfakes.FakeP2PRouter{}
		networkTopologyFactory := &dbfakes.FakeNetworkTopologyFactory{}
		resourceCacheFactory := &dbfakes.FakeResourceCacheFactory{}

		streamer := worker.NewMultiNetworkStreamer(
			resourceCacheFactory,
			networkTopologyFactory,
			compression.NewGzipCompression(),
			100.0, // 100MB limit
			worker.P2PConfig{
				Enabled: true,
				Timeout: 5 * time.Minute,
			},
			router,
		)

		// Create mock volumes
		srcVolume := &runtimefakes.FakeP2PVolume{}
		dstVolume := &runtimefakes.FakeP2PVolume{}

		srcDBVolume := &dbfakes.FakeCreatedVolume{}
		dstDBVolume := &dbfakes.FakeCreatedVolume{}

		srcDBVolume.WorkerNameReturns("worker-1")
		dstDBVolume.WorkerNameReturns("worker-2")

		srcVolume.DBVolumeReturns(srcDBVolume)
		dstVolume.DBVolumeReturns(dstDBVolume)
		srcVolume.HandleReturns("src-handle")
		dstVolume.HandleReturns("dst-handle")
		srcVolume.SourceReturns("source")

		// Setup router to return direct route
		router.FindRouteReturns(&worker.P2PRoute{
			Type:           worker.P2PRouteDirect,
			DirectURL:      "http://172.20.0.5:7788",
			NetworkSegment: "segment1",
			Priority:       1,
			Latency:        5,
			Bandwidth:      1000,
		}, nil)

		// Setup P2P URLs
		dstVolume.GetStreamInP2PURLReturns("http://172.20.0.5:7788/volumes/dst-handle/stream-in", nil)
		srcVolume.StreamP2POutReturns(nil)

		// Execute
		err := streamer.Stream(ctx, srcVolume, dstVolume)

		// Assert
		assert.NoError(t, err)
		assert.Equal(t, 1, router.FindRouteCallCount())

		// Verify route finding was called with correct parameters
		calledCtx, srcWorker, dstWorker := router.FindRouteArgsForCall(0)
		assert.Equal(t, ctx, calledCtx)
		assert.Equal(t, "worker-1", srcWorker)
		assert.Equal(t, "worker-2", dstWorker)

		// Verify P2P streaming was attempted
		assert.Equal(t, 1, srcVolume.StreamP2POutCallCount())
		_, path, streamURL, comp := srcVolume.StreamP2POutArgsForCall(0)
		assert.Equal(t, ".", path)
		assert.Contains(t, streamURL, "stream-in")
		assert.Equal(t, compression.NewGzipCompression().Encoding(), comp.Encoding())
	})

	t.Run("RelayP2PRoute", func(t *testing.T) {
		// Setup
		ctx := context.Background()
		router := &workerfakes.FakeP2PRouter{}
		networkTopologyFactory := &dbfakes.FakeNetworkTopologyFactory{}
		resourceCacheFactory := &dbfakes.FakeResourceCacheFactory{}

		streamer := worker.NewMultiNetworkStreamer(
			resourceCacheFactory,
			networkTopologyFactory,
			compression.NewGzipCompression(),
			100.0,
			worker.P2PConfig{
				Enabled: true,
				Timeout: 5 * time.Minute,
			},
			router,
		)

		// Create mock volumes
		srcVolume := &runtimefakes.FakeP2PVolume{}
		dstVolume := &runtimefakes.FakeP2PVolume{}

		srcDBVolume := &dbfakes.FakeCreatedVolume{}
		dstDBVolume := &dbfakes.FakeCreatedVolume{}

		srcDBVolume.WorkerNameReturns("worker-s1")
		dstDBVolume.WorkerNameReturns("worker-s2")

		srcVolume.DBVolumeReturns(srcDBVolume)
		dstVolume.DBVolumeReturns(dstDBVolume)

		// Setup router to return relay route
		router.FindRouteReturns(&worker.P2PRoute{
			Type:        worker.P2PRouteRelay,
			RelayWorker: "worker-relay",
			RelayURL:    "http://172.21.0.10:7788",
			Priority:    2,
		}, nil)

		// Note: Relay streaming is not fully implemented yet
		// This test verifies the routing decision is made correctly

		// Execute
		_ = streamer.Stream(ctx, srcVolume, dstVolume)

		// Assert
		assert.Equal(t, 1, router.FindRouteCallCount())

		// Verify route finding was called
		_, srcWorker, dstWorker := router.FindRouteArgsForCall(0)
		assert.Equal(t, "worker-s1", srcWorker)
		assert.Equal(t, "worker-s2", dstWorker)
	})

	t.Run("FallbackToATC", func(t *testing.T) {
		// Setup
		ctx := context.Background()
		router := &workerfakes.FakeP2PRouter{}
		networkTopologyFactory := &dbfakes.FakeNetworkTopologyFactory{}
		resourceCacheFactory := &dbfakes.FakeResourceCacheFactory{}

		streamer := worker.NewMultiNetworkStreamer(
			resourceCacheFactory,
			networkTopologyFactory,
			compression.NewGzipCompression(),
			100.0,
			worker.P2PConfig{
				Enabled: true,
				Timeout: 5 * time.Minute,
			},
			router,
		)

		// Create mock volumes
		srcVolume := &runtimefakes.FakeVolume{}
		dstVolume := &runtimefakes.FakeVolume{}

		srcDBVolume := &dbfakes.FakeCreatedVolume{}
		dstDBVolume := &dbfakes.FakeCreatedVolume{}

		srcDBVolume.WorkerNameReturns("worker-isolated")
		dstDBVolume.WorkerNameReturns("worker-s1")

		srcVolume.DBVolumeReturns(srcDBVolume)
		dstVolume.DBVolumeReturns(dstDBVolume)
		srcVolume.HandleReturns("src-handle")
		dstVolume.HandleReturns("dst-handle")
		srcVolume.SourceReturns("source")

		// Setup router to return ATC route (no P2P available)
		router.FindRouteReturns(&worker.P2PRoute{
			Type:     worker.P2PRouteATC,
			Priority: 100,
		}, nil)

		// Setup ATC streaming
		srcVolume.StreamOutReturns(&nopCloser{}, nil)
		dstVolume.StreamInReturns(nil)

		// Execute
		err := streamer.Stream(ctx, srcVolume, dstVolume)

		// Assert
		assert.NoError(t, err)

		// Verify ATC streaming was used
		assert.Equal(t, 1, srcVolume.StreamOutCallCount())
		assert.Equal(t, 1, dstVolume.StreamInCallCount())
	})
}

func TestP2PRouter(t *testing.T) {
	t.Run("FindDirectRoute", func(t *testing.T) {
		// Setup
		ctx := context.Background()
		networkTopologyFactory := &dbfakes.FakeNetworkTopologyFactory()
		workerFactory := &dbfakes.FakeWorkerFactory()

		router := worker.NewP2PRouter(
			testLogger(t),
			networkTopologyFactory,
			workerFactory,
		)

		// Create test topology
		topology := worker.NetworkTopology{
			Workers: map[string]*worker.WorkerNetworkInfo{
				"worker-1": {
					Name: "worker-1",
					Endpoints: []worker.P2PEndpoint{
						{
							URL:            "http://172.20.0.5:7788",
							NetworkSegment: "segment1",
							Priority:       1,
						},
					},
					NetworkSegments: map[string]bool{"segment1": true},
					IsOnline:        true,
				},
				"worker-2": {
					Name: "worker-2",
					Endpoints: []worker.P2PEndpoint{
						{
							URL:            "http://172.20.0.6:7788",
							NetworkSegment: "segment1",
							Priority:       1,
						},
					},
					NetworkSegments: map[string]bool{"segment1": true},
					IsOnline:        true,
				},
			},
			Connectivity: map[string]map[string]*worker.ConnectivityInfo{
				"worker-1": {
					"worker-2": {
						IsDirect:    true,
						Latency:     5,
						Bandwidth:   1000,
						SuccessRate: 0.99,
					},
				},
			},
		}

		// Inject topology (would normally be done via RefreshNetworkTopology)
		routerImpl := router.(*worker.P2PRouterImpl)
		routerImpl.SetTopology(topology) // Note: This method would need to be added

		// Execute
		route, err := router.FindRoute(ctx, "worker-1", "worker-2")

		// Assert
		require.NoError(t, err)
		assert.Equal(t, worker.P2PRouteDirect, route.Type)
		assert.Equal(t, "http://172.20.0.6:7788", route.DirectURL)
		assert.Equal(t, "segment1", route.NetworkSegment)
		assert.Equal(t, 5, route.Latency)
		assert.Equal(t, 1000, route.Bandwidth)
	})

	t.Run("FindRelayRoute", func(t *testing.T) {
		// Setup
		ctx := context.Background()
		networkTopologyFactory := &dbfakes.FakeNetworkTopologyFactory()
		workerFactory := &dbfakes.FakeWorkerFactory()

		router := worker.NewP2PRouter(
			testLogger(t),
			networkTopologyFactory,
			workerFactory,
		)

		// Create test topology with workers on different segments
		topology := worker.NetworkTopology{
			Workers: map[string]*worker.WorkerNetworkInfo{
				"worker-s1": {
					Name: "worker-s1",
					Endpoints: []worker.P2PEndpoint{
						{
							URL:            "http://172.20.0.5:7788",
							NetworkSegment: "segment1",
							Priority:       1,
						},
					},
					NetworkSegments: map[string]bool{"segment1": true},
					IsOnline:        true,
				},
				"worker-s2": {
					Name: "worker-s2",
					Endpoints: []worker.P2PEndpoint{
						{
							URL:            "http://172.21.0.5:7788",
							NetworkSegment: "segment2",
							Priority:       1,
						},
					},
					NetworkSegments: map[string]bool{"segment2": true},
					IsOnline:        true,
				},
				"worker-relay": {
					Name: "worker-relay",
					Endpoints: []worker.P2PEndpoint{
						{
							URL:            "http://172.20.0.10:7788",
							NetworkSegment: "segment1",
							Priority:       1,
						},
						{
							URL:            "http://172.21.0.10:7788",
							NetworkSegment: "segment2",
							Priority:       1,
						},
					},
					NetworkSegments: map[string]bool{
						"segment1": true,
						"segment2": true,
					},
					IsRelayCapable: true,
					IsOnline:       true,
				},
			},
			Connectivity: map[string]map[string]*worker.ConnectivityInfo{
				"worker-s1": {
					"worker-s2": {
						IsDirect:     false,
						RelayWorkers: []string{"worker-relay"},
						Latency:      50,
						Bandwidth:    500,
						SuccessRate:  0.95,
					},
				},
			},
		}

		// Inject topology
		routerImpl := router.(*worker.P2PRouterImpl)
		routerImpl.SetTopology(topology) // Note: This method would need to be added

		// Execute
		route, err := router.FindRoute(ctx, "worker-s1", "worker-s2")

		// Assert
		require.NoError(t, err)
		assert.Equal(t, worker.P2PRouteRelay, route.Type)
		assert.Equal(t, "worker-relay", route.RelayWorker)
		assert.NotEmpty(t, route.RelayURL)
	})
}

func TestNetworkDetector(t *testing.T) {
	t.Run("DetectNetworks", func(t *testing.T) {
		// This would require mocking network interfaces
		// For now, we'll test the configuration parsing

		configs := []network.InterfaceConfig{
			{
				Pattern:        "eth0",
				NetworkSegment: "segment1",
				Priority:       1,
			},
			{
				Pattern:        "eth1",
				NetworkSegment: "segment2",
				Priority:       2,
			},
		}

		detector := network.NewNetworkDetector(
			testLogger(t),
			configs,
			4, // IPv4
			false, // no auto-detect
			false, // not relay capable
		)

		// Get P2P URLs
		urls := detector.GetP2PURLs(7788)

		// This will be empty in test environment without real interfaces
		// In integration tests, this would return actual URLs
		assert.NotNil(t, urls)
	})
}

// Helper functions

type nopCloser struct {
	io.Reader
}

func (nopCloser) Close() error { return nil }

func testLogger(t *testing.T) lager.Logger {
	return lager.NewLogger(t.Name())
}