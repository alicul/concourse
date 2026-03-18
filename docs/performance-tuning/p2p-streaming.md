# P2P Volume Streaming Performance Tuning Guide

## Performance Baseline Metrics

### Target Performance Indicators
| Metric | Good | Acceptable | Poor | Action Required |
|--------|------|------------|------|-----------------|
| P2P Success Rate | >80% | 60-80% | <60% | Investigate connectivity |
| P2P Stream Latency (p50) | <2s | 2-5s | >5s | Optimize routing |
| P2P Stream Latency (p95) | <10s | 10-30s | >30s | Scale relay workers |
| Route Cache Hit Rate | >90% | 70-90% | <70% | Increase cache TTL |
| Relay Utilization | <60% | 60-80% | >80% | Add relay capacity |
| Network Topology Changes | <10/hour | 10-50/hour | >50/hour | Stabilize network |

## Network Optimization

### 1. Network Interface Selection

#### Automatic Detection (Default)
```yaml
# Let workers auto-detect interfaces
CONCOURSE_BAGGAGECLAIM_P2P_INTERFACES: ""
```

#### Manual Configuration (Optimized)
```yaml
# Specify high-bandwidth interfaces
CONCOURSE_BAGGAGECLAIM_P2P_INTERFACES: "eth0,eth1"

# Exclude slow interfaces
CONCOURSE_BAGGAGECLAIM_P2P_EXCLUDE_INTERFACES: "docker0,virbr0"
```

#### Multi-NIC Optimization
```bash
# Dedicate NICs for P2P traffic
# eth0: Management traffic
# eth1: P2P streaming (10Gbps)
# eth2: Worker communication

CONCOURSE_BAGGAGECLAIM_P2P_INTERFACES: "eth1"
CONCOURSE_TSA_BIND_IP: "eth0"
```

### 2. MTU Optimization

```bash
# Check current MTU
ip link show | grep mtu

# Set jumbo frames for P2P interfaces (if supported)
sudo ip link set dev eth1 mtu 9000

# Verify end-to-end MTU
ping -M do -s 8972 other-worker.example.com
```

Configuration:
```yaml
# Worker configuration
CONCOURSE_P2P_NETWORK_MTU: "9000"
CONCOURSE_P2P_TCP_NODELAY: "true"
```

### 3. TCP Tuning

System-level optimizations:
```bash
# /etc/sysctl.d/99-p2p-tuning.conf

# Increase TCP buffer sizes
net.core.rmem_max = 134217728
net.core.wmem_max = 134217728
net.ipv4.tcp_rmem = 4096 87380 134217728
net.ipv4.tcp_wmem = 4096 65536 134217728

# Enable TCP Fast Open
net.ipv4.tcp_fastopen = 3

# Optimize for low latency
net.ipv4.tcp_low_latency = 1

# Increase connection backlog
net.core.somaxconn = 4096
net.ipv4.tcp_max_syn_backlog = 8192

# Enable BBR congestion control
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr

# Apply settings
sudo sysctl -p /etc/sysctl.d/99-p2p-tuning.conf
```

## P2P Configuration Tuning

### 1. Port Range Optimization

```yaml
# Small deployments (< 50 workers)
CONCOURSE_BAGGAGECLAIM_P2P_PORT_RANGE: "7000-7050"

# Medium deployments (50-200 workers)
CONCOURSE_BAGGAGECLAIM_P2P_PORT_RANGE: "7000-7100"

# Large deployments (> 200 workers)
CONCOURSE_BAGGAGECLAIM_P2P_PORT_RANGE: "7000-7500"

# Maximum concurrent streams per worker
CONCOURSE_P2P_MAX_CONCURRENT_STREAMS: "100"
```

### 2. Connection Pooling

```yaml
# Connection pool settings
CONCOURSE_P2P_CONNECTION_POOL_SIZE: "50"
CONCOURSE_P2P_CONNECTION_IDLE_TIMEOUT: "5m"
CONCOURSE_P2P_CONNECTION_MAX_AGE: "1h"

# Keep-alive settings
CONCOURSE_P2P_KEEPALIVE_INTERVAL: "30s"
CONCOURSE_P2P_KEEPALIVE_PROBES: "3"
```

### 3. Streaming Buffer Optimization

```yaml
# Small volumes (< 100MB typical)
CONCOURSE_P2P_STREAM_BUFFER_SIZE: "32KB"
CONCOURSE_P2P_READ_BUFFER_SIZE: "32KB"
CONCOURSE_P2P_WRITE_BUFFER_SIZE: "32KB"

# Large volumes (> 1GB typical)
CONCOURSE_P2P_STREAM_BUFFER_SIZE: "256KB"
CONCOURSE_P2P_READ_BUFFER_SIZE: "256KB"
CONCOURSE_P2P_WRITE_BUFFER_SIZE: "256KB"

# Mixed workloads (adaptive)
CONCOURSE_P2P_ADAPTIVE_BUFFERING: "true"
CONCOURSE_P2P_MIN_BUFFER_SIZE: "32KB"
CONCOURSE_P2P_MAX_BUFFER_SIZE: "1MB"
```

## Routing Engine Optimization

### 1. Route Cache Tuning

```yaml
# Cache configuration
CONCOURSE_P2P_ROUTE_CACHE_TTL: "5m"      # Short for dynamic networks
CONCOURSE_P2P_ROUTE_CACHE_TTL: "30m"     # Long for stable networks
CONCOURSE_P2P_ROUTE_CACHE_SIZE: "10000"  # Number of cached routes

# Cache warming
CONCOURSE_P2P_ROUTE_CACHE_WARM_ON_START: "true"
CONCOURSE_P2P_ROUTE_CACHE_WARM_WORKERS: "50"  # Top N active workers
```

### 2. Connectivity Testing

```yaml
# Test timeout optimization
CONCOURSE_P2P_CONNECTIVITY_TEST_TIMEOUT: "1s"   # Fast networks
CONCOURSE_P2P_CONNECTIVITY_TEST_TIMEOUT: "5s"   # Slow/WAN networks

# Parallel testing
CONCOURSE_P2P_CONNECTIVITY_TEST_PARALLEL: "10"  # Test N endpoints simultaneously

# Test caching
CONCOURSE_P2P_CONNECTIVITY_CACHE_TTL: "10m"
```

### 3. Route Selection Strategies

```yaml
# Latency-based (best for performance)
CONCOURSE_P2P_ROUTING_STRATEGY: "latency"
CONCOURSE_P2P_LATENCY_SAMPLE_SIZE: "3"
CONCOURSE_P2P_LATENCY_PERCENTILE: "50"  # Use median

# Direct-first (minimize hops)
CONCOURSE_P2P_ROUTING_STRATEGY: "direct"
CONCOURSE_P2P_PREFER_SAME_NETWORK: "true"

# Load-balanced (distribute traffic)
CONCOURSE_P2P_ROUTING_STRATEGY: "load-balanced"
CONCOURSE_P2P_LOAD_THRESHOLD: "0.7"  # Switch routes at 70% capacity
```

## Relay Worker Optimization

### 1. Relay Placement Strategy

```yaml
# Geographic distribution
- Deploy relays in each availability zone
- Place relays at network boundaries
- Colocate with high-traffic workers

# Network topology
- One relay per network segment pair
- Additional relays for high-traffic routes
- Dedicated relays for critical paths
```

### 2. Relay Resource Allocation

```yaml
# CPU optimization
CONCOURSE_WORKER_RELAY_CPU_LIMIT: "4"
CONCOURSE_WORKER_RELAY_CPU_RESERVATION: "2"

# Memory optimization
CONCOURSE_WORKER_RELAY_MEMORY_LIMIT: "8GB"
CONCOURSE_WORKER_RELAY_MEMORY_RESERVATION: "4GB"

# Network bandwidth
CONCOURSE_WORKER_RELAY_BANDWIDTH_LIMIT: "10GB/s"
CONCOURSE_WORKER_RELAY_BURST_LIMIT: "15GB/s"
CONCOURSE_WORKER_RELAY_BURST_DURATION: "10s"
```

### 3. Load Balancing Optimization

```yaml
# Least connections (best for long streams)
CONCOURSE_RELAY_LOAD_BALANCING_STRATEGY: "least-connections"

# Latency-based (best for performance)
CONCOURSE_RELAY_LOAD_BALANCING_STRATEGY: "latency-based"
CONCOURSE_RELAY_LATENCY_WEIGHT: "0.7"
CONCOURSE_RELAY_CAPACITY_WEIGHT: "0.3"

# Weighted random (best for distribution)
CONCOURSE_RELAY_LOAD_BALANCING_STRATEGY: "weighted-random"
CONCOURSE_RELAY_WEIGHT_UPDATE_INTERVAL: "30s"
```

### 4. Connection Management

```yaml
# Connection limits
CONCOURSE_WORKER_RELAY_MAX_CONNECTIONS: "500"
CONCOURSE_WORKER_RELAY_MAX_CONNECTIONS_PER_WORKER: "10"
CONCOURSE_WORKER_RELAY_CONNECTION_TIMEOUT: "30s"

# Connection reuse
CONCOURSE_WORKER_RELAY_CONNECTION_POOL: "true"
CONCOURSE_WORKER_RELAY_CONNECTION_POOL_SIZE: "100"
CONCOURSE_WORKER_RELAY_CONNECTION_IDLE_TIMEOUT: "5m"
```

## Storage Optimization

### 1. Volume Locality

```bash
# Ensure volumes are on fast storage
CONCOURSE_BAGGAGECLAIM_DRIVER: "btrfs"  # Best performance
CONCOURSE_BAGGAGECLAIM_DRIVER: "overlay"  # Good performance
CONCOURSE_BAGGAGECLAIM_DRIVER: "naive"   # Compatibility mode

# Use SSD for baggageclaim volumes
mount | grep baggageclaim
# Should show: /dev/nvme0n1 on /var/lib/concourse/volumes
```

### 2. Compression

```yaml
# Enable compression for large volumes
CONCOURSE_P2P_COMPRESSION_ENABLED: "true"
CONCOURSE_P2P_COMPRESSION_ALGORITHM: "lz4"  # Fast
CONCOURSE_P2P_COMPRESSION_ALGORITHM: "zstd"  # Better ratio
CONCOURSE_P2P_COMPRESSION_LEVEL: "3"  # 1-9, higher = better compression

# Compression thresholds
CONCOURSE_P2P_COMPRESSION_MIN_SIZE: "1MB"
CONCOURSE_P2P_COMPRESSION_MIN_BENEFIT: "0.1"  # 10% size reduction
```

### 3. Caching Strategy

```yaml
# Volume caching
CONCOURSE_BAGGAGECLAIM_CACHE_SIZE: "50GB"
CONCOURSE_BAGGAGECLAIM_CACHE_TTL: "1h"

# P2P stream caching
CONCOURSE_P2P_STREAM_CACHE: "true"
CONCOURSE_P2P_STREAM_CACHE_SIZE: "10GB"
CONCOURSE_P2P_STREAM_CACHE_TTL: "30m"
```

## Monitoring and Alerting

### 1. Performance Monitoring Queries

```promql
# P2P streaming efficiency
sum(rate(concourse_volumes_streaming_p2p_success_total[5m])) /
sum(rate(concourse_volumes_streaming_total[5m])) * 100

# Average streaming duration by method
avg(rate(concourse_volumes_streaming_duration_seconds_sum[5m]) /
    rate(concourse_volumes_streaming_duration_seconds_count[5m])) by (method)

# Relay worker efficiency
sum(rate(concourse_relay_streaming_bytes[5m])) by (relay_worker) /
sum(concourse_relay_capacity_available) by (relay_worker) * 100

# Network utilization
sum(rate(concourse_p2p_streaming_by_network_total[5m])) by (network_segment) /
sum(rate(concourse_p2p_streaming_by_network_total[5m])) * 100
```

### 2. Performance Alerts

```yaml
groups:
  - name: p2p_performance
    rules:
      - alert: HighP2PLatency
        expr: |
          histogram_quantile(0.95,
            sum(rate(concourse_volumes_streaming_duration_seconds_bucket{method="p2p"}[5m])) by (le)
          ) > 30
        for: 10m
        annotations:
          summary: "P2P streaming p95 latency > 30s"
          runbook: "docs/runbooks/high-p2p-latency.md"

      - alert: LowRouteCacheHitRate
        expr: |
          rate(concourse_p2p_route_cache_hits_total[5m]) /
          (rate(concourse_p2p_route_cache_hits_total[5m]) +
           rate(concourse_p2p_route_cache_misses_total[5m])) < 0.7
        for: 10m
        annotations:
          summary: "Route cache hit rate below 70%"

      - alert: RelayWorkerOverload
        expr: |
          concourse_relay_capacity_used /
          concourse_relay_capacity_available > 0.9
        for: 5m
        annotations:
          summary: "Relay worker at >90% capacity"
```

## Benchmarking

### 1. P2P Performance Test

```bash
#!/bin/bash
# benchmark-p2p.sh

# Test configuration
TEST_VOLUME_SIZES=(1MB 10MB 100MB 1GB 10GB)
TEST_ITERATIONS=10
WORKERS=(worker-1 worker-2 worker-3)

echo "P2P Performance Benchmark"
echo "========================="

for size in "${TEST_VOLUME_SIZES[@]}"; do
  echo -e "\nTesting $size volumes:"

  for ((i=1; i<=$TEST_ITERATIONS; i++)); do
    # Create test volume
    VOLUME_ID=$(fly -t main execute \
      --config test-job.yml \
      --output volume-$size-$i \
      2>&1 | grep "volume:" | cut -d' ' -f2)

    # Measure transfer time
    START=$(date +%s.%N)
    fly -t main execute \
      --config transfer-job.yml \
      --input test-volume=volume-$size-$i \
      2>&1 > /dev/null
    END=$(date +%s.%N)

    DURATION=$(echo "$END - $START" | bc)
    echo "  Iteration $i: ${DURATION}s"
  done
done
```

### 2. Network Throughput Test

```bash
#!/bin/bash
# test-network-throughput.sh

# Test P2P network throughput between workers
for src in "${WORKERS[@]}"; do
  for dst in "${WORKERS[@]}"; do
    if [ "$src" != "$dst" ]; then
      echo "Testing $src -> $dst:"

      # Run iperf3 test
      ssh $dst "iperf3 -s -1 -p 7001" &
      sleep 2
      ssh $src "iperf3 -c $dst -p 7001 -t 10 -J" | jq '.end.sum_received.bits_per_second / 1000000000'
    fi
  done
done
```

### 3. Relay Performance Test

```bash
#!/bin/bash
# benchmark-relay.sh

RELAY_WORKERS=(relay-1 relay-2 relay-3)
TEST_DURATION=60

echo "Relay Worker Performance Test"
echo "=============================="

for relay in "${RELAY_WORKERS[@]}"; do
  echo -e "\nTesting $relay:"

  # Generate load
  for ((i=1; i<=10; i++)); do
    curl -X POST http://$relay:8080/test/stream \
      -d "{\"size\": \"100MB\", \"duration\": $TEST_DURATION}" &
  done

  # Monitor metrics
  sleep $TEST_DURATION

  # Collect results
  THROUGHPUT=$(curl -s http://$relay:8080/metrics | grep relay_streaming_bytes | tail -1)
  LATENCY=$(curl -s http://$relay:8080/metrics | grep relay_streaming_duration_seconds | grep quantile=\"0.95\")

  echo "  Throughput: $THROUGHPUT"
  echo "  P95 Latency: $LATENCY"
done
```

## Optimization Checklist

### Initial Deployment
- [ ] Configure network interfaces explicitly
- [ ] Set appropriate MTU values
- [ ] Apply TCP tuning parameters
- [ ] Configure P2P port range
- [ ] Set buffer sizes based on workload
- [ ] Enable route caching
- [ ] Deploy relay workers at network boundaries

### Performance Issues
- [ ] Check P2P success rate metrics
- [ ] Verify network connectivity matrix
- [ ] Review route cache hit rate
- [ ] Monitor relay worker utilization
- [ ] Analyze streaming duration percentiles
- [ ] Check for network topology changes
- [ ] Review worker resource utilization

### Scaling
- [ ] Increase P2P port range
- [ ] Add relay workers
- [ ] Optimize load balancing strategy
- [ ] Increase cache sizes
- [ ] Tune connection pools
- [ ] Enable compression
- [ ] Implement volume caching

## Advanced Optimizations

### 1. NUMA Optimization
```bash
# Pin relay workers to NUMA nodes
numactl --cpunodebind=0 --membind=0 ./relay-worker

# Check NUMA topology
numactl --hardware
```

### 2. Kernel Bypass (DPDK)
```yaml
# Enable DPDK for ultra-low latency
CONCOURSE_P2P_DPDK_ENABLED: "true"
CONCOURSE_P2P_DPDK_CORES: "0-3"
CONCOURSE_P2P_DPDK_MEMORY: "2048"
```

### 3. Hardware Offload
```bash
# Enable NIC offload features
ethtool -K eth1 tso on
ethtool -K eth1 gso on
ethtool -K eth1 gro on
ethtool -K eth1 lro on
```

## Troubleshooting Performance Issues

### Slow P2P Transfers
1. Check network bandwidth: `iperf3`
2. Verify MTU settings: `ping -M do -s`
3. Review TCP tuning: `ss -tin`
4. Check route selection: API calls
5. Monitor relay utilization: Prometheus

### High CPU Usage
1. Profile P2P operations: `pprof`
2. Check compression settings
3. Review buffer sizes
4. Optimize connection pooling
5. Consider hardware offload

### Memory Issues
1. Monitor connection counts
2. Review cache sizes
3. Check for memory leaks
4. Optimize buffer allocation
5. Implement connection limits