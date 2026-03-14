# P2P Multi-Network Volume Streaming for Concourse

## Overview

This implementation extends Concourse's P2P volume streaming to support multi-network environments where workers may be deployed across different network segments. The solution provides intelligent routing, relay worker support, and automatic fallback to ensure reliable volume streaming in complex network topologies.

## Features

### 1. **Multi-Network P2P Streaming**
- Workers can stream volumes directly when on the same network segment
- Automatic network topology discovery and management
- Support for multiple network interfaces per worker

### 2. **Relay Workers**
- Special workers that bridge network segments
- Automatic relay discovery and selection
- Efficient proxying of volume streams between isolated networks

### 3. **Intelligent Routing**
- Automatic route selection based on network topology
- Priority-based endpoint selection
- Performance-aware routing decisions

### 4. **Comprehensive Metrics**
- Detailed Prometheus metrics with labels for:
  - Source and destination workers
  - Step type, name, pipeline, and job information
  - Streaming type (direct, relay, ATC-mediated)
  - Network segments
  - Success/failure tracking
  - Bandwidth and latency measurements

### 5. **Automatic Fallback**
- Cascade: Direct P2P → Relay P2P → ATC-mediated
- Transparent failover on connectivity issues
- Performance monitoring for optimal path selection

## Architecture

### Components Added

1. **Database Schema** (`atc/db/migration/migrations/1001_add_network_topology.up.sql`)
   - `network_segments`: Network segment definitions
   - `worker_networks`: Worker network configurations
   - `worker_connectivity`: Connectivity matrix
   - `p2p_streaming_metrics`: Streaming performance data

2. **Network Detection** (`worker/baggageclaim/network/detector.go`)
   - Multi-interface detection
   - Network segment identification
   - Connectivity testing

3. **P2P Router** (`atc/worker/p2p_router.go`)
   - Route discovery algorithm
   - Network topology management
   - Performance-based routing

4. **Multi-Network Streamer** (`atc/worker/multinetwork_streamer.go`)
   - Enhanced streaming with routing
   - Metrics collection
   - Fallback logic

5. **Relay Support** (`worker/baggageclaim/volume/relay_streamer.go`)
   - Stream proxying
   - Multi-hop routing
   - Bandwidth management

## Configuration

### Web/ATC Configuration

```yaml
# Enable P2P multi-network streaming
CONCOURSE_ENABLE_P2P_VOLUME_STREAMING: "true"
CONCOURSE_P2P_MULTI_NETWORK_ENABLED: "true"
CONCOURSE_P2P_RELAY_WORKERS_ENABLED: "true"
CONCOURSE_P2P_NETWORK_TOPOLOGY_REFRESH: "30s"
CONCOURSE_P2P_VOLUME_STREAMING_TIMEOUT: "5m"
```

### Worker Configuration

```yaml
# Regular worker with single network
CONCOURSE_BAGGAGECLAIM_P2P_INTERFACES: |
  - pattern: "eth0"
    network_segment: "segment1"
    priority: 1

# Relay worker with multiple networks
CONCOURSE_BAGGAGECLAIM_P2P_RELAY_ENABLED: "true"
CONCOURSE_BAGGAGECLAIM_P2P_INTERFACES: |
  - pattern: "eth0"
    network_segment: "segment1"
    priority: 1
  - pattern: "eth1"
    network_segment: "segment2"
    priority: 2

# Network detection mode
CONCOURSE_BAGGAGECLAIM_P2P_NETWORK_DETECTION: "auto"  # or "manual"
```

## Metrics

### Key Metrics with Labels

1. **`concourse_volumes_streamed_count`**
   - Labels: source_worker, destination_worker, streaming_type, step_type, step_name, pipeline_name, job_name, team_name, network_segment, success

2. **`concourse_volumes_streamed_bytes`**
   - Labels: source_worker, destination_worker, streaming_type, step_type, step_name, pipeline_name, job_name, team_name, network_segment

3. **`concourse_volumes_streaming_duration_seconds`**
   - Labels: source_worker, destination_worker, streaming_type, step_type, pipeline_name, job_name

4. **`concourse_p2p_streaming_success_total`**
   - Labels: source_worker, destination_worker, streaming_type, network_segment

5. **`concourse_p2p_relay_streams_total`**
   - Labels: source_worker, destination_worker, relay_worker, source_segment, destination_segment

## Testing

### Integration Test Environment

The `docker-compose-p2p-multinetwork.yml` creates a test environment with:
- 3 network segments
- 5 workers (including 1 relay worker)
- Prometheus and Grafana for monitoring

### Running Tests

```bash
# Start the test environment
docker-compose -f docker-compose-p2p-multinetwork.yml up -d

# Run integration tests
./test/p2p-multinetwork/run-tests.sh

# View metrics
curl http://localhost:9090/metrics | grep concourse_volumes_streamed

# Access Grafana dashboard
open http://localhost:3000  # admin/admin
```

### Test Scenarios

1. **Direct P2P** - Workers on same network segment
2. **Relay P2P** - Workers on different segments with relay
3. **ATC Fallback** - Isolated worker without P2P access
4. **Performance** - Compare P2P vs ATC-mediated streaming
5. **Failure Handling** - Network partition and relay failure

## API Endpoints

### New Endpoints

1. **GET /p2p-urls** - Get all P2P endpoints for a worker
   ```json
   {
     "endpoints": [
       {
         "url": "http://172.20.0.5:7788",
         "network_segment": "segment1",
         "priority": 1,
         "bandwidth": "1000Mbps"
       }
     ],
     "connectivity_test_port": 7789,
     "is_relay_capable": false
   }
   ```

2. **POST /test-connectivity** - Test connectivity to another worker
   ```json
   Request: {"target_url": "http://worker2:7788"}
   Response: {
     "success": true,
     "latency": 5000000,
     "network_path": "172.20.0.5:45678 -> 172.20.0.6:7788"
   }
   ```

3. **GET /network-info** - Get detailed network information
   ```json
   {
     "networks": [
       {
         "interface_name": "eth0",
         "ip_address": "172.20.0.5",
         "cidr": "172.20.0.0/16",
         "network_segment": "segment1",
         "priority": 1
       }
     ],
     "is_relay_capable": false
   }
   ```

## Monitoring Dashboard

The Grafana dashboard (`monitoring/grafana/dashboards/p2p-streaming.json`) provides:
- P2P streaming success rates
- Volume streaming by type (pie chart)
- Streaming duration heatmap
- Bytes streamed over time
- Relay worker usage
- Network topology changes
- Active P2P streams
- Worker network segments table

## Implementation Status

### Completed ✅
- Database schema for network topology
- Core types and structures
- Prometheus metrics with detailed labels
- Network detection module
- Multi-network P2P API
- Routing engine
- Enhanced streamer
- Relay worker support
- Docker Compose test environment
- Monitoring configuration
- Integration test scripts

### Future Enhancements 🚀
1. **Advanced Relay Logic**
   - Load balancing across multiple relays
   - Relay health monitoring
   - Dynamic relay selection

2. **Performance Optimization**
   - Connection pooling
   - Route caching
   - Predictive routing based on historical data

3. **Security**
   - Optional TLS for P2P streams
   - Worker authentication for P2P connections
   - Rate limiting

4. **Operations**
   - Network topology visualization UI
   - P2P streaming troubleshooting tools
   - Automatic network configuration discovery

## Troubleshooting

### Common Issues

1. **Workers not finding P2P routes**
   - Check network configuration in worker logs
   - Verify network segments are correctly configured
   - Ensure workers can reach each other on configured interfaces

2. **Relay not working**
   - Verify relay worker has multiple network interfaces
   - Check `CONCOURSE_BAGGAGECLAIM_P2P_RELAY_ENABLED=true`
   - Look for relay discovery in web logs

3. **Metrics not appearing**
   - Ensure Prometheus endpoint is accessible
   - Check metric registration in logs
   - Verify labels are being set correctly

### Debug Commands

```bash
# Check worker network configuration
docker exec <worker-container> curl http://localhost:7788/network-info

# Test connectivity between workers
docker exec <worker1> curl -X POST http://<worker2>:7788/test-connectivity \
  -d '{"target_url": "http://<worker2>:7788"}'

# View P2P routing decisions
docker logs <web-container> 2>&1 | grep "p2p-routing"

# Monitor active streams
watch 'curl -s http://localhost:9090/metrics | grep active_streams'
```

## Contributing

To contribute to this feature:

1. Follow the existing code structure
2. Add tests for new functionality
3. Update metrics as needed
4. Document configuration changes
5. Test in multi-network environment

## License

This implementation is part of the Concourse CI project and follows the same Apache 2.0 license.