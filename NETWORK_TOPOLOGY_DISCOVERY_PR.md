# PR #1: Network Topology Discovery for Multi-Network P2P

## Summary

This PR implements network topology discovery and management, laying the foundation for multi-network P2P volume streaming in Concourse. It enables workers to detect and report their network configuration, allowing the ATC to maintain a complete view of the network topology.

## Problem Statement

Current P2P streaming assumes all workers are on the same network. This limitation prevents P2P streaming in:
- Multi-cloud deployments
- Hybrid cloud/on-premise setups
- Network-segmented environments
- Geographically distributed clusters

## Solution

This PR introduces:
1. **Network detection** - Workers auto-discover their network segments
2. **Topology storage** - Database schema for network topology
3. **API endpoints** - REST APIs for topology management
4. **Metrics** - Monitoring for network discovery and connectivity

## Changes

### Database Schema (Migration 1773606548)

New tables:
- `network_segments` - Network segment definitions
- `worker_networks` - Worker-to-segment mappings with P2P endpoints
- `worker_connectivity` - Connectivity test results between workers
- `relay_workers` - Relay-capable workers (for future PR)
- `relay_network_bridges` - Network bridges for relay workers

### Worker Components

**Network Detector** (`worker/network/detector.go`):
- Discovers network interfaces matching configured patterns
- Identifies network types (private/public/overlay)
- Generates P2P endpoints for each segment
- Tests connectivity to other workers

**Network Reporter** (`worker/network/reporter.go`):
- Periodically reports network topology to ATC
- Sends connectivity test results
- Configurable reporting interval

### ATC Components

**Network Topology Factory** (`atc/db/network_topology.go`):
- Database access layer for network topology
- CRUD operations for network segments
- Worker network management
- Connectivity matrix operations

**API Handler** (`atc/api/network_topology_handler.go`):
- REST endpoints for topology management
- Multi-network P2P URL discovery
- Connectivity reporting

### API Endpoints

```http
GET  /api/v1/network-topology              # Complete topology
GET  /api/v1/workers/:name/networks        # Worker's networks
PUT  /api/v1/workers/:name/networks        # Update networks
GET  /api/v1/workers/:name/p2p-urls        # P2P URLs (multi-network)
PUT  /api/v1/workers/:name/connectivity    # Report connectivity
GET  /api/v1/connectivity-matrix           # Full connectivity matrix
```

### Metrics

New Prometheus metrics:
- `concourse_network_topology_changes_total`
- `concourse_network_segments_discovered{worker}`
- `concourse_network_connectivity_tests_total`
- `concourse_network_p2p_connectivity_test_success_total`
- `concourse_network_p2p_connectivity_test_failure_total`

## Configuration

### Worker Configuration

```yaml
# Network detection configuration
CONCOURSE_BAGGAGECLAIM_P2P_INTERFACE_PATTERN: "eth.*"
CONCOURSE_BAGGAGECLAIM_P2P_NETWORK_DETECTION: "auto"
CONCOURSE_NETWORK_TOPOLOGY_REPORT_INTERVAL: "5m"
```

### ATC Configuration

```yaml
# Enable multi-network support (future PR will use this)
CONCOURSE_P2P_MULTI_NETWORK_ENABLED: false  # Will be true in PR #2
```

## Benefits

### Immediate Benefits
1. **Network visibility** - Complete view of worker network topology
2. **Connectivity insights** - Know which workers can reach each other
3. **Foundation for routing** - Data needed for intelligent P2P routing

### Enables Future Features (PR #2-4)
1. Multi-network P2P routing
2. Relay worker support
3. Network-aware job scheduling
4. Cross-region volume streaming

## Testing

### Unit Tests
```bash
go test ./atc/db -run TestNetworkTopology
go test ./worker/network/...
```

### Integration Test
```bash
# Start workers with network detection
docker-compose -f docker-compose-multinetwork.yml up -d

# Check network topology
curl http://localhost:8080/api/v1/network-topology

# Verify worker networks
curl http://localhost:8080/api/v1/workers/worker1/networks
```

### Manual Testing

1. **Network Detection**:
   ```bash
   # Worker logs should show:
   # "detected-network-segment" segment_id="private-10_0_1_0-24"
   ```

2. **Topology Reporting**:
   ```bash
   # Check metrics
   curl http://localhost:9090/metrics | grep network_segments_discovered
   ```

3. **Connectivity Testing**:
   ```bash
   # Worker logs show:
   # "connectivity-test-result" target="worker2" can_connect=true
   ```

## Migration Guide

### For Operators

1. **No breaking changes** - Existing P2P continues to work
2. **Optional configuration** - Network detection is automatic
3. **Gradual adoption** - Can be enabled per worker

### For Developers

1. **Use NetworkTopologyFactory** for network queries
2. **Update P2P logic** to use multi-network URLs (PR #2)
3. **Add network awareness** to scheduling decisions

## Performance Impact

- **Minimal overhead**: Network detection runs once at startup
- **Low frequency reporting**: Default 5-minute intervals
- **Efficient queries**: Indexed database lookups
- **No impact on streaming**: Detection is out-of-band

## Security Considerations

1. **Network isolation maintained** - No automatic cross-network routing
2. **Explicit configuration** - Relay workers require explicit setup
3. **No credential exposure** - Only network metadata is shared

## Commits

1. Add database schema for network topology
2. Add network topology data models and factory
3. Add network detection and reporting services
4. Add API endpoints for network topology
5. Add network topology metrics

## Future Work (Separate PRs)

- **PR #2**: Multi-network P2P routing using topology data
- **PR #3**: Relay worker implementation for bridging networks
- **PR #4**: Enhanced documentation and dashboards

## Review Checklist

- [ ] Database migrations run successfully
- [ ] Workers detect and report networks
- [ ] API endpoints return topology data
- [ ] Metrics appear in Prometheus
- [ ] No regression in existing P2P
- [ ] Documentation is clear

---

This PR provides the foundation for multi-network P2P streaming by implementing comprehensive network topology discovery and management. It has no impact on existing P2P functionality while enabling future enhancements.