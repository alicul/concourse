# P2P Volume Streaming Troubleshooting Guide

## Quick Diagnostics

### Health Check Script
```bash
#!/bin/bash
# p2p-health-check.sh

echo "=== P2P Volume Streaming Health Check ==="

# Check P2P success rate
P2P_SUCCESS_RATE=$(curl -s http://prometheus:9090/api/v1/query \
  -d 'query=sum(rate(concourse_volumes_streaming_p2p_success_total[5m])) / (sum(rate(concourse_volumes_streaming_p2p_success_total[5m])) + sum(rate(concourse_volumes_streaming_p2p_failure_total[5m])))' \
  | jq -r '.data.result[0].value[1]')

echo "P2P Success Rate: $(echo "$P2P_SUCCESS_RATE * 100" | bc)%"

# Check active relay workers
RELAY_WORKERS=$(fly -t main workers --json | jq '[.[] | select(.relay == true)] | length')
echo "Active Relay Workers: $RELAY_WORKERS"

# Check network topology
NETWORK_SEGMENTS=$(curl -s http://atc.example.com/api/v1/network-topology | jq '.segments | length')
echo "Network Segments: $NETWORK_SEGMENTS"

# Recent failures
echo -e "\nRecent P2P Failures:"
fly -t main workers --json | jq -r '.[] | select(.p2p_failures > 0) | "\(.name): \(.p2p_failures) failures"'
```

## Common Issues and Solutions

### 1. P2P Streaming Completely Failing

#### Symptoms
- All volume streams falling back to ATC
- `concourse_volumes_streaming_p2p_success_total` remains at 0
- Workers show no P2P endpoints

#### Diagnosis
```bash
# Check if P2P is enabled
fly -t main workers --json | jq '.[] | {name: .name, p2p_enabled: .p2p_enabled}'

# Verify P2P endpoints exist
fly -t main workers --json | jq '.[] | {name: .name, p2p_urls: .p2p_urls}'

# Check worker logs for P2P errors
docker logs concourse-worker 2>&1 | grep -i "p2p"
```

#### Solutions
1. **Enable P2P on workers**
   ```yaml
   CONCOURSE_BAGGAGECLAIM_P2P_ENABLED: true
   CONCOURSE_BAGGAGECLAIM_P2P_PORT_RANGE: 7000-7100
   ```

2. **Verify network interfaces**
   ```bash
   # On worker node
   ip addr show | grep -E "eth|docker"

   # Configure specific interfaces
   CONCOURSE_BAGGAGECLAIM_P2P_INTERFACES: "eth0,eth1"
   ```

3. **Check firewall rules**
   ```bash
   # Test P2P port connectivity
   nc -zv worker-2.example.com 7000-7100

   # Open P2P ports
   ufw allow 7000:7100/tcp
   ```

### 2. Low P2P Success Rate (<50%)

#### Symptoms
- High rate of fallback to ATC
- Intermittent P2P failures
- Performance degradation

#### Diagnosis
```bash
# Check connectivity matrix
curl http://atc.example.com/api/v1/connectivity-matrix | jq '.failures'

# View route selection patterns
curl -s http://prometheus:9090/api/v1/query \
  -d 'query=sum(rate(concourse_p2p_routes_by_method_total[5m])) by (method)' \
  | jq '.data.result'

# Check specific worker pairs
fly -t main workers --json | jq '.[] | {name: .name, connectivity: .connectivity_issues}'
```

#### Solutions

1. **Network connectivity issues**
   ```bash
   # Test connectivity between workers
   for worker in $(fly -t main workers --json | jq -r '.[].name'); do
     echo "Testing $worker..."
     curl -m 5 http://$worker:7000/healthz
   done
   ```

2. **Increase timeouts**
   ```yaml
   CONCOURSE_P2P_CONNECTIVITY_TEST_TIMEOUT: "5s"
   CONCOURSE_P2P_STREAM_TIMEOUT: "60s"
   ```

3. **Clear route cache**
   ```bash
   curl -X DELETE http://atc.example.com/api/v1/p2p-route-cache
   ```

### 3. Relay Worker Issues

#### Symptoms
- Relay workers showing high latency
- Connection refused errors
- Capacity exhaustion

#### Diagnosis
```bash
# Check relay worker status
fly -t main workers --json | jq '.[] | select(.relay == true) | {name: .name, active_streams: .active_relay_streams, capacity: .relay_capacity}'

# Monitor relay metrics
curl -s http://prometheus:9090/api/v1/query \
  -d 'query=concourse_relay_streaming_in_progress' \
  | jq '.data.result'

# Check relay worker logs
docker logs relay-worker 2>&1 | grep -E "ERROR|WARN"
```

#### Solutions

1. **Scale relay workers**
   ```bash
   # Deploy additional relay worker
   docker-compose up -d --scale relay-worker=3
   ```

2. **Increase capacity limits**
   ```yaml
   CONCOURSE_WORKER_RELAY_MAX_CONNECTIONS: "500"
   CONCOURSE_WORKER_RELAY_BANDWIDTH_LIMIT: "10GB/s"
   ```

3. **Optimize load balancing**
   ```bash
   # Switch to latency-based routing
   curl -X PUT http://atc.example.com/api/v1/relay-config \
     -d '{"load_balancing": "latency-based"}'
   ```

### 4. Network Topology Detection Problems

#### Symptoms
- Workers not detecting all networks
- Incorrect network segment assignment
- Missing P2P endpoints

#### Diagnosis
```bash
# Check detected networks
fly -t main workers --json | jq '.[] | {name: .name, networks: .networks}'

# Verify network detection on worker
docker exec concourse-worker ip addr show

# Check topology updates
curl http://atc.example.com/api/v1/network-topology/history | jq
```

#### Solutions

1. **Configure network patterns**
   ```yaml
   CONCOURSE_WORKER_NETWORK_PRIVATE_PATTERNS: "10.0.0.0/8,172.16.0.0/12"
   CONCOURSE_WORKER_NETWORK_OVERLAY_PATTERNS: "100.64.0.0/10"
   ```

2. **Manual network registration**
   ```bash
   curl -X PUT http://atc.example.com/api/v1/workers/worker-1/networks \
     -d '{
       "networks": [{
         "segment": "private-vpc",
         "cidr": "10.0.0.0/16",
         "p2p_url": "http://10.0.1.5:7000"
       }]
     }'
   ```

### 5. Performance Issues

#### Symptoms
- High P2P latency (>10s for small volumes)
- Slow volume streaming
- Worker CPU/memory spikes

#### Diagnosis
```bash
# Check streaming duration percentiles
curl -s http://prometheus:9090/api/v1/query \
  -d 'query=histogram_quantile(0.95, sum(rate(concourse_volumes_streaming_duration_seconds_bucket[5m])) by (le, method))' \
  | jq '.data.result'

# Monitor volume sizes
curl -s http://prometheus:9090/api/v1/query \
  -d 'query=histogram_quantile(0.95, sum(rate(concourse_volumes_streaming_size_bytes_bucket[5m])) by (le))' \
  | jq '.data.result'

# Check route cache effectiveness
curl -s http://prometheus:9090/api/v1/query \
  -d 'query=rate(concourse_p2p_route_cache_hits_total[5m]) / (rate(concourse_p2p_route_cache_hits_total[5m]) + rate(concourse_p2p_route_cache_misses_total[5m]))' \
  | jq '.data.result'
```

#### Solutions

1. **Optimize route caching**
   ```yaml
   CONCOURSE_P2P_ROUTE_CACHE_TTL: "10m"
   CONCOURSE_P2P_ROUTE_CACHE_SIZE: "10000"
   ```

2. **Increase P2P concurrency**
   ```yaml
   CONCOURSE_BAGGAGECLAIM_P2P_PORT_RANGE: "7000-7200"
   CONCOURSE_P2P_MAX_CONCURRENT_STREAMS: "50"
   ```

3. **Tune bandwidth limits**
   ```yaml
   CONCOURSE_WORKER_RELAY_BANDWIDTH_LIMIT: "unlimited"
   CONCOURSE_P2P_STREAM_BUFFER_SIZE: "64KB"
   ```

## Debugging Tools

### 1. P2P Stream Tracer
```bash
#!/bin/bash
# trace-p2p-stream.sh

VOLUME_ID=$1
echo "Tracing P2P stream for volume: $VOLUME_ID"

# Enable debug logging
curl -X PUT http://atc.example.com/api/v1/debug/p2p \
  -d '{"volume_id": "'$VOLUME_ID'", "trace": true}'

# Monitor logs
tail -f /var/log/concourse/worker.log | grep $VOLUME_ID
```

### 2. Connectivity Matrix Analyzer
```python
#!/usr/bin/env python3
# analyze-connectivity.py

import requests
import json

resp = requests.get('http://atc.example.com/api/v1/connectivity-matrix')
matrix = resp.json()

print("Connectivity Analysis:")
print("=" * 50)

for src in matrix['workers']:
    failures = 0
    successes = 0

    for dst in matrix['workers']:
        if src == dst:
            continue

        key = f"{src}->{dst}"
        if matrix['connections'].get(key, {}).get('status') == 'connected':
            successes += 1
        else:
            failures += 1

    success_rate = successes / (successes + failures) * 100 if (successes + failures) > 0 else 0
    print(f"{src}: {success_rate:.1f}% connectivity ({failures} failures)")

# Find isolated workers
isolated = []
for worker in matrix['workers']:
    if all(matrix['connections'].get(f"{worker}->{other}", {}).get('status') != 'connected'
           for other in matrix['workers'] if other != worker):
        isolated.append(worker)

if isolated:
    print(f"\nIsolated workers: {', '.join(isolated)}")
```

### 3. Route Path Visualizer
```bash
#!/bin/bash
# visualize-route.sh

SRC_WORKER=$1
DST_WORKER=$2

echo "Route from $SRC_WORKER to $DST_WORKER:"
curl -s "http://atc.example.com/api/v1/p2p-route?src=$SRC_WORKER&dst=$DST_WORKER" | jq -r '
  if .method == "direct" then
    "\(.src) --> \(.dst) [Direct P2P via \(.network_segment)]"
  elif .method == "relay" then
    "\(.src) --> \(.relay_worker) --> \(.dst) [Relay]"
  else
    "\(.src) --> ATC --> \(.dst) [Fallback]"
  end
'
```

## Log Analysis

### Key Log Patterns

#### Worker Logs
```bash
# P2P initialization
grep "P2P endpoint started" worker.log

# Connection failures
grep "P2P connection failed" worker.log | tail -20

# Network detection
grep "Detected network segment" worker.log

# Relay operations
grep "Relay stream" worker.log
```

#### ATC Logs
```bash
# Route decisions
grep "P2P route selected" atc.log

# Topology changes
grep "Network topology updated" atc.log

# Fallback events
grep "Falling back to ATC streaming" atc.log
```

### Log Aggregation Query (Elasticsearch)
```json
{
  "query": {
    "bool": {
      "must": [
        {"term": {"component": "p2p-streaming"}},
        {"range": {"@timestamp": {"gte": "now-1h"}}}
      ],
      "should": [
        {"term": {"level": "ERROR"}},
        {"term": {"level": "WARN"}}
      ]
    }
  },
  "aggs": {
    "error_types": {
      "terms": {"field": "error_type.keyword"}
    },
    "affected_workers": {
      "terms": {"field": "worker.keyword"}
    }
  }
}
```

## Emergency Procedures

### Disable P2P Globally
```bash
# Immediate disable (runtime)
curl -X PUT http://atc.example.com/api/v1/admin/p2p \
  -d '{"enabled": false}'

# Permanent disable (config)
export CONCOURSE_P2P_VOLUME_STREAMING_ENABLED=false
systemctl restart concourse-web
```

### Force ATC Streaming for Specific Worker
```bash
# Blacklist worker from P2P
curl -X PUT http://atc.example.com/api/v1/workers/worker-1/p2p \
  -d '{"enabled": false}'
```

### Reset Network Topology
```bash
# Clear all topology data
curl -X DELETE http://atc.example.com/api/v1/network-topology

# Force re-detection
for worker in $(fly -t main workers --json | jq -r '.[].name'); do
  curl -X POST http://$worker:2222/detect-networks
done
```

### Drain Relay Worker
```bash
# Gracefully drain relay worker
curl -X PUT http://relay-worker:8080/admin/drain \
  -d '{"timeout": "5m"}'

# Wait for active streams to complete
while [ $(curl -s http://relay-worker:8080/metrics | grep relay_streaming_in_progress | awk '{print $2}') -gt 0 ]; do
  echo "Waiting for streams to complete..."
  sleep 10
done

# Remove from rotation
docker stop relay-worker
```

## Monitoring Queries

### Prometheus Queries

```promql
# P2P effectiveness over time
sum(rate(concourse_volumes_streaming_p2p_success_total[5m])) /
(sum(rate(concourse_volumes_streaming_p2p_success_total[5m])) +
 sum(rate(concourse_volumes_streaming_atc_success_total[5m])))

# Worker-specific P2P failures
sum(rate(concourse_volumes_streaming_p2p_failure_total[5m])) by (worker)

# Relay worker load distribution
sum(concourse_relay_streaming_in_progress) by (relay_worker)

# Network segment utilization
sum(rate(concourse_p2p_streaming_by_network_total[5m])) by (network_segment)

# Route cache effectiveness
rate(concourse_p2p_route_cache_hits_total[5m]) /
(rate(concourse_p2p_route_cache_hits_total[5m]) +
 rate(concourse_p2p_route_cache_misses_total[5m]))
```

### Grafana Alert Queries

```sql
-- Workers with high P2P failure rate
SELECT worker,
       COUNT(*) as failures,
       AVG(duration) as avg_duration
FROM p2p_streaming_events
WHERE status = 'failed'
  AND time > NOW() - INTERVAL '1 hour'
GROUP BY worker
HAVING COUNT(*) > 10
ORDER BY failures DESC;

-- Network segments with connectivity issues
SELECT src_segment,
       dst_segment,
       SUM(CASE WHEN connected THEN 1 ELSE 0 END) as connected,
       SUM(CASE WHEN NOT connected THEN 1 ELSE 0 END) as disconnected
FROM network_connectivity_tests
WHERE time > NOW() - INTERVAL '1 hour'
GROUP BY src_segment, dst_segment
HAVING SUM(CASE WHEN NOT connected THEN 1 ELSE 0 END) > 0;
```

## Support Information

### Collecting Diagnostic Bundle
```bash
#!/bin/bash
# collect-p2p-diagnostics.sh

BUNDLE_DIR="p2p-diagnostics-$(date +%Y%m%d-%H%M%S)"
mkdir -p $BUNDLE_DIR

# Collect worker information
fly -t main workers --json > $BUNDLE_DIR/workers.json

# Network topology
curl http://atc.example.com/api/v1/network-topology > $BUNDLE_DIR/topology.json

# Connectivity matrix
curl http://atc.example.com/api/v1/connectivity-matrix > $BUNDLE_DIR/connectivity.json

# Recent metrics
curl http://prometheus:9090/api/v1/query_range \
  -d 'query=concourse_volumes_streaming_p2p_success_total' \
  -d 'start='$(date -d '1 hour ago' +%s) \
  -d 'end='$(date +%s) \
  -d 'step=60' > $BUNDLE_DIR/p2p_metrics.json

# Worker logs (last 1000 lines)
for worker in $(fly -t main workers --json | jq -r '.[].name'); do
  ssh $worker "tail -n 1000 /var/log/concourse/worker.log" > $BUNDLE_DIR/$worker.log 2>&1
done

# Create archive
tar -czf $BUNDLE_DIR.tar.gz $BUNDLE_DIR/
echo "Diagnostic bundle created: $BUNDLE_DIR.tar.gz"
```

### Contact Support
When reporting P2P streaming issues:
1. Run the diagnostic collection script
2. Include the Grafana dashboard screenshots
3. Provide the timeframe of the issue
4. List any recent configuration changes
5. Include worker deployment topology diagram