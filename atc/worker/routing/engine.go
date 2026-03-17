package routing

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
)

// Route represents a P2P route between workers
type Route struct {
	SourceWorker string
	DestWorker   string
	Method       RouteMethod
	Endpoints    []Endpoint
	Priority     int
	Latency      time.Duration
	Bandwidth    int // Mbps
	RelayWorker  string // Empty for direct routes
}

// RouteMethod defines how volumes will be streamed
type RouteMethod string

const (
	RouteMethodDirect RouteMethod = "direct"
	RouteMethodRelay  RouteMethod = "relay"
	RouteMethodATC    RouteMethod = "atc"
)

// Endpoint represents a P2P endpoint with metadata
type Endpoint struct {
	URL            string
	NetworkSegment string
	Priority       int
	CanConnect     bool
	Latency        time.Duration
}

// Engine is the P2P routing engine
type Engine struct {
	logger                 lager.Logger
	networkTopologyFactory db.NetworkTopologyFactory
	connectivityTester     ConnectivityTester
	cache                  *routeCache
	metricsEnabled         bool
}

// ConnectivityTester tests connectivity between workers
type ConnectivityTester interface {
	TestConnectivity(ctx context.Context, sourceWorker, destWorker string) (bool, time.Duration, error)
	TestEndpoint(ctx context.Context, endpoint string) (bool, time.Duration, error)
}

// routeCache caches routing decisions
type routeCache struct {
	mu     sync.RWMutex
	routes map[string]*Route
	expiry map[string]time.Time
	ttl    time.Duration
}

// NewEngine creates a new routing engine
func NewEngine(
	logger lager.Logger,
	networkTopologyFactory db.NetworkTopologyFactory,
	connectivityTester ConnectivityTester,
) *Engine {
	return &Engine{
		logger:                 logger.Session("routing-engine"),
		networkTopologyFactory: networkTopologyFactory,
		connectivityTester:     connectivityTester,
		cache: &routeCache{
			routes: make(map[string]*Route),
			expiry: make(map[string]time.Time),
			ttl:    5 * time.Minute,
		},
		metricsEnabled: true,
	}
}

// FindRoute finds the best route between two workers
func (e *Engine) FindRoute(ctx context.Context, sourceWorker, destWorker string) (*Route, error) {
	e.logger.Debug("finding-route", lager.Data{
		"source": sourceWorker,
		"dest":   destWorker,
	})

	// Start timing for metrics
	startTime := time.Now()
	defer func() {
		if e.metricsEnabled {
			duration := time.Since(startTime).Seconds()
			metric.Emit(metric.Event{
				Name:  "p2p route selection duration",
				Value: duration,
				Attributes: metric.Attributes{
					"source": sourceWorker,
					"dest":   destWorker,
				},
			})
		}
	}()

	// Check cache first
	cacheKey := fmt.Sprintf("%s->%s", sourceWorker, destWorker)
	if route := e.cache.get(cacheKey); route != nil {
		e.logger.Debug("route-cache-hit", lager.Data{"route": route.Method})
		return route, nil
	}

	// Get network topology
	topology, err := e.networkTopologyFactory.GetNetworkTopology()
	if err != nil {
		e.logger.Error("failed-to-get-topology", err)
		return nil, fmt.Errorf("failed to get network topology: %w", err)
	}

	// Find direct route first
	directRoute := e.findDirectRoute(ctx, sourceWorker, destWorker, topology)
	if directRoute != nil && directRoute.Endpoints != nil && len(directRoute.Endpoints) > 0 {
		// Test connectivity for direct route
		canConnect := false
		for _, endpoint := range directRoute.Endpoints {
			ok, latency, err := e.connectivityTester.TestEndpoint(ctx, endpoint.URL)
			if err == nil && ok {
				endpoint.CanConnect = true
				endpoint.Latency = latency
				canConnect = true
				directRoute.Latency = latency
				break
			}
		}

		if canConnect {
			e.logger.Info("direct-route-found", lager.Data{
				"source": sourceWorker,
				"dest":   destWorker,
				"latency": directRoute.Latency,
			})
			e.cache.set(cacheKey, directRoute)
			e.emitRouteMetrics(directRoute)
			return directRoute, nil
		}
	}

	// Try relay route if direct fails
	relayRoute := e.findRelayRoute(ctx, sourceWorker, destWorker, topology)
	if relayRoute != nil {
		e.logger.Info("relay-route-found", lager.Data{
			"source": sourceWorker,
			"dest":   destWorker,
			"relay":  relayRoute.RelayWorker,
		})
		e.cache.set(cacheKey, relayRoute)
		e.emitRouteMetrics(relayRoute)
		return relayRoute, nil
	}

	// Fallback to ATC streaming
	atcRoute := &Route{
		SourceWorker: sourceWorker,
		DestWorker:   destWorker,
		Method:       RouteMethodATC,
		Priority:     100, // Lowest priority
	}

	e.logger.Info("fallback-to-atc", lager.Data{
		"source": sourceWorker,
		"dest":   destWorker,
	})
	e.cache.set(cacheKey, atcRoute)
	e.emitRouteMetrics(atcRoute)
	return atcRoute, nil
}

// findDirectRoute finds a direct P2P route between workers
func (e *Engine) findDirectRoute(ctx context.Context, sourceWorker, destWorker string, topology db.NetworkTopology) *Route {
	// Get source worker's networks
	var sourceNetworks []db.WorkerNetwork
	for _, wn := range topology.WorkerNetworks {
		if wn.WorkerName == sourceWorker {
			sourceNetworks = append(sourceNetworks, wn)
		}
	}

	// Get destination worker's networks
	var destNetworks []db.WorkerNetwork
	for _, wn := range topology.WorkerNetworks {
		if wn.WorkerName == destWorker {
			destNetworks = append(destNetworks, wn)
		}
	}

	if len(sourceNetworks) == 0 || len(destNetworks) == 0 {
		e.logger.Debug("no-networks-found", lager.Data{
			"source": sourceWorker,
			"dest":   destWorker,
		})
		return nil
	}

	// Find common network segments
	var commonSegments []string
	for _, sn := range sourceNetworks {
		for _, dn := range destNetworks {
			if sn.SegmentID == dn.SegmentID {
				commonSegments = append(commonSegments, sn.SegmentID)
			}
		}
	}

	if len(commonSegments) == 0 {
		e.logger.Debug("no-common-segments", lager.Data{
			"source": sourceWorker,
			"dest":   destWorker,
		})
		return nil
	}

	// Build route with endpoints from common segments
	route := &Route{
		SourceWorker: sourceWorker,
		DestWorker:   destWorker,
		Method:       RouteMethodDirect,
		Priority:     1, // Highest priority for direct routes
	}

	for _, segment := range commonSegments {
		for _, dn := range destNetworks {
			if dn.SegmentID == segment {
				endpoint := Endpoint{
					URL:            dn.P2PEndpoint,
					NetworkSegment: segment,
					Priority:       1,
				}
				route.Endpoints = append(route.Endpoints, endpoint)
			}
		}
	}

	// Sort endpoints by priority
	sort.Slice(route.Endpoints, func(i, j int) bool {
		return route.Endpoints[i].Priority < route.Endpoints[j].Priority
	})

	return route
}

// findRelayRoute finds a route through a relay worker
func (e *Engine) findRelayRoute(ctx context.Context, sourceWorker, destWorker string, topology db.NetworkTopology) *Route {
	// This will be implemented in PR #3
	// For now, return nil to fall back to ATC
	return nil
}

// emitRouteMetrics emits metrics for the selected route
func (e *Engine) emitRouteMetrics(route *Route) {
	if !e.metricsEnabled {
		return
	}

	metric.Emit(metric.Event{
		Name:  "p2p routes by method",
		Value: 1,
		Attributes: metric.Attributes{
			"method": string(route.Method),
			"source": route.SourceWorker,
			"dest":   route.DestWorker,
		},
	})

	if route.Method == RouteMethodRelay && route.RelayWorker != "" {
		metric.Emit(metric.Event{
			Name:  "p2p relay routes",
			Value: 1,
			Attributes: metric.Attributes{
				"relay": route.RelayWorker,
			},
		})
	}
}

// GetP2PURLs returns all P2P URLs for a worker
func (e *Engine) GetP2PURLs(ctx context.Context, workerName string) ([]atc.P2PEndpoint, error) {
	networks, err := e.networkTopologyFactory.GetWorkerNetworks(workerName)
	if err != nil {
		return nil, err
	}

	var endpoints []atc.P2PEndpoint
	for _, network := range networks {
		// Get segment priority
		segment, found, err := e.networkTopologyFactory.GetNetworkSegment(network.SegmentID)
		if err != nil || !found {
			continue
		}

		endpoints = append(endpoints, atc.P2PEndpoint{
			URL:            network.P2PEndpoint,
			NetworkSegment: network.SegmentID,
			Priority:       segment.Priority,
			Bandwidth:      fmt.Sprintf("%dmbps", network.BandwidthMbps),
		})
	}

	// Sort by priority
	sort.Slice(endpoints, func(i, j int) bool {
		return endpoints[i].Priority < endpoints[j].Priority
	})

	return endpoints, nil
}

// TestAndUpdateConnectivity tests and updates connectivity between workers
func (e *Engine) TestAndUpdateConnectivity(ctx context.Context, sourceWorker, destWorker string) error {
	canConnect, latency, err := e.connectivityTester.TestConnectivity(ctx, sourceWorker, destWorker)

	connectivity := db.WorkerConnectivity{
		SourceWorker: sourceWorker,
		DestWorker:   destWorker,
		CanConnect:   canConnect,
		LatencyMs:    int(latency.Milliseconds()),
		LastTested:   time.Now(),
	}

	if err != nil {
		connectivity.TestError = err.Error()
	}

	// Update metrics
	metric.Metrics.WorkerConnectivityTests.Inc()
	if canConnect {
		metric.Metrics.P2PConnectivityTestSuccess.Inc()
	} else {
		metric.Metrics.P2PConnectivityTestFailure.Inc()
	}

	return e.networkTopologyFactory.UpdateWorkerConnectivity(connectivity)
}

// ClearCache clears the route cache
func (e *Engine) ClearCache() {
	e.cache.clear()
}

// routeCache methods

func (c *routeCache) get(key string) *Route {
	c.mu.RLock()
	defer c.mu.RUnlock()

	expiry, exists := c.expiry[key]
	if !exists || time.Now().After(expiry) {
		return nil
	}

	return c.routes[key]
}

func (c *routeCache) set(key string, route *Route) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.routes[key] = route
	c.expiry[key] = time.Now().Add(c.ttl)
}

func (c *routeCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.routes = make(map[string]*Route)
	c.expiry = make(map[string]time.Time)
}