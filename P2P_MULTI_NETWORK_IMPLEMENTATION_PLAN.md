# P2P Volume Streaming for Multi-Network Environments - Implementation Plan

## Executive Summary

This document outlines the implementation plan for enhancing Concourse's P2P volume streaming to support multi-network environments. Currently, P2P streaming assumes all workers are on the same network. This enhancement will enable P2P streaming across different network segments, with automatic network topology discovery and intelligent routing.

## Current State Analysis

### Existing P2P Implementation
- **Basic P2P streaming exists** but assumes single network topology
- Workers expose P2P endpoints via `GetP2pUrl` API
- Configuration uses `p2p-interface-name-pattern` to select network interface
- Fallback to ATC-mediated streaming on P2P failure
- No network segment awareness or routing intelligence

### Key Components
1. **Baggageclaim API**:
   - `/p2p-url` endpoint returns worker's P2P URL
   - `/stream-p2p-out` endpoint initiates direct streaming
   - `/stream-in` endpoint receives volume data

2. **ATC Streamer**:
   - `Streamer.p2pStream()` orchestrates P2P transfers
   - Falls back to ATC-mediated streaming on failure
   - Configuration via `--enable-p2p-volume-streaming` flag

3. **Worker Configuration**:
   - `--p2p-interface-name-pattern`: Interface selection regex
   - `--p2p-interface-family`: IPv4 or IPv6 selection
   - Single interface/IP assumption

## Proposed Architecture

### 1. Network Topology Discovery

**Goal**: Workers automatically discover their network segments and connectivity

**Components**:
- **Network Segment Identifier**: Each worker identifies its network segment(s)
- **Connectivity Matrix**: Track which workers can reach each other
- **Network Registry**: Central registry in ATC database

**Implementation**:
```go
type NetworkSegment struct {
    ID          string
    CIDR        string
    Gateway     string
    Type        string // "private", "public", "overlay"
    Priority    int    // For routing preferences
}

type WorkerNetworkInfo struct {
    WorkerName     string
    Segments       []NetworkSegment
    P2PEndpoints   map[string]string // segment_id -> endpoint URL
    LastUpdated    time.Time
}
```

### 2. Multi-Network P2P Protocol

**Enhanced P2P URL Discovery**:
```go
// Current: Single P2P URL
GET /p2p-url -> "http://10.0.1.5:7788"

// Enhanced: Multiple P2P URLs with network metadata
GET /p2p-urls -> {
  "endpoints": [
    {
      "url": "http://10.0.1.5:7788",
      "network_segment": "network-1",
      "priority": 1,
      "bandwidth": "1gbps"
    },
    {
      "url": "http://192.168.1.10:7788",
      "network_segment": "network-2",
      "priority": 2,
      "bandwidth": "100mbps"
    }
  ],
  "connectivity_test_port": 7789
}
```

### 3. Intelligent Routing

**Route Selection Algorithm**:
1. Query source and destination worker network info
2. Find common network segments
3. Select optimal route based on:
   - Network segment priority
   - Available bandwidth
   - Historical performance metrics
4. Test connectivity before streaming
5. Fallback cascade: Direct P2P → Relay P2P → ATC-mediated

**Connectivity Testing**:
```go
func TestP2PConnectivity(ctx context.Context, sourceWorker, destWorker Worker) (*P2PRoute, error) {
    // 1. Get network info from both workers
    srcNetworks := sourceWorker.GetP2PNetworks()
    dstNetworks := destWorker.GetP2PNetworks()

    // 2. Find optimal route
    route := FindOptimalRoute(srcNetworks, dstNetworks)

    // 3. Test connectivity
    if err := route.TestConnectivity(ctx); err != nil {
        return nil, err
    }

    return route, nil
}
```

### 4. Relay Worker Support

For workers in isolated networks, introduce relay workers:

```go
type RelayWorker struct {
    Worker
    RelayCapable   bool
    NetworkBridges []NetworkBridge // Networks it can bridge
}

type NetworkBridge struct {
    FromSegment string
    ToSegment   string
    Bandwidth   string
}
```

## Implementation Phases

### Phase 1: Network Discovery (Week 1-2)
1. **Database Schema Updates**
   - Add `worker_networks` table
   - Add `network_segments` table
   - Add `worker_connectivity` table

2. **Worker Network Detection**
   - Implement multi-interface detection
   - Network segment identification
   - Periodic network info updates to ATC

3. **API Enhancements**
   - Extend `/p2p-url` to `/p2p-urls` (backward compatible)
   - Add network metadata endpoints
   - Connectivity test endpoints

**Deliverables**:
- [ ] Database migrations
- [ ] Network detection library
- [ ] API extensions
- [ ] Unit tests

### Phase 2: Multi-Network Routing (Week 3-4)
1. **Routing Engine**
   - Route discovery algorithm
   - Priority-based selection
   - Performance metrics collection

2. **Connectivity Testing**
   - Pre-flight connectivity checks
   - Bandwidth estimation
   - Latency measurements

3. **Enhanced Streamer**
   - Multi-route P2P streaming
   - Intelligent fallback logic
   - Performance monitoring

**Deliverables**:
- [ ] Routing engine implementation
- [ ] Connectivity test suite
- [ ] Enhanced streamer with multi-network support
- [ ] Integration tests

### Phase 3: Relay Workers (Week 5-6)
1. **Relay Worker Implementation**
   - Relay capability detection
   - Stream proxying logic
   - Performance optimization

2. **Relay Routing**
   - Multi-hop routing support
   - Relay worker selection
   - Load balancing

**Deliverables**:
- [ ] Relay worker implementation
- [ ] Multi-hop routing
- [ ] Load balancing
- [ ] End-to-end tests

### Phase 4: Configuration & Operations (Week 7)
1. **Configuration Management**
   - Network segment configuration
   - Routing preferences
   - Relay worker configuration

2. **Monitoring & Metrics**
   - Network topology visualization
   - P2P streaming metrics
   - Performance dashboards

3. **Documentation**
   - Administrator guide
   - Network configuration guide
   - Troubleshooting guide

**Deliverables**:
- [ ] Configuration interfaces
- [ ] Monitoring dashboards
- [ ] Documentation
- [ ] Performance benchmarks

## Configuration Changes

### Worker Configuration
```yaml
# Current
CONCOURSE_BAGGAGECLAIM_P2P_INTERFACE_PATTERN: "eth0"

# Enhanced
CONCOURSE_BAGGAGECLAIM_P2P_INTERFACES: |
  - pattern: "eth0"
    network_segment: "private-1"
    priority: 1
  - pattern: "eth1"
    network_segment: "public-1"
    priority: 2

CONCOURSE_BAGGAGECLAIM_P2P_RELAY_ENABLED: true
CONCOURSE_BAGGAGECLAIM_P2P_NETWORK_DETECTION: auto
```

### ATC Configuration
```yaml
# Current
CONCOURSE_ENABLE_P2P_VOLUME_STREAMING: true

# Enhanced
CONCOURSE_ENABLE_P2P_VOLUME_STREAMING: true
CONCOURSE_P2P_MULTI_NETWORK_ENABLED: true
CONCOURSE_P2P_RELAY_WORKERS_ENABLED: true
CONCOURSE_P2P_NETWORK_TOPOLOGY_REFRESH: 5m
```

## Testing Strategy

### Unit Tests
- Network detection logic
- Routing algorithm
- Connectivity testing
- Relay logic

### Integration Tests
1. **Single Network** (baseline)
   - Verify existing P2P works
   - Performance benchmarks

2. **Dual Network**
   - Workers on different networks
   - Verify routing selection
   - Fallback testing

3. **Multi-Network with Relay**
   - Three network segments
   - Relay worker bridging
   - Performance validation

4. **Failure Scenarios**
   - Network partition
   - Relay worker failure
   - Route changes during streaming

### Docker Compose Test Environment

```yaml
# docker-compose-multi-network.yml
version: '3.8'

networks:
  network1:
    driver: bridge
    ipam:
      config:
        - subnet: 172.20.0.0/16

  network2:
    driver: bridge
    ipam:
      config:
        - subnet: 172.21.0.0/16

  network3:
    driver: bridge
    ipam:
      config:
        - subnet: 172.22.0.0/16

services:
  web:
    # ... existing config ...
    networks:
      - network1
      - network2
      - network3

  worker1:
    # ... base worker config ...
    networks:
      - network1
    environment:
      CONCOURSE_NAME: worker1
      CONCOURSE_BAGGAGECLAIM_P2P_INTERFACES: |
        - pattern: "eth0"
          network_segment: "network1"

  worker2:
    # ... base worker config ...
    networks:
      - network2
    environment:
      CONCOURSE_NAME: worker2
      CONCOURSE_BAGGAGECLAIM_P2P_INTERFACES: |
        - pattern: "eth0"
          network_segment: "network2"

  worker-relay:
    # ... base worker config ...
    networks:
      - network1
      - network2
    environment:
      CONCOURSE_NAME: worker-relay
      CONCOURSE_BAGGAGECLAIM_P2P_RELAY_ENABLED: true
      CONCOURSE_BAGGAGECLAIM_P2P_INTERFACES: |
        - pattern: "eth0"
          network_segment: "network1"
        - pattern: "eth1"
          network_segment: "network2"

  worker3:
    # ... base worker config ...
    networks:
      - network3
    environment:
      CONCOURSE_NAME: worker3
      # Isolated network - requires ATC-mediated streaming
```

### Integration Test Script
```bash
#!/bin/bash
# test-p2p-multi-network.sh

# 1. Start multi-network environment
docker-compose -f docker-compose-multi-network.yml up -d

# 2. Wait for workers to register
sleep 30

# 3. Run test pipeline that forces volume streaming between workers
fly -t dev execute -c test-tasks/p2p-streaming-test.yml

# 4. Verify P2P streaming occurred (check metrics)
curl http://localhost:9090/metrics | grep p2p_streams_

# 5. Test failover scenarios
docker-compose -f docker-compose-multi-network.yml stop worker-relay
fly -t dev execute -c test-tasks/p2p-streaming-test.yml

# 6. Validate results
```

## Commit Strategy

### Commit Batches

**Batch 1: Database & Core Types**
- Add network topology database schema
- Define core networking types
- Add worker network info storage

**Batch 2: Network Detection**
- Implement multi-interface detection
- Add network segment identification
- Worker network info reporting

**Batch 3: API Extensions**
- Extend P2P URL endpoints
- Add connectivity test endpoints
- Backward compatibility layer

**Batch 4: Routing Engine**
- Implement route discovery
- Add priority-based selection
- Performance metrics collection

**Batch 5: Enhanced Streaming**
- Multi-route P2P streaming
- Connectivity pre-flight checks
- Intelligent fallback logic

**Batch 6: Relay Workers**
- Relay capability detection
- Stream proxying implementation
- Multi-hop routing

**Batch 7: Configuration**
- Configuration interfaces
- Environment variable parsing
- Validation logic

**Batch 8: Testing**
- Unit tests for all components
- Integration test suite
- Docker compose test environment

**Batch 9: Documentation**
- API documentation
- Administrator guide
- Migration guide

## Performance Considerations

### Metrics to Track
- P2P streaming success rate by network topology
- Average streaming time by route type
- Fallback frequency
- Network bandwidth utilization
- Relay worker load

### Optimization Opportunities
1. **Connection Pooling**: Reuse P2P connections
2. **Route Caching**: Cache successful routes
3. **Predictive Routing**: Use historical data for route selection
4. **Compression Optimization**: Adjust based on network bandwidth

## Security Considerations

1. **Network Isolation**: Ensure P2P doesn't breach network boundaries
2. **Authentication**: Validate worker identity in P2P connections
3. **Encryption**: Optional TLS for P2P streams
4. **Rate Limiting**: Prevent P2P abuse
5. **Audit Logging**: Track all P2P streaming operations

## Rollout Plan

1. **Feature Flag**: Deploy behind feature flag
2. **Canary Testing**: Test with subset of workers
3. **Gradual Rollout**: Increase P2P usage gradually
4. **Monitoring**: Track performance and errors
5. **Rollback Plan**: Quick disable via feature flag

## Success Criteria

1. **Functional**:
   - P2P streaming works across network boundaries
   - Automatic fallback to ATC-mediated streaming
   - No regression in single-network scenarios

2. **Performance**:
   - 50% reduction in cross-network streaming time vs ATC-mediated
   - 90% P2P success rate in multi-network environments
   - < 1 second connectivity test overhead

3. **Operational**:
   - Clear network topology visibility
   - Easy troubleshooting of P2P issues
   - Minimal configuration complexity

## Open Questions

1. **Network Detection**: Should we support manual network configuration or rely on auto-detection?
2. **Relay Workers**: Should any worker be relay-capable or require special configuration?
3. **Security**: Should P2P streams be encrypted by default?
4. **Performance**: What's the acceptable overhead for connectivity testing?
5. **Compatibility**: How to handle mixed worker versions during rollout?

## Next Steps

1. Review and refine this plan with the team
2. Create detailed technical design documents
3. Set up development environment with multi-network topology
4. Begin Phase 1 implementation
5. Regular progress reviews and plan adjustments