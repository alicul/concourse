package relay

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
)

// LoadBalancer manages load balancing across relay workers
type LoadBalancer struct {
	logger                 lager.Logger
	networkTopologyFactory db.NetworkTopologyFactory
	strategy               LoadBalancingStrategy
	mu                     sync.RWMutex
	relayStats             map[string]*RelayStats
	lastUpdate             time.Time
}

// LoadBalancingStrategy defines how to select relay workers
type LoadBalancingStrategy string

const (
	StrategyRoundRobin       LoadBalancingStrategy = "round-robin"
	StrategyLeastConnections LoadBalancingStrategy = "least-connections"
	StrategyWeightedRandom   LoadBalancingStrategy = "weighted-random"
	StrategyLatencyBased     LoadBalancingStrategy = "latency-based"
)

// RelayStats tracks statistics for a relay worker
type RelayStats struct {
	WorkerName         string
	ActiveConnections  int
	TotalConnections   int64
	TotalBytesRelayed  int64
	AverageLatency     time.Duration
	LastUsed           time.Time
	SuccessRate        float64
	CurrentLoad        float64 // 0.0 to 1.0
}

// RelaySelection represents a selected relay worker
type RelaySelection struct {
	WorkerName string
	Endpoints  []string
	Priority   int
	Reason     string
}

// NewLoadBalancer creates a new relay load balancer
func NewLoadBalancer(
	logger lager.Logger,
	networkTopologyFactory db.NetworkTopologyFactory,
	strategy LoadBalancingStrategy,
) *LoadBalancer {
	return &LoadBalancer{
		logger:                 logger.Session("relay-load-balancer"),
		networkTopologyFactory: networkTopologyFactory,
		strategy:               strategy,
		relayStats:             make(map[string]*RelayStats),
	}
}

// SelectRelay selects the best relay worker for a streaming operation
func (lb *LoadBalancer) SelectRelay(
	ctx context.Context,
	sourceSegments []string,
	destSegments []string,
) (*RelaySelection, error) {
	lb.logger.Debug("selecting-relay", lager.Data{
		"strategy":        lb.strategy,
		"source_segments": sourceSegments,
		"dest_segments":   destSegments,
	})

	// Get current topology
	topology, err := lb.networkTopologyFactory.GetNetworkTopology()
	if err != nil {
		return nil, fmt.Errorf("failed to get network topology: %w", err)
	}

	// Find eligible relay workers
	candidates := lb.findEligibleRelays(topology, sourceSegments, destSegments)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no eligible relay workers found")
	}

	// Update relay statistics if needed
	if time.Since(lb.lastUpdate) > 30*time.Second {
		lb.updateRelayStats(topology)
	}

	// Select relay based on strategy
	var selected *relayCandidate
	var reason string

	switch lb.strategy {
	case StrategyRoundRobin:
		selected, reason = lb.selectRoundRobin(candidates)
	case StrategyLeastConnections:
		selected, reason = lb.selectLeastConnections(candidates, topology)
	case StrategyWeightedRandom:
		selected, reason = lb.selectWeightedRandom(candidates)
	case StrategyLatencyBased:
		selected, reason = lb.selectLatencyBased(candidates)
	default:
		selected, reason = lb.selectLeastConnections(candidates, topology)
	}

	if selected == nil {
		return nil, fmt.Errorf("failed to select relay worker")
	}

	// Get relay endpoints
	endpoints := lb.getRelayEndpoints(topology, selected.workerName, sourceSegments)

	selection := &RelaySelection{
		WorkerName: selected.workerName,
		Endpoints:  endpoints,
		Priority:   selected.priority,
		Reason:     reason,
	}

	lb.logger.Info("relay-selected", lager.Data{
		"worker":    selected.workerName,
		"strategy":  lb.strategy,
		"reason":    reason,
		"endpoints": len(endpoints),
	})

	// Update last used time
	lb.updateLastUsed(selected.workerName)

	return selection, nil
}

// findEligibleRelays finds relay workers that can bridge the segments
func (lb *LoadBalancer) findEligibleRelays(
	topology db.NetworkTopology,
	sourceSegments []string,
	destSegments []string,
) []*relayCandidate {
	var candidates []*relayCandidate

	for _, relay := range topology.RelayWorkers {
		if !relay.Enabled {
			continue
		}

		// Check capacity
		if relay.ActiveConnections >= relay.MaxConnections {
			lb.logger.Debug("relay-at-capacity", lager.Data{
				"relay": relay.WorkerName,
			})
			continue
		}

		// Check if relay can bridge segments
		for _, bridge := range topology.RelayNetworkBridges {
			if bridge.WorkerName != relay.WorkerName || !bridge.Enabled {
				continue
			}

			if lb.canBridge(bridge, sourceSegments, destSegments) {
				candidate := &relayCandidate{
					workerName: relay.WorkerName,
					priority:   bridge.Priority,
					capacity:   relay.MaxConnections - relay.ActiveConnections,
					bandwidth:  relay.BandwidthLimitMbps,
					bridge:     bridge,
				}
				candidates = append(candidates, candidate)
				break // Found a valid bridge for this relay
			}
		}
	}

	return candidates
}

// canBridge checks if a bridge can connect source to destination segments
func (lb *LoadBalancer) canBridge(
	bridge db.RelayNetworkBridge,
	sourceSegments []string,
	destSegments []string,
) bool {
	sourceMatch := false
	destMatch := false

	for _, ss := range sourceSegments {
		if bridge.FromSegment == ss {
			sourceMatch = true
			break
		}
	}

	for _, ds := range destSegments {
		if bridge.ToSegment == ds {
			destMatch = true
			break
		}
	}

	return sourceMatch && destMatch
}

// selectRoundRobin selects relay using round-robin
func (lb *LoadBalancer) selectRoundRobin(candidates []*relayCandidate) (*relayCandidate, string) {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	// Sort by last used time
	sort.Slice(candidates, func(i, j int) bool {
		statsI := lb.relayStats[candidates[i].workerName]
		statsJ := lb.relayStats[candidates[j].workerName]

		if statsI == nil && statsJ == nil {
			return false
		}
		if statsI == nil {
			return true // Prefer unused relays
		}
		if statsJ == nil {
			return false
		}
		return statsI.LastUsed.Before(statsJ.LastUsed)
	})

	return candidates[0], "round-robin selection"
}

// selectLeastConnections selects relay with least active connections
func (lb *LoadBalancer) selectLeastConnections(
	candidates []*relayCandidate,
	topology db.NetworkTopology,
) (*relayCandidate, string) {
	// Sort by active connections
	sort.Slice(candidates, func(i, j int) bool {
		// Find relay worker info
		var relayI, relayJ *db.RelayWorker
		for _, r := range topology.RelayWorkers {
			if r.WorkerName == candidates[i].workerName {
				relayI = &r
			}
			if r.WorkerName == candidates[j].workerName {
				relayJ = &r
			}
		}

		if relayI == nil || relayJ == nil {
			return false
		}

		// Prefer relay with fewer active connections
		if relayI.ActiveConnections != relayJ.ActiveConnections {
			return relayI.ActiveConnections < relayJ.ActiveConnections
		}

		// Tie-breaker: higher capacity
		return candidates[i].capacity > candidates[j].capacity
	})

	selected := candidates[0]
	reason := fmt.Sprintf("least connections (%d active)", 0)

	// Get actual active connections
	for _, r := range topology.RelayWorkers {
		if r.WorkerName == selected.workerName {
			reason = fmt.Sprintf("least connections (%d active)", r.ActiveConnections)
			break
		}
	}

	return selected, reason
}

// selectWeightedRandom selects relay using weighted random based on capacity
func (lb *LoadBalancer) selectWeightedRandom(candidates []*relayCandidate) (*relayCandidate, string) {
	// Calculate total weight (available capacity)
	totalWeight := 0
	for _, c := range candidates {
		totalWeight += c.capacity
	}

	if totalWeight == 0 {
		// All at capacity, pick randomly
		idx := rand.Intn(len(candidates))
		return candidates[idx], "random selection (all at capacity)"
	}

	// Weighted random selection
	r := rand.Intn(totalWeight)
	cumulative := 0

	for _, c := range candidates {
		cumulative += c.capacity
		if r < cumulative {
			return c, fmt.Sprintf("weighted random (capacity: %d)", c.capacity)
		}
	}

	return candidates[len(candidates)-1], "weighted random selection"
}

// selectLatencyBased selects relay based on historical latency
func (lb *LoadBalancer) selectLatencyBased(candidates []*relayCandidate) (*relayCandidate, string) {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	// Sort by average latency
	sort.Slice(candidates, func(i, j int) bool {
		statsI := lb.relayStats[candidates[i].workerName]
		statsJ := lb.relayStats[candidates[j].workerName]

		if statsI == nil && statsJ == nil {
			return false
		}
		if statsI == nil {
			return true // Prefer relays without stats (new)
		}
		if statsJ == nil {
			return false
		}

		// Prefer lower latency
		if statsI.AverageLatency != statsJ.AverageLatency {
			return statsI.AverageLatency < statsJ.AverageLatency
		}

		// Tie-breaker: higher success rate
		return statsI.SuccessRate > statsJ.SuccessRate
	})

	selected := candidates[0]
	reason := "lowest latency"

	if stats := lb.relayStats[selected.workerName]; stats != nil {
		reason = fmt.Sprintf("lowest latency (%.2fms)", stats.AverageLatency.Seconds()*1000)
	}

	return selected, reason
}

// getRelayEndpoints gets P2P endpoints for a relay worker
func (lb *LoadBalancer) getRelayEndpoints(
	topology db.NetworkTopology,
	relayWorker string,
	sourceSegments []string,
) []string {
	var endpoints []string

	for _, wn := range topology.WorkerNetworks {
		if wn.WorkerName != relayWorker {
			continue
		}

		// Check if this network matches source segments
		for _, ss := range sourceSegments {
			if wn.SegmentID == ss {
				endpoints = append(endpoints, wn.P2PEndpoint)
				break
			}
		}
	}

	return endpoints
}

// updateRelayStats updates statistics for relay workers
func (lb *LoadBalancer) updateRelayStats(topology db.NetworkTopology) {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	for _, relay := range topology.RelayWorkers {
		stats, exists := lb.relayStats[relay.WorkerName]
		if !exists {
			stats = &RelayStats{
				WorkerName: relay.WorkerName,
			}
			lb.relayStats[relay.WorkerName] = stats
		}

		stats.ActiveConnections = relay.ActiveConnections
		stats.TotalBytesRelayed = relay.TotalBytesRelayed

		// Calculate load percentage
		if relay.MaxConnections > 0 {
			stats.CurrentLoad = float64(relay.ActiveConnections) / float64(relay.MaxConnections)
		}
	}

	lb.lastUpdate = time.Now()
}

// updateLastUsed updates the last used time for a relay
func (lb *LoadBalancer) updateLastUsed(workerName string) {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	if stats, exists := lb.relayStats[workerName]; exists {
		stats.LastUsed = time.Now()
		stats.TotalConnections++
	} else {
		lb.relayStats[workerName] = &RelayStats{
			WorkerName:       workerName,
			LastUsed:         time.Now(),
			TotalConnections: 1,
		}
	}
}

// UpdateRelayMetrics updates metrics for a completed relay operation
func (lb *LoadBalancer) UpdateRelayMetrics(
	workerName string,
	success bool,
	latency time.Duration,
	bytesRelayed int64,
) {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	stats, exists := lb.relayStats[workerName]
	if !exists {
		stats = &RelayStats{
			WorkerName: workerName,
		}
		lb.relayStats[workerName] = stats
	}

	// Update success rate (simple moving average)
	if stats.TotalConnections > 0 {
		currentRate := stats.SuccessRate
		if success {
			stats.SuccessRate = (currentRate*float64(stats.TotalConnections-1) + 1) / float64(stats.TotalConnections)
		} else {
			stats.SuccessRate = (currentRate * float64(stats.TotalConnections-1)) / float64(stats.TotalConnections)
		}
	} else if success {
		stats.SuccessRate = 1.0
	}

	// Update average latency (simple moving average)
	if stats.TotalConnections > 1 {
		stats.AverageLatency = (stats.AverageLatency*time.Duration(stats.TotalConnections-1) + latency) / time.Duration(stats.TotalConnections)
	} else {
		stats.AverageLatency = latency
	}

	stats.TotalBytesRelayed += bytesRelayed
}

// GetStats returns current load balancer statistics
func (lb *LoadBalancer) GetStats() map[string]*RelayStats {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	// Create a copy of the stats map
	statsCopy := make(map[string]*RelayStats)
	for k, v := range lb.relayStats {
		statsCopy[k] = &RelayStats{
			WorkerName:        v.WorkerName,
			ActiveConnections: v.ActiveConnections,
			TotalConnections:  v.TotalConnections,
			TotalBytesRelayed: v.TotalBytesRelayed,
			AverageLatency:    v.AverageLatency,
			LastUsed:          v.LastUsed,
			SuccessRate:       v.SuccessRate,
			CurrentLoad:       v.CurrentLoad,
		}
	}

	return statsCopy
}