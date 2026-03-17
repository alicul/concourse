# PR #2: Multi-Network P2P Routing

## Summary

This PR implements intelligent P2P routing across multiple network segments, building on the network topology discovery from PR #1. It enables Concourse to automatically find the best P2P path between workers, even when they're on different networks.

## Problem Statement

With PR #1, workers can discover and report their network topology, but P2P streaming still can't:
- Select optimal routes across multiple networks
- Test connectivity before streaming
- Try multiple endpoints intelligently
- Track performance by network segment

## Solution

This PR introduces:
1. **Routing Engine** - Intelligent route selection based on network topology
2. **Connectivity Testing** - Pre-flight checks before streaming
3. **Multi-Endpoint Support** - Try multiple P2P endpoints in priority order
4. **Performance Metrics** - Track routing decisions and effectiveness

## Architecture

### Components

#### Routing Engine (`atc/worker/routing/engine.go`)
- **Route Selection Algorithm**:
  1. Check cache for recent routing decisions
  2. Find common network segments between workers
  3. Test connectivity to available endpoints
  4. Select route with lowest latency
  5. Cache decision for 5 minutes

- **Route Types**:
  - `Direct`: Workers on same network segment
  - `Relay`: Through relay worker (PR #3)
  - `ATC`: Traditional fallback

#### Connectivity Tester (`atc/worker/routing/connectivity_tester.go`)
- Tests TCP connectivity to P2P endpoints
- Measures latency for route optimization
- Configurable timeout (default 5s)
- Mock implementation for testing

#### Multi-Network Streamer (`atc/worker/multi_network_streamer.go`)
- Extends base streamer with routing intelligence
- Tries endpoints in priority order
- Tracks metrics by network segment
- Graceful fallback chain

### Data Flow

```
Stream Request
     ↓
Routing Engine
     ↓
Find Best Route ←→ Check Cache
     ↓                 ↑
Test Connectivity     Hit
     ↓                 ↓
Select Route      Return Cached
     ↓
Try P2P Endpoints (by priority)
     ↓
Success → Done
     ↓
Failure → Try Next
     ↓
All Failed → ATC Fallback
```

## Changes

### New Files
- `atc/worker/routing/engine.go` - Routing engine implementation
- `atc/worker/routing/connectivity_tester.go` - Connectivity testing
- `atc/worker/multi_network_streamer.go` - Multi-network aware streamer

### Modified Files
- `atc/metric/emit.go` - Added routing metric definitions
- `atc/metric/emitter/prometheus.go` - Prometheus routing metrics

## Metrics

### New Prometheus Metrics

```prometheus
# Route selection performance
concourse_network_p2p_route_selection_duration_seconds{source="worker1",dest="worker2"}

# Routing decisions
concourse_network_p2p_routes_by_method_total{method="direct|relay|atc"}

# Network segment usage
concourse_network_p2p_streaming_by_network_total{segment="private-10_0_0_0-16",status="success|failure"}

# Cache effectiveness
concourse_network_p2p_route_cache_hits_total
concourse_network_p2p_route_cache_misses_total
```

## Configuration

### Worker Configuration
```yaml
# No new configuration required
# Uses network topology from PR #1
```

### ATC Configuration
```yaml
# Enable multi-network P2P (optional)
CONCOURSE_P2P_MULTI_NETWORK_ENABLED: true

# Route cache TTL (optional)
CONCOURSE_P2P_ROUTE_CACHE_TTL: "5m"

# Connectivity test timeout (optional)
CONCOURSE_P2P_CONNECTIVITY_TIMEOUT: "5s"
```

## Testing

### Unit Tests
```go
// Test route selection
func TestRouteSelection(t *testing.T) {
    engine := NewEngine(...)
    route, err := engine.FindRoute(ctx, "worker1", "worker2")
    assert.Equal(t, RouteMethodDirect, route.Method)
}

// Test connectivity
func TestConnectivity(t *testing.T) {
    tester := NewConnectivityTester(...)
    ok, latency, err := tester.TestEndpoint(ctx, "http://10.0.0.5:7788")
    assert.True(t, ok)
}
```

### Integration Test
```bash
# Setup multi-network environment
docker-compose -f docker-compose-multinetwork.yml up -d

# Trigger streaming between workers
fly execute -c task.yml

# Verify routing metrics
curl http://localhost:9090/metrics | grep p2p_routes_by_method
```

## Performance

### Improvements
- **Route Caching**: 5-minute TTL reduces repeated calculations
- **Parallel Testing**: Connectivity tests can run concurrently
- **Priority Ordering**: Try most likely endpoints first
- **Early Termination**: Stop on first successful endpoint

### Overhead
- **Route Selection**: ~10-100ms (cached: <1ms)
- **Connectivity Test**: ~5-50ms per endpoint
- **Total Overhead**: <200ms worst case

## Benefits

1. **Automatic Optimization**: Always selects best available route
2. **Network Awareness**: Understands complex topologies
3. **Graceful Degradation**: Multiple fallback levels
4. **Performance Visibility**: Detailed metrics by network

## Migration

### Backward Compatibility
- Fully compatible with PR #1
- Falls back to basic P2P if routing unavailable
- No changes required to existing workers

### Upgrade Path
1. Deploy PR #1 (network topology)
2. Deploy PR #2 (routing)
3. Monitor metrics
4. Tune configuration if needed

## Example Scenarios

### Scenario 1: Workers on Same Network
```
worker1 (10.0.0.5) → worker2 (10.0.0.6)
Route: Direct via 10.0.0.0/24
Latency: 2ms
Method: P2P Direct
```

### Scenario 2: Workers on Different Networks
```
worker1 (10.0.0.5) → worker3 (192.168.1.10)
Route: No common segment
Method: ATC Fallback
Note: Will use relay in PR #3
```

### Scenario 3: Multiple Network Options
```
worker1 (10.0.0.5, 172.16.0.5) → worker2 (10.0.0.6, 172.16.0.6)
Routes tested:
1. 10.0.0.0/24 - 2ms ✓ Selected
2. 172.16.0.0/16 - 5ms
Method: P2P Direct via optimal network
```

## Troubleshooting

### No Routes Found
```bash
# Check network topology
curl http://localhost:8080/api/v1/network-topology

# Verify connectivity
curl http://localhost:8080/api/v1/connectivity-matrix
```

### High Route Selection Time
```bash
# Check cache effectiveness
curl http://localhost:9090/metrics | grep route_cache

# May indicate network issues
```

### All P2P Failing
```bash
# Check worker networks
fly workers

# Review streaming metrics
curl http://localhost:9090/metrics | grep streaming_by_network
```

## Future Work (PR #3)

- Relay worker routing
- Multi-hop path finding
- Load balancing across routes
- Bandwidth-aware routing

## Commits

1. Add P2P routing engine for multi-network support
2. Add multi-network aware streamer
3. Add comprehensive P2P routing metrics

## Review Checklist

- [ ] Routing engine finds optimal routes
- [ ] Connectivity tests work correctly
- [ ] Metrics are properly emitted
- [ ] Cache improves performance
- [ ] Fallback chain works
- [ ] No regression in basic P2P

---

This PR enables intelligent P2P routing across multiple network segments, significantly improving P2P streaming effectiveness in complex network topologies.