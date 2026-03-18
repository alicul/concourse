# Multi-Network P2P Volume Streaming Administrator Guide

## Table of Contents
1. [Overview](#overview)
2. [Architecture](#architecture)
3. [Configuration](#configuration)
4. [Network Topology Setup](#network-topology-setup)
5. [Relay Worker Configuration](#relay-worker-configuration)
6. [Monitoring](#monitoring)
7. [Security Considerations](#security-considerations)
8. [Best Practices](#best-practices)

## Overview

Multi-network P2P volume streaming enables Concourse workers to efficiently stream volumes directly between each other across complex network topologies, including:
- Multiple network segments (private, public, overlay)
- Network boundaries requiring relay workers
- Mixed cloud and on-premise deployments
- Kubernetes clusters with network policies

### Benefits
- **Reduced ATC Load**: 60-80% reduction in ATC volume streaming traffic
- **Improved Performance**: Direct P2P transfers reduce latency by 30-50%
- **Network Efficiency**: Traffic stays within network segments when possible
- **Scalability**: Relay workers enable scaling across network boundaries
- **Resilience**: Automatic fallback to ATC when P2P unavailable

## Architecture

### Components

#### Network Topology Discovery
- Workers automatically detect their network interfaces
- Report network segments and P2P endpoints to ATC
- Test connectivity to other workers
- Maintain up-to-date network topology

#### P2P Routing Engine
- Intelligent route selection based on network topology
- Connectivity testing before streaming
- Route caching for performance
- Automatic fallback to relay or ATC

#### Relay Workers
- Bridge disconnected network segments
- HTTP-based stream proxying
- Bandwidth and connection limiting
- Load balancing across multiple relays

### Data Flow

```
┌─────────────────────────────────────────────────────────┐
│                     ATC (Orchestrator)                   │
│  - Network topology management                           │
│  - Route calculation                                     │
│  - Relay worker coordination                             │
└─────────────────────────────────────────────────────────┘
                            │
        ┌───────────────────┼───────────────────┐
        │                   │                   │
┌───────▼────────┐ ┌───────▼────────┐ ┌───────▼────────┐
│ Network Seg A  │ │ Network Seg B  │ │ Network Seg C  │
│                │ │                │ │                │
│  Worker A1 ←───┼─┼→ Worker B1     │ │  Worker C1     │
│  Worker A2     │ │  Worker B2 ←───┼─┼→ Worker C2     │
└────────────────┘ └────────────────┘ └────────────────┘
        │                   │                   │
        └───────────────────┼───────────────────┘
                    [Relay Worker]
```

## Configuration

### Worker Configuration

```yaml
# worker.yml
CONCOURSE_BAGGAGECLAIM_P2P_ENABLED: true
CONCOURSE_BAGGAGECLAIM_P2P_INTERFACES: eth0,eth1,docker0
CONCOURSE_BAGGAGECLAIM_P2P_PORT_RANGE: 7000-7100

# Network detection patterns (optional)
CONCOURSE_WORKER_NETWORK_PRIVATE_PATTERNS: "10.0.0.0/8,172.16.0.0/12,192.168.0.0/16"
CONCOURSE_WORKER_NETWORK_OVERLAY_PATTERNS: "100.64.0.0/10"

# Relay capabilities (for relay workers only)
CONCOURSE_WORKER_RELAY_ENABLED: true
CONCOURSE_WORKER_RELAY_MAX_CONNECTIONS: 100
CONCOURSE_WORKER_RELAY_BANDWIDTH_LIMIT: "100MB/s"
```

### ATC Configuration

```yaml
# atc.yml
CONCOURSE_P2P_VOLUME_STREAMING_ENABLED: true
CONCOURSE_P2P_MULTI_NETWORK_ENABLED: true
CONCOURSE_P2P_RELAY_ENABLED: true

# Routing preferences
CONCOURSE_P2P_ROUTING_STRATEGY: "latency" # direct, relay, latency
CONCOURSE_P2P_ROUTE_CACHE_TTL: "5m"
CONCOURSE_P2P_CONNECTIVITY_TEST_TIMEOUT: "2s"

# Load balancing for relay workers
CONCOURSE_RELAY_LOAD_BALANCING_STRATEGY: "least-connections" # round-robin, weighted-random, latency-based
```

## Network Topology Setup

### 1. Enable Network Discovery

Workers automatically discover their network configuration on startup:

```bash
# View worker's detected networks
fly -t main workers --details

# Example output:
name: worker-1
networks:
  - segment: private-vpc-1
    cidr: 10.0.0.0/16
    type: private
    p2p-url: http://10.0.1.5:7000
  - segment: overlay-network
    cidr: 100.64.0.0/10
    type: overlay
    p2p-url: http://100.64.0.5:7000
```

### 2. Verify Network Connectivity

```bash
# Check connectivity matrix
curl http://atc.example.com/api/v1/connectivity-matrix | jq

# Test specific worker connectivity
curl http://atc.example.com/api/v1/workers/worker-1/connectivity | jq
```

### 3. Configure Network Segments (Optional)

For custom network segmentation:

```bash
# Define a network segment
curl -X POST http://atc.example.com/api/v1/network-segments \
  -H "Content-Type: application/json" \
  -d '{
    "name": "dmz-network",
    "cidr": "172.20.0.0/16",
    "type": "dmz",
    "priority": 10
  }'
```

## Relay Worker Configuration

### Prerequisites

Relay workers must:
- Have connectivity to multiple network segments
- Have sufficient bandwidth for proxying
- Be deployed on stable, high-performance nodes

### Setup Steps

1. **Deploy Relay Worker**
```yaml
# docker-compose.yml for relay worker
version: '3'
services:
  relay-worker:
    image: concourse/concourse:latest
    command: worker
    environment:
      CONCOURSE_TSA_HOST: atc.example.com:2222
      CONCOURSE_TSA_PUBLIC_KEY: /concourse-keys/tsa_host_key.pub
      CONCOURSE_TSA_WORKER_PRIVATE_KEY: /concourse-keys/worker_key

      # Enable relay mode
      CONCOURSE_WORKER_RELAY_ENABLED: "true"
      CONCOURSE_WORKER_RELAY_MAX_CONNECTIONS: "200"
      CONCOURSE_WORKER_RELAY_BANDWIDTH_LIMIT: "1GB/s"

      # Network interfaces for multi-network
      CONCOURSE_BAGGAGECLAIM_P2P_INTERFACES: "eth0,eth1"
    networks:
      - network_a
      - network_b
    volumes:
      - /concourse-keys:/concourse-keys
```

2. **Register Relay Worker**
```bash
# Relay workers auto-register, verify with:
fly -t main workers --details | grep relay

# Should show:
relay: true
networks: [network-a, network-b]
relay-bridges: ["network-a:network-b"]
```

3. **Configure Load Balancing**
```bash
# Set relay load balancing strategy
curl -X PUT http://atc.example.com/api/v1/relay-config \
  -H "Content-Type: application/json" \
  -d '{
    "load_balancing": "least-connections",
    "health_check_interval": "30s",
    "max_relay_hops": 2
  }'
```

## Monitoring

### Key Metrics

#### P2P Effectiveness
- `concourse_volumes_streaming_p2p_success_total` - Successful P2P streams
- `concourse_volumes_streaming_p2p_failure_total` - Failed P2P attempts
- `concourse_p2p_routes_by_method_total{method}` - Route selection (direct/relay/atc)

#### Network Health
- `concourse_network_connectivity_tests_total` - Connectivity test attempts
- `concourse_network_topology_changes_total` - Topology stability
- `concourse_network_segments_discovered{worker}` - Segments per worker

#### Relay Performance
- `concourse_relay_streaming_in_progress` - Active relay streams
- `concourse_relay_streaming_duration_seconds` - Relay latency
- `concourse_relay_capacity_used` / `concourse_relay_capacity_available` - Capacity utilization
- `concourse_relay_streaming_bytes` - Bandwidth usage

### Grafana Dashboards

Import the provided dashboards:
- `monitoring/dashboards/multi-network-p2p.json` - Overall P2P monitoring
- `monitoring/dashboards/relay-worker-operations.json` - Relay worker specific

### Alert Examples

```yaml
# prometheus-alerts.yml
groups:
  - name: p2p_streaming
    rules:
      - alert: LowP2PSuccessRate
        expr: |
          sum(rate(concourse_volumes_streaming_p2p_success_total[5m])) /
          (sum(rate(concourse_volumes_streaming_p2p_success_total[5m])) +
           sum(rate(concourse_volumes_streaming_p2p_failure_total[5m]))) < 0.5
        for: 10m
        annotations:
          summary: "P2P success rate below 50%"

      - alert: RelayWorkerDown
        expr: concourse_relay_workers_active < 1
        for: 5m
        annotations:
          summary: "No relay workers available"

      - alert: HighRelayLatency
        expr: |
          histogram_quantile(0.95,
            sum(rate(concourse_relay_streaming_duration_seconds_bucket[5m])) by (le)
          ) > 30
        for: 10m
        annotations:
          summary: "Relay streaming p95 latency > 30s"
```

## Security Considerations

### Network Isolation
- P2P connections respect network boundaries
- Workers only connect within allowed segments
- Relay workers provide controlled bridging

### Authentication
- P2P endpoints use worker authentication tokens
- Relay streams require valid worker credentials
- All connections use TLS in production

### Firewall Rules

Required ports:
- Worker P2P: 7000-7100/tcp (configurable)
- Relay proxy: 8080/tcp (default)
- Worker heartbeat: 2222/tcp (TSA)

Example firewall rules:
```bash
# Allow P2P between workers in same segment
iptables -A INPUT -s 10.0.0.0/16 -p tcp --dport 7000:7100 -j ACCEPT

# Allow relay traffic from trusted relay workers
iptables -A INPUT -s relay-worker.example.com -p tcp --dport 8080 -j ACCEPT
```

## Best Practices

### 1. Network Design
- Deploy workers close to their workloads
- Minimize network hops between workers
- Use dedicated network segments for high-volume transfers
- Place relay workers at network boundaries

### 2. Relay Worker Placement
- Deploy 2-3 relay workers per network boundary
- Use high-bandwidth instances for relay workers
- Monitor relay capacity and scale accordingly
- Distribute relay workers across availability zones

### 3. Performance Tuning
- Increase P2P port range for high concurrency
- Tune connectivity test timeouts for network latency
- Use route caching to reduce overhead
- Monitor and adjust bandwidth limits

### 4. Monitoring and Alerting
- Set up dashboards before deployment
- Configure alerts for P2P success rate
- Monitor relay worker health
- Track network topology changes

### 5. Gradual Rollout
1. Enable P2P on test workers first
2. Monitor metrics and logs
3. Add relay workers if needed
4. Expand to production gradually
5. Fine-tune based on observed performance

### 6. Troubleshooting Preparation
- Document network topology
- Test connectivity tools
- Prepare runbooks for common issues
- Train operations team

## Common Issues and Solutions

### Issue: Low P2P Success Rate
**Symptoms**: High fallback to ATC streaming
**Solutions**:
- Check network connectivity between workers
- Verify firewall rules allow P2P ports
- Increase connectivity test timeout
- Check worker network configuration

### Issue: Relay Worker Overload
**Symptoms**: High relay latency, connection failures
**Solutions**:
- Add more relay workers
- Increase connection/bandwidth limits
- Distribute load with better placement
- Use different load balancing strategy

### Issue: Network Topology Instability
**Symptoms**: Frequent topology changes, route cache misses
**Solutions**:
- Stabilize worker network configuration
- Increase topology update interval
- Check for network interface flapping
- Review network detection patterns

## References

- [P2P Volume Streaming RFC](https://github.com/concourse/rfcs/blob/master/082-p2p-volume-streaming/proposal.md)
- [Network Topology API Documentation](./api/network-topology.md)
- [Relay Worker Operations Runbook](./runbooks/relay-workers.md)
- [Performance Tuning Guide](./performance-tuning.md)