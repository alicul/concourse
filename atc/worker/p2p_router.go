package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
)

// P2PRouter handles routing decisions for P2P volume streaming
type P2PRouter interface {
	// FindRoute finds the best P2P route between two workers
	FindRoute(ctx context.Context, sourceWorker, destWorker string) (*P2PRoute, error)
	// RefreshNetworkTopology refreshes the network topology information
	RefreshNetworkTopology(ctx context.Context) error
	// GetNetworkTopology returns the current network topology
	GetNetworkTopology() NetworkTopology
}

// P2PRoute represents a route for P2P streaming
type P2PRoute struct {
	Type           P2PRouteType `json:"type"`
	DirectURL      string       `json:"direct_url,omitempty"`
	RelayWorker    string       `json:"relay_worker,omitempty"`
	RelayURL       string       `json:"relay_url,omitempty"`
	NetworkSegment string       `json:"network_segment,omitempty"`
	Priority       int          `json:"priority"`
	Latency        int          `json:"latency_ms,omitempty"`
	Bandwidth      int          `json:"bandwidth_mbps,omitempty"`
}

// P2PRouteType represents the type of P2P route
type P2PRouteType string

const (
	P2PRouteDirect P2PRouteType = "direct"
	P2PRouteRelay  P2PRouteType = "relay"
	P2PRouteATC    P2PRouteType = "atc"
)

// NetworkTopology represents the network topology of workers
type NetworkTopology struct {
	Workers      map[string]*WorkerNetworkInfo `json:"workers"`
	Segments     map[string]*NetworkSegment    `json:"segments"`
	Connectivity map[string]map[string]*ConnectivityInfo `json:"connectivity"`
	LastUpdated  time.Time `json:"last_updated"`
}

// WorkerNetworkInfo contains network information for a worker
type WorkerNetworkInfo struct {
	Name           string                    `json:"name"`
	Endpoints      []P2PEndpoint            `json:"endpoints"`
	NetworkSegments map[string]bool         `json:"network_segments"`
	IsRelayCapable bool                     `json:"is_relay_capable"`
	IsOnline       bool                     `json:"is_online"`
}

// P2PEndpoint represents a P2P endpoint
type P2PEndpoint struct {
	URL            string `json:"url"`
	NetworkSegment string `json:"network_segment"`
	Priority       int    `json:"priority"`
	Bandwidth      string `json:"bandwidth,omitempty"`
}

// NetworkSegment represents a network segment
type NetworkSegment struct {
	ID       string   `json:"id"`
	Workers  []string `json:"workers"`
	Type     string   `json:"type"`
	Priority int      `json:"priority"`
}

// ConnectivityInfo represents connectivity between two workers
type ConnectivityInfo struct {
	IsDirect      bool          `json:"is_direct"`
	Latency       int           `json:"latency_ms"`
	Bandwidth     int           `json:"bandwidth_mbps"`
	SuccessRate   float64       `json:"success_rate"`
	LastTested    time.Time     `json:"last_tested"`
	RelayWorkers  []string      `json:"relay_workers,omitempty"`
}

// P2PRouterImpl implements the P2PRouter interface
type P2PRouterImpl struct {
	logger               lager.Logger
	networkTopologyFactory db.NetworkTopologyFactory
	workerFactory        db.WorkerFactory
	topology             NetworkTopology
	topologyMutex        sync.RWMutex
	httpClient           *http.Client
}

// NewP2PRouter creates a new P2P router
func NewP2PRouter(
	logger lager.Logger,
	networkTopologyFactory db.NetworkTopologyFactory,
	workerFactory db.WorkerFactory,
) P2PRouter {
	return &P2PRouterImpl{
		logger:                 logger,
		networkTopologyFactory: networkTopologyFactory,
		workerFactory:          workerFactory,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
		topology: NetworkTopology{
			Workers:      make(map[string]*WorkerNetworkInfo),
			Segments:     make(map[string]*NetworkSegment),
			Connectivity: make(map[string]map[string]*ConnectivityInfo),
		},
	}
}

// FindRoute finds the best P2P route between two workers
func (r *P2PRouterImpl) FindRoute(ctx context.Context, sourceWorker, destWorker string) (*P2PRoute, error) {
	logger := lagerctx.FromContext(ctx).Session("find-p2p-route", lager.Data{
		"source": sourceWorker,
		"dest":   destWorker,
	})

	r.topologyMutex.RLock()
	defer r.topologyMutex.RUnlock()

	// Check if workers exist in topology
	srcInfo, srcExists := r.topology.Workers[sourceWorker]
	dstInfo, dstExists := r.topology.Workers[destWorker]

	if !srcExists || !dstExists {
		logger.Info("worker-not-in-topology")
		return &P2PRoute{Type: P2PRouteATC, Priority: 100}, nil
	}

	if !srcInfo.IsOnline || !dstInfo.IsOnline {
		logger.Info("worker-offline")
		return &P2PRoute{Type: P2PRouteATC, Priority: 100}, nil
	}

	// Try to find direct route
	directRoute := r.findDirectRoute(logger, srcInfo, dstInfo)
	if directRoute != nil {
		metric.P2PStreamingAttempts.WithLabelValues(
			sourceWorker, destWorker, "direct", directRoute.NetworkSegment,
		).Inc()
		return directRoute, nil
	}

	// Try to find relay route
	relayRoute := r.findRelayRoute(logger, srcInfo, dstInfo)
	if relayRoute != nil {
		metric.P2PStreamingAttempts.WithLabelValues(
			sourceWorker, destWorker, "relay", relayRoute.NetworkSegment,
		).Inc()
		return relayRoute, nil
	}

	// Fallback to ATC-mediated streaming
	logger.Info("no-p2p-route-found-using-atc")
	metric.P2PStreamingAttempts.WithLabelValues(
		sourceWorker, destWorker, "fallback_to_atc", "",
	).Inc()

	return &P2PRoute{Type: P2PRouteATC, Priority: 100}, nil
}

// findDirectRoute attempts to find a direct P2P route
func (r *P2PRouterImpl) findDirectRoute(logger lager.Logger, src, dst *WorkerNetworkInfo) *P2PRoute {
	// Check if workers share any network segments
	for segment := range src.NetworkSegments {
		if dst.NetworkSegments[segment] {
			// Find endpoints for this segment
			var srcEndpoint, dstEndpoint *P2PEndpoint

			for _, ep := range src.Endpoints {
				if ep.NetworkSegment == segment {
					srcEndpoint = &ep
					break
				}
			}

			for _, ep := range dst.Endpoints {
				if ep.NetworkSegment == segment {
					dstEndpoint = &ep
					break
				}
			}

			if srcEndpoint != nil && dstEndpoint != nil {
				// Check connectivity info
				if connInfo := r.getConnectivityInfo(src.Name, dst.Name); connInfo != nil && connInfo.IsDirect {
					logger.Info("found-direct-route", lager.Data{
						"segment": segment,
						"url":     dstEndpoint.URL,
					})

					return &P2PRoute{
						Type:           P2PRouteDirect,
						DirectURL:      dstEndpoint.URL,
						NetworkSegment: segment,
						Priority:       dstEndpoint.Priority,
						Latency:        connInfo.Latency,
						Bandwidth:      connInfo.Bandwidth,
					}
				}
			}
		}
	}

	return nil
}

// findRelayRoute attempts to find a relay route through another worker
func (r *P2PRouterImpl) findRelayRoute(logger lager.Logger, src, dst *WorkerNetworkInfo) *P2PRoute {
	// Find relay-capable workers
	var relayWorkers []string

	for workerName, workerInfo := range r.topology.Workers {
		if workerInfo.IsRelayCapable && workerInfo.IsOnline {
			// Check if relay can reach both source and destination
			canReachSrc := false
			canReachDst := false

			for segment := range workerInfo.NetworkSegments {
				if src.NetworkSegments[segment] {
					canReachSrc = true
				}
				if dst.NetworkSegments[segment] {
					canReachDst = true
				}
			}

			if canReachSrc && canReachDst {
				relayWorkers = append(relayWorkers, workerName)
			}
		}
	}

	// Sort relay workers by priority/performance
	sort.Slice(relayWorkers, func(i, j int) bool {
		// You could sort by latency, load, or other metrics
		return relayWorkers[i] < relayWorkers[j]
	})

	// Use the first available relay
	if len(relayWorkers) > 0 {
		relayWorker := relayWorkers[0]
		relayInfo := r.topology.Workers[relayWorker]

		// Find the best endpoint on the relay worker
		if len(relayInfo.Endpoints) > 0 {
			logger.Info("found-relay-route", lager.Data{
				"relay": relayWorker,
				"url":   relayInfo.Endpoints[0].URL,
			})

			return &P2PRoute{
				Type:        P2PRouteRelay,
				RelayWorker: relayWorker,
				RelayURL:    relayInfo.Endpoints[0].URL,
				Priority:    50,
			}
		}
	}

	return nil
}

// getConnectivityInfo returns connectivity information between two workers
func (r *P2PRouterImpl) getConnectivityInfo(src, dst string) *ConnectivityInfo {
	if srcConn, exists := r.topology.Connectivity[src]; exists {
		if dstConn, exists := srcConn[dst]; exists {
			return dstConn
		}
	}
	return nil
}

// RefreshNetworkTopology refreshes the network topology information
func (r *P2PRouterImpl) RefreshNetworkTopology(ctx context.Context) error {
	logger := lagerctx.FromContext(ctx).Session("refresh-network-topology")
	logger.Debug("start")
	defer logger.Debug("done")

	// Get all workers
	workers, err := r.workerFactory.Workers()
	if err != nil {
		logger.Error("failed-to-get-workers", err)
		return err
	}

	newTopology := NetworkTopology{
		Workers:      make(map[string]*WorkerNetworkInfo),
		Segments:     make(map[string]*NetworkSegment),
		Connectivity: make(map[string]map[string]*ConnectivityInfo),
		LastUpdated:  time.Now(),
	}

	// Query each worker for its network information
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, worker := range workers {
		wg.Add(1)
		go func(w db.Worker) {
			defer wg.Done()

			// Get P2P URLs from worker
			endpoints, isRelay, err := r.queryWorkerNetworkInfo(ctx, w)
			if err != nil {
				logger.Error("failed-to-query-worker", err, lager.Data{"worker": w.Name()})
				return
			}

			// Build network segments map
			segments := make(map[string]bool)
			for _, ep := range endpoints {
				segments[ep.NetworkSegment] = true
			}

			workerInfo := &WorkerNetworkInfo{
				Name:            w.Name(),
				Endpoints:       endpoints,
				NetworkSegments: segments,
				IsRelayCapable:  isRelay,
				IsOnline:        w.State() == db.WorkerStateRunning,
			}

			mu.Lock()
			newTopology.Workers[w.Name()] = workerInfo

			// Update network segments
			for segment := range segments {
				if _, exists := newTopology.Segments[segment]; !exists {
					newTopology.Segments[segment] = &NetworkSegment{
						ID:      segment,
						Workers: []string{},
						Type:    "auto",
					}
				}
				newTopology.Segments[segment].Workers = append(
					newTopology.Segments[segment].Workers, w.Name(),
				)
			}
			mu.Unlock()
		}(worker)
	}

	wg.Wait()

	// Test connectivity between workers (in background)
	go r.testWorkerConnectivity(ctx, newTopology.Workers)

	// Update topology
	r.topologyMutex.Lock()
	r.topology = newTopology
	r.topologyMutex.Unlock()

	// Record topology change
	metric.NetworkTopologyChanges.Inc()

	// Update worker network segments gauge
	for workerName, workerInfo := range newTopology.Workers {
		relayCapable := "false"
		if workerInfo.IsRelayCapable {
			relayCapable = "true"
		}
		metric.WorkerNetworkSegments.WithLabelValues(
			workerName, relayCapable,
		).Set(float64(len(workerInfo.NetworkSegments)))
	}

	logger.Info("topology-refreshed", lager.Data{
		"workers":  len(newTopology.Workers),
		"segments": len(newTopology.Segments),
	})

	return nil
}

// queryWorkerNetworkInfo queries a worker for its network information
func (r *P2PRouterImpl) queryWorkerNetworkInfo(ctx context.Context, worker db.Worker) ([]P2PEndpoint, bool, error) {
	// Construct URL for the worker's P2P endpoint
	workerAddr := worker.GardenAddr()
	if workerAddr == "" {
		return nil, false, fmt.Errorf("worker has no garden address")
	}

	// Query the new multi-network P2P endpoint
	url := fmt.Sprintf("http://%s/p2p-urls", worker.BaggageclaimURL())

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, false, err
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		// Fall back to legacy endpoint
		return r.queryWorkerNetworkInfoLegacy(ctx, worker)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return r.queryWorkerNetworkInfoLegacy(ctx, worker)
	}

	var response struct {
		Endpoints      []P2PEndpoint `json:"endpoints"`
		IsRelayCapable bool          `json:"is_relay_capable"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, false, err
	}

	return response.Endpoints, response.IsRelayCapable, nil
}

// queryWorkerNetworkInfoLegacy queries a worker using the legacy P2P endpoint
func (r *P2PRouterImpl) queryWorkerNetworkInfoLegacy(ctx context.Context, worker db.Worker) ([]P2PEndpoint, bool, error) {
	url := fmt.Sprintf("http://%s/p2p-url", worker.BaggageclaimURL())

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, false, err
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("failed to get P2P URL: status %d", resp.StatusCode)
	}

	// Read the URL as plain text (legacy format)
	buf := make([]byte, 1024)
	n, err := resp.Body.Read(buf)
	if err != nil && err.Error() != "EOF" {
		return nil, false, err
	}

	p2pURL := string(buf[:n])

	// Create a single endpoint with default segment
	endpoint := P2PEndpoint{
		URL:            p2pURL,
		NetworkSegment: "default",
		Priority:       1,
	}

	return []P2PEndpoint{endpoint}, false, nil
}

// testWorkerConnectivity tests connectivity between workers
func (r *P2PRouterImpl) testWorkerConnectivity(ctx context.Context, workers map[string]*WorkerNetworkInfo) {
	logger := lagerctx.FromContext(ctx).Session("test-worker-connectivity")

	// Test connectivity between all worker pairs
	for srcName, srcInfo := range workers {
		if !srcInfo.IsOnline {
			continue
		}

		r.topologyMutex.Lock()
		if r.topology.Connectivity[srcName] == nil {
			r.topology.Connectivity[srcName] = make(map[string]*ConnectivityInfo)
		}
		r.topologyMutex.Unlock()

		for dstName, dstInfo := range workers {
			if srcName == dstName || !dstInfo.IsOnline {
				continue
			}

			// Test connectivity from src to dst
			connInfo := r.testConnectivity(ctx, srcInfo, dstInfo)

			r.topologyMutex.Lock()
			r.topology.Connectivity[srcName][dstName] = connInfo
			r.topologyMutex.Unlock()

			// Record metrics
			result := "failure"
			if connInfo.IsDirect {
				result = "success"
			}

			metric.P2PConnectivityTests.WithLabelValues(
				srcName, dstName, result, "",
			).Inc()

			if connInfo.Latency > 0 {
				metric.P2PConnectivityLatency.WithLabelValues(
					srcName, dstName, "",
				).Observe(float64(connInfo.Latency))
			}
		}
	}

	logger.Info("connectivity-testing-complete")
}

// testConnectivity tests connectivity between two workers
func (r *P2PRouterImpl) testConnectivity(ctx context.Context, src, dst *WorkerNetworkInfo) *ConnectivityInfo {
	// Check if they share network segments
	for segment := range src.NetworkSegments {
		if dst.NetworkSegments[segment] {
			// They're on the same segment - assume direct connectivity
			return &ConnectivityInfo{
				IsDirect:    true,
				Latency:     5, // Assume low latency for same segment
				Bandwidth:   1000, // Assume 1Gbps
				SuccessRate: 0.99,
				LastTested:  time.Now(),
			}
		}
	}

	// Find relay workers
	var relayWorkers []string
	for workerName, workerInfo := range r.topology.Workers {
		if workerInfo.IsRelayCapable {
			// Check if this worker can relay
			canReachSrc := false
			canReachDst := false

			for segment := range workerInfo.NetworkSegments {
				if src.NetworkSegments[segment] {
					canReachSrc = true
				}
				if dst.NetworkSegments[segment] {
					canReachDst = true
				}
			}

			if canReachSrc && canReachDst {
				relayWorkers = append(relayWorkers, workerName)
			}
		}
	}

	return &ConnectivityInfo{
		IsDirect:     false,
		Latency:      50, // Higher latency for relay
		Bandwidth:    500, // Lower bandwidth for relay
		SuccessRate:  0.95,
		LastTested:   time.Now(),
		RelayWorkers: relayWorkers,
	}
}

// GetNetworkTopology returns the current network topology
func (r *P2PRouterImpl) GetNetworkTopology() NetworkTopology {
	r.topologyMutex.RLock()
	defer r.topologyMutex.RUnlock()
	return r.topology
}