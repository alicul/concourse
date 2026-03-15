# P2P Multi-Network Volume Streaming - Implementation Roadmap

## Overview
This document outlines the phased implementation plan for extending Concourse's P2P volume streaming to support multi-network environments, with comprehensive monitoring from day one.

## Current Status

### ✅ Completed (PR #0)
- **Comprehensive Volume Streaming Metrics**
  - Fixed missing Prometheus metrics
  - Added detailed success/failure counters
  - Implemented duration and size histograms
  - Created Grafana dashboard for visualization
  - **Benefit**: Immediate visibility into existing P2P performance

### 📊 Baseline Metrics Now Available
With PR #0 deployed, we can now measure:
- P2P success rate: `streaming_p2p_success / (streaming_p2p_success + streaming_p2p_failure)`
- Fallback rate: `volumes_streamed_via_fallback / volumes_streamed`
- Performance delta: `p50(streaming_duration{method="p2p"}) vs p50(streaming_duration{method="atc"})`
- Volume size distribution patterns
- Worker-level streaming load

## Implementation Phases

### Phase 1: Network Topology Discovery (Week 1-2)
**PR #1: Network Discovery & Storage**

#### Database Schema
```sql
-- Network segments table
CREATE TABLE network_segments (
    id TEXT PRIMARY KEY,
    cidr TEXT NOT NULL,
    gateway TEXT,
    type TEXT CHECK (type IN ('private', 'public', 'overlay')),
    priority INTEGER DEFAULT 0
);

-- Worker networks mapping
CREATE TABLE worker_networks (
    worker_name TEXT NOT NULL,
    segment_id TEXT NOT NULL,
    p2p_endpoint TEXT NOT NULL,
    last_updated TIMESTAMP DEFAULT NOW(),
    PRIMARY KEY (worker_name, segment_id),
    FOREIGN KEY (segment_id) REFERENCES network_segments(id)
);

-- Connectivity matrix
CREATE TABLE worker_connectivity (
    source_worker TEXT NOT NULL,
    dest_worker TEXT NOT NULL,
    can_connect BOOLEAN NOT NULL,
    latency_ms INTEGER,
    bandwidth_mbps INTEGER,
    last_tested TIMESTAMP DEFAULT NOW(),
    PRIMARY KEY (source_worker, dest_worker)
);
```

#### Key Components
1. **Network Detection Service**
   - Auto-detect network interfaces and segments
   - Identify network types (private/public/overlay)
   - Report to ATC periodically

2. **API Extensions**
   ```go
   GET /api/v1/workers/:name/networks
   GET /api/v1/network-topology
   GET /api/v1/workers/:name/connectivity
   ```

3. **New Metrics**
   - `network_topology_changes_total`
   - `worker_network_segments{worker}`
   - `network_discovery_duration_seconds`

#### Deliverables
- [ ] Database migrations
- [ ] Network detection library
- [ ] API endpoints
- [ ] Unit tests
- [ ] Metrics integration

### Phase 2: Multi-Network P2P Protocol (Week 3-4)
**PR #2: Multi-Network Routing**

#### Enhanced P2P Protocol
```go
// Current: Single P2P URL
GET /p2p-url -> "http://10.0.1.5:7788"

// Enhanced: Multiple P2P URLs with metadata
GET /p2p-urls -> {
  "endpoints": [
    {
      "url": "http://10.0.1.5:7788",
      "network_segment": "private-1",
      "priority": 1,
      "bandwidth": "1gbps"
    },
    {
      "url": "http://192.168.1.10:7788",
      "network_segment": "public-1",
      "priority": 2,
      "bandwidth": "100mbps"
    }
  ],
  "connectivity_test_port": 7789
}
```

#### Routing Engine
```go
type RouteSelector struct {
    topology NetworkTopology
    metrics  RouteMetrics
}

func (r *RouteSelector) SelectRoute(src, dst Worker) (*P2PRoute, error) {
    // 1. Find common network segments
    // 2. Test connectivity
    // 3. Select optimal path based on:
    //    - Network priority
    //    - Historical performance
    //    - Current load
}
```

#### New Metrics
- `p2p_route_selection_duration_seconds`
- `p2p_routes_by_network{segment}`
- `p2p_connectivity_tests_total{status}`
- `p2p_streaming_hops` (for relay paths)

#### Deliverables
- [ ] Multi-endpoint P2P API
- [ ] Routing engine
- [ ] Connectivity testing
- [ ] Enhanced streamer
- [ ] Integration tests

### Phase 3: Relay Worker Support (Week 5-6)
**PR #3: Relay Workers**

#### Relay Worker Configuration
```yaml
CONCOURSE_BAGGAGECLAIM_P2P_RELAY_ENABLED: true
CONCOURSE_BAGGAGECLAIM_P2P_RELAY_MAX_CONNECTIONS: 10
CONCOURSE_BAGGAGECLAIM_P2P_RELAY_BANDWIDTH_LIMIT: "1gbps"
```

#### Relay Protocol
```go
type RelayRequest struct {
    SourceWorker string
    DestWorker   string
    VolumeHandle string
    Compression  string
}

type RelayWorker interface {
    CanRelay(src, dst NetworkSegment) bool
    RelayStream(ctx context.Context, req RelayRequest) error
}
```

#### New Metrics
- `relay_streams_total{source_segment,dest_segment}`
- `relay_stream_duration_seconds`
- `relay_bandwidth_bytes_per_second`
- `relay_worker_load{worker}`

#### Deliverables
- [ ] Relay worker implementation
- [ ] Multi-hop routing
- [ ] Load balancing
- [ ] End-to-end tests

### Phase 4: Operations & Documentation (Week 7)
**PR #4: Operations Support**

#### Enhanced Dashboards
- Network topology visualization
- Per-segment streaming metrics
- Relay worker performance
- Cross-network latency heatmap

#### Documentation
- Administrator guide
- Network configuration best practices
- Troubleshooting guide
- Performance tuning guide

#### Alerts
```yaml
- name: P2P Streaming Alerts
  rules:
  - alert: HighP2PFailureRate
    expr: rate(streaming_p2p_failure_total[5m]) > 0.1
  - alert: HighFallbackRate
    expr: rate(volumes_streamed_via_fallback[5m]) / rate(volumes_streamed[5m]) > 0.5
  - alert: RelayWorkerOverloaded
    expr: relay_worker_load > 0.8
```

## Success Metrics

### Phase 0 (Current Metrics)
- Baseline P2P success rate established
- Current fallback rate documented
- Performance characteristics understood

### Phase 1 Targets
- 100% of workers report network topology
- < 1s network discovery time
- Zero impact on existing P2P streaming

### Phase 2 Targets
- 90% P2P success rate in multi-network environments
- < 2s routing decision time
- 50% reduction in cross-network streaming time vs ATC

### Phase 3 Targets
- 95% success rate with relay workers
- < 20% overhead for relay streaming
- Support for 3+ network hops

### Phase 4 Targets
- Complete operational visibility
- < 5 min MTTR for P2P issues
- 90% of issues self-diagnosable via dashboards

## Risk Mitigation

### Feature Flags
```go
CONCOURSE_P2P_MULTI_NETWORK_ENABLED=false  // Phase 1
CONCOURSE_P2P_RELAY_ENABLED=false         // Phase 3
CONCOURSE_P2P_NETWORK_DISCOVERY=manual    // Fallback option
```

### Rollback Strategy
1. All features behind flags
2. Backward compatible API
3. Graceful degradation to single-network P2P
4. Ultimate fallback to ATC streaming

### Testing Strategy
1. **Unit tests**: Each component in isolation
2. **Integration tests**: Docker Compose multi-network
3. **Performance tests**: Benchmark vs baseline
4. **Chaos tests**: Network partition scenarios
5. **Production canary**: Progressive rollout

## Timeline

| Week | Phase | Deliverable |
|------|-------|-------------|
| 0 | Metrics | ✅ Comprehensive monitoring (DONE) |
| 1-2 | Discovery | Network topology detection |
| 3-4 | Routing | Multi-network P2P protocol |
| 5-6 | Relay | Relay worker implementation |
| 7 | Ops | Documentation & dashboards |
| 8 | Testing | End-to-end validation |
| 9 | Rollout | Production deployment |

## Open Questions

1. **Network Detection**: Auto-detect vs manual configuration?
2. **Relay Selection**: Dedicated relay workers or any worker can relay?
3. **Security**: Should P2P streams be encrypted across network boundaries?
4. **Performance**: Acceptable overhead for connectivity testing?
5. **Compatibility**: Support for mixed versions during rollout?

## Next Immediate Steps

1. ✅ Deploy PR #0 (metrics) to production
2. ⏳ Collect baseline metrics for 1-2 weeks
3. 📊 Analyze P2P usage patterns and failure modes
4. 🎯 Refine multi-network requirements based on data
5. 🚀 Begin Phase 1 implementation

---

## Contact

For questions or discussions about this roadmap:
- GitHub: [concourse/concourse#issues](https://github.com/concourse/concourse/issues)
- Slack: #concourse-dev

---

*Last Updated: March 2026*
*Status: PR #0 Complete, Phase 1 Planning*