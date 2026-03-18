# Relay Worker Operations Runbook

## Overview
This runbook provides operational procedures for managing relay workers in Concourse's multi-network P2P volume streaming infrastructure.

## Table of Contents
1. [Quick Reference](#quick-reference)
2. [Deployment Procedures](#deployment-procedures)
3. [Operational Tasks](#operational-tasks)
4. [Emergency Procedures](#emergency-procedures)
5. [Monitoring and Health Checks](#monitoring-and-health-checks)
6. [Troubleshooting](#troubleshooting)
7. [Maintenance Procedures](#maintenance-procedures)

## Quick Reference

### Key Commands
```bash
# Check relay worker status
fly -t main workers --json | jq '.[] | select(.relay == true)'

# View active relay streams
curl http://relay-worker:8080/api/v1/streams

# Check relay capacity
curl http://relay-worker:8080/api/v1/capacity

# Drain relay worker
curl -X POST http://relay-worker:8080/api/v1/drain

# Force stop relay streams
curl -X POST http://relay-worker:8080/api/v1/stop-all
```

### Critical Metrics
- `concourse_relay_workers_active` - Number of active relay workers
- `concourse_relay_streaming_in_progress` - Current relay streams
- `concourse_relay_capacity_used` - Used capacity
- `concourse_relay_streaming_failure_total` - Relay failures

## Deployment Procedures

### 1. Initial Relay Worker Deployment

#### Prerequisites Checklist
- [ ] Multi-network connectivity verified
- [ ] Sufficient bandwidth available (>1Gbps recommended)
- [ ] CPU: 4+ cores, Memory: 8GB+ RAM
- [ ] Docker/Kubernetes environment ready
- [ ] Network firewall rules configured

#### Docker Deployment
```yaml
# docker-compose.yml
version: '3.8'
services:
  relay-worker-1:
    image: concourse/concourse:7.9.0
    container_name: relay-worker-1
    hostname: relay-worker-1
    command: worker
    privileged: true
    environment:
      # TSA connection
      CONCOURSE_TSA_HOST: "atc.example.com:2222"
      CONCOURSE_TSA_PUBLIC_KEY: "/concourse-keys/tsa_host_key.pub"
      CONCOURSE_TSA_WORKER_PRIVATE_KEY: "/concourse-keys/worker_key"

      # Worker identification
      CONCOURSE_WORKER_NAME: "relay-worker-1"
      CONCOURSE_WORKER_TAG: "relay"
      CONCOURSE_WORKER_TEAM: "main"

      # Relay configuration
      CONCOURSE_WORKER_RELAY_ENABLED: "true"
      CONCOURSE_WORKER_RELAY_MAX_CONNECTIONS: "200"
      CONCOURSE_WORKER_RELAY_BANDWIDTH_LIMIT: "10GB/s"
      CONCOURSE_WORKER_RELAY_CONNECTION_TIMEOUT: "30s"

      # P2P configuration
      CONCOURSE_BAGGAGECLAIM_P2P_ENABLED: "true"
      CONCOURSE_BAGGAGECLAIM_P2P_INTERFACES: "eth0,eth1"
      CONCOURSE_BAGGAGECLAIM_P2P_PORT_RANGE: "7000-7100"

      # Resource limits
      CONCOURSE_WORKER_RESOURCE_CPU: "4"
      CONCOURSE_WORKER_RESOURCE_MEMORY: "8GB"

    volumes:
      - /concourse-keys:/concourse-keys:ro
      - relay-work-dir:/worker-state

    networks:
      - network_a
      - network_b
      - management

    restart: unless-stopped
    logging:
      driver: json-file
      options:
        max-size: "100m"
        max-file: "5"

volumes:
  relay-work-dir:

networks:
  network_a:
    external: true
  network_b:
    external: true
  management:
    external: true
```

#### Kubernetes Deployment
```yaml
# relay-worker-deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: relay-worker
  namespace: concourse
spec:
  replicas: 3
  selector:
    matchLabels:
      app: relay-worker
  template:
    metadata:
      labels:
        app: relay-worker
        role: relay
    spec:
      nodeSelector:
        node-role: relay
      affinity:
        podAntiAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
          - labelSelector:
              matchExpressions:
              - key: app
                operator: In
                values: ["relay-worker"]
            topologyKey: kubernetes.io/hostname

      containers:
      - name: relay-worker
        image: concourse/concourse:7.9.0
        command: ["dumb-init", "concourse", "worker"]
        env:
        - name: CONCOURSE_TSA_HOST
          value: "concourse-web:2222"
        - name: CONCOURSE_WORKER_RELAY_ENABLED
          value: "true"
        - name: CONCOURSE_WORKER_RELAY_MAX_CONNECTIONS
          value: "500"
        # ... additional env vars

        resources:
          requests:
            cpu: "2"
            memory: "4Gi"
          limits:
            cpu: "4"
            memory: "8Gi"

        volumeMounts:
        - name: concourse-keys
          mountPath: /concourse-keys
          readOnly: true
        - name: worker-state
          mountPath: /worker-state

      volumes:
      - name: concourse-keys
        secret:
          secretName: concourse-worker-keys
      - name: worker-state
        emptyDir: {}
```

### 2. Adding Relay Workers to Existing Deployment

#### Step-by-step Process
```bash
#!/bin/bash
# add-relay-worker.sh

RELAY_NAME=$1
NETWORKS=$2  # comma-separated list

echo "Adding relay worker: $RELAY_NAME"
echo "Networks: $NETWORKS"

# 1. Deploy the relay worker
docker-compose up -d $RELAY_NAME

# 2. Wait for worker to register
echo "Waiting for worker registration..."
for i in {1..30}; do
  if fly -t main workers --json | jq -r '.[].name' | grep -q $RELAY_NAME; then
    echo "Worker registered successfully"
    break
  fi
  sleep 10
done

# 3. Verify relay capabilities
RELAY_INFO=$(fly -t main workers --json | jq ".[] | select(.name == \"$RELAY_NAME\")")
echo "Relay info: $RELAY_INFO"

# 4. Test connectivity
curl -f http://$RELAY_NAME:8080/health || exit 1

# 5. Verify network bridges
BRIDGES=$(curl -s http://atc.example.com/api/v1/relay-workers/$RELAY_NAME/bridges)
echo "Network bridges: $BRIDGES"

echo "Relay worker $RELAY_NAME added successfully"
```

## Operational Tasks

### 1. Scaling Relay Workers

#### Scale Up
```bash
# Docker Compose
docker-compose up -d --scale relay-worker=5

# Kubernetes
kubectl scale deployment relay-worker --replicas=5 -n concourse

# Verify scaling
watch -n 5 'fly -t main workers --json | jq "[.[] | select(.relay == true)] | length"'
```

#### Scale Down (with graceful drain)
```bash
#!/bin/bash
# scale-down-relay.sh

WORKER_TO_REMOVE=$1

# 1. Mark for drain
curl -X PUT http://$WORKER_TO_REMOVE:8080/api/v1/drain \
  -d '{"accept_new": false, "timeout": "5m"}'

# 2. Wait for active streams to complete
while true; do
  ACTIVE=$(curl -s http://$WORKER_TO_REMOVE:8080/api/v1/streams | jq '.active')
  if [ "$ACTIVE" -eq 0 ]; then
    break
  fi
  echo "Waiting for $ACTIVE active streams to complete..."
  sleep 10
done

# 3. Remove from load balancer
curl -X DELETE http://atc.example.com/api/v1/relay-workers/$WORKER_TO_REMOVE

# 4. Stop the worker
docker stop $WORKER_TO_REMOVE
```

### 2. Load Balancing Configuration

#### Update Strategy
```bash
# Change load balancing strategy
curl -X PUT http://atc.example.com/api/v1/relay-config \
  -H "Content-Type: application/json" \
  -d '{
    "load_balancing": "latency-based",
    "health_check_interval": "30s",
    "max_relay_hops": 2,
    "selection_weights": {
      "latency": 0.6,
      "capacity": 0.3,
      "streams": 0.1
    }
  }'
```

#### Manual Load Distribution
```bash
# Temporarily redirect traffic
curl -X PUT http://atc.example.com/api/v1/relay-workers/relay-1/weight \
  -d '{"weight": 0.1}'  # Reduce load

curl -X PUT http://atc.example.com/api/v1/relay-workers/relay-2/weight \
  -d '{"weight": 0.9}'  # Increase load
```

### 3. Capacity Management

#### Monitor Capacity
```bash
#!/bin/bash
# monitor-relay-capacity.sh

while true; do
  clear
  echo "Relay Worker Capacity Monitor"
  echo "=============================="
  echo

  for worker in $(fly -t main workers --json | jq -r '.[] | select(.relay == true) | .name'); do
    CAPACITY=$(curl -s http://$worker:8080/api/v1/capacity)
    USED=$(echo $CAPACITY | jq '.used')
    AVAILABLE=$(echo $CAPACITY | jq '.available')
    PERCENT=$(echo "scale=2; $USED / $AVAILABLE * 100" | bc)

    printf "%-20s: %3d/%3d (%5.1f%%)\n" $worker $USED $AVAILABLE $PERCENT
  done

  echo
  sleep 5
done
```

#### Adjust Capacity Limits
```bash
# Increase capacity for specific worker
docker exec relay-worker-1 \
  curl -X PUT localhost:8080/api/v1/config \
  -d '{"max_connections": 500, "bandwidth_limit": "20GB/s"}'
```

## Emergency Procedures

### 1. Complete Relay Failure

#### Immediate Response
```bash
#!/bin/bash
# emergency-relay-bypass.sh

echo "EMERGENCY: Bypassing all relay workers"

# 1. Disable relay routing
curl -X PUT http://atc.example.com/api/v1/admin/relay \
  -d '{"enabled": false}'

# 2. Force direct P2P only
curl -X PUT http://atc.example.com/api/v1/admin/p2p \
  -d '{"routing_strategy": "direct_only"}'

# 3. Alert operations team
./send-alert.sh "Relay workers bypassed due to failure"

echo "Relay bypass activated. P2P will use direct connections or fall back to ATC."
```

#### Recovery Steps
1. Investigate relay worker logs
2. Check network connectivity
3. Restart failed relay workers
4. Gradually re-enable relay routing
5. Monitor metrics closely

### 2. Relay Worker Overload

#### Immediate Mitigation
```bash
#!/bin/bash
# mitigate-overload.sh

OVERLOADED_WORKER=$1

# 1. Stop accepting new connections
curl -X PUT http://$OVERLOADED_WORKER:8080/api/v1/connections \
  -d '{"accept_new": false}'

# 2. Deploy emergency relay worker
docker-compose up -d emergency-relay

# 3. Redistribute load
curl -X PUT http://atc.example.com/api/v1/relay-workers/$OVERLOADED_WORKER/weight \
  -d '{"weight": 0.1}'

# 4. Monitor recovery
watch -n 5 "curl -s http://$OVERLOADED_WORKER:8080/api/v1/capacity"
```

### 3. Network Partition

#### Detection and Response
```bash
#!/bin/bash
# handle-network-partition.sh

# 1. Detect partition
PARTITIONS=$(curl -s http://atc.example.com/api/v1/network-partitions)

if [ $(echo $PARTITIONS | jq '. | length') -gt 0 ]; then
  echo "Network partition detected!"

  # 2. Deploy bridge relay workers
  for partition in $(echo $PARTITIONS | jq -r '.[]'); do
    NETWORKS=$(echo $partition | jq -r '.networks | join(",")')
    ./deploy-bridge-relay.sh "bridge-$partition" "$NETWORKS"
  done

  # 3. Update routing tables
  curl -X POST http://atc.example.com/api/v1/routing/recalculate
fi
```

## Monitoring and Health Checks

### 1. Health Check Script
```bash
#!/bin/bash
# relay-health-check.sh

STATUS="HEALTHY"
ISSUES=()

# Check number of relay workers
RELAY_COUNT=$(fly -t main workers --json | jq '[.[] | select(.relay == true)] | length')
if [ $RELAY_COUNT -lt 2 ]; then
  STATUS="DEGRADED"
  ISSUES+=("Only $RELAY_COUNT relay workers active")
fi

# Check relay capacity
for worker in $(fly -t main workers --json | jq -r '.[] | select(.relay == true) | .name'); do
  CAPACITY=$(curl -s http://$worker:8080/api/v1/capacity)
  UTIL=$(echo "$CAPACITY" | jq '.used / .available * 100')

  if (( $(echo "$UTIL > 80" | bc -l) )); then
    STATUS="WARNING"
    ISSUES+=("$worker at $UTIL% capacity")
  fi
done

# Check relay latency
P95_LATENCY=$(curl -s http://prometheus:9090/api/v1/query \
  -d 'query=histogram_quantile(0.95, sum(rate(concourse_relay_streaming_duration_seconds_bucket[5m])) by (le))' \
  | jq -r '.data.result[0].value[1]')

if (( $(echo "$P95_LATENCY > 30" | bc -l) )); then
  STATUS="WARNING"
  ISSUES+=("High relay latency: ${P95_LATENCY}s")
fi

# Report status
echo "Relay Worker Health: $STATUS"
if [ ${#ISSUES[@]} -gt 0 ]; then
  echo "Issues:"
  printf ' - %s\n' "${ISSUES[@]}"
fi
```

### 2. Metrics Dashboard Queries

```promql
# Key metrics for relay monitoring

# Relay utilization by worker
concourse_relay_capacity_used / concourse_relay_capacity_available

# Relay streaming rate
sum(rate(concourse_relay_streaming_success_total[5m])) by (relay_worker)

# Relay latency percentiles
histogram_quantile(0.99,
  sum(rate(concourse_relay_streaming_duration_seconds_bucket[5m])) by (relay_worker, le)
)

# Network bridges utilization
sum(rate(concourse_relay_streaming_bytes[5m])) by (src_network, dst_network)

# Relay failures by reason
sum(rate(concourse_relay_streaming_failure_total[5m])) by (relay_worker, reason)
```

## Troubleshooting

### Issue: Relay Worker Not Registering

#### Diagnosis
```bash
# Check worker logs
docker logs relay-worker-1 2>&1 | grep -E "ERROR|WARN"

# Verify TSA connectivity
docker exec relay-worker-1 nc -zv atc.example.com 2222

# Check authentication
docker exec relay-worker-1 ls -la /concourse-keys/
```

#### Resolution
1. Verify TSA host and port
2. Check worker keys are correct
3. Ensure network connectivity to ATC
4. Review firewall rules
5. Check worker name uniqueness

### Issue: Relay Not Bridging Networks

#### Diagnosis
```bash
# Check network interfaces
docker exec relay-worker-1 ip addr show

# Verify network detection
curl http://relay-worker-1:2222/api/v1/networks

# Test cross-network connectivity
docker exec relay-worker-1 ping -c 1 10.0.0.1  # Network A
docker exec relay-worker-1 ping -c 1 172.16.0.1  # Network B
```

#### Resolution
1. Ensure worker has interfaces in both networks
2. Verify P2P interfaces configuration
3. Check network routing tables
4. Confirm no network policies blocking traffic

### Issue: High Relay Latency

#### Diagnosis
```bash
# Profile relay operations
curl http://relay-worker-1:8080/debug/pprof/profile?seconds=30 > relay.prof
go tool pprof relay.prof

# Check bandwidth usage
iftop -i eth1 -f "port 8080"

# Monitor active streams
watch -n 1 'curl -s http://relay-worker-1:8080/api/v1/streams | jq ".streams[] | {id, duration, bytes_transferred, src, dst}"'
```

#### Resolution
1. Check network bandwidth saturation
2. Increase buffer sizes
3. Optimize TCP settings
4. Consider adding more relay workers
5. Review load balancing strategy

## Maintenance Procedures

### 1. Rolling Update

```bash
#!/bin/bash
# rolling-update-relay.sh

NEW_VERSION=$1
WORKERS=$(fly -t main workers --json | jq -r '.[] | select(.relay == true) | .name')

for worker in $WORKERS; do
  echo "Updating $worker to version $NEW_VERSION"

  # 1. Drain the worker
  curl -X PUT http://$worker:8080/api/v1/drain \
    -d '{"accept_new": false, "timeout": "5m"}'

  # 2. Wait for streams to complete
  while [ $(curl -s http://$worker:8080/api/v1/streams | jq '.active') -gt 0 ]; do
    sleep 10
  done

  # 3. Update the worker
  docker-compose stop $worker
  docker-compose rm -f $worker
  sed -i "s/concourse:.*$/concourse:$NEW_VERSION/" docker-compose.yml
  docker-compose up -d $worker

  # 4. Wait for worker to be healthy
  while ! curl -f http://$worker:8080/health; do
    sleep 5
  done

  # 5. Re-enable traffic
  curl -X PUT http://$worker:8080/api/v1/drain \
    -d '{"accept_new": true}'

  echo "$worker updated successfully"
  sleep 30  # Wait before next worker
done
```

### 2. Backup and Restore

```bash
#!/bin/bash
# backup-relay-config.sh

BACKUP_DIR="relay-backup-$(date +%Y%m%d-%H%M%S)"
mkdir -p $BACKUP_DIR

# Backup relay configuration
for worker in $(fly -t main workers --json | jq -r '.[] | select(.relay == true) | .name'); do
  curl -s http://$worker:8080/api/v1/config > $BACKUP_DIR/$worker-config.json
done

# Backup network topology
curl -s http://atc.example.com/api/v1/network-topology > $BACKUP_DIR/topology.json

# Backup relay routes
curl -s http://atc.example.com/api/v1/relay-routes > $BACKUP_DIR/routes.json

tar -czf $BACKUP_DIR.tar.gz $BACKUP_DIR/
echo "Backup created: $BACKUP_DIR.tar.gz"
```

### 3. Performance Baseline

```bash
#!/bin/bash
# establish-baseline.sh

echo "Establishing relay performance baseline..."

# Run performance tests
for size in 1MB 10MB 100MB 1GB; do
  for i in {1..10}; do
    START=$(date +%s.%N)
    # Simulate relay stream
    curl -X POST http://relay-worker-1:8080/api/v1/test/stream \
      -d "{\"size\": \"$size\"}"
    END=$(date +%s.%N)
    DURATION=$(echo "$END - $START" | bc)
    echo "$size,$DURATION" >> baseline.csv
  done
done

# Calculate percentiles
cat baseline.csv | datamash -t, group 1 perc:50 2 perc:95 2 perc:99 2
```

## Appendix

### A. Configuration Reference

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| CONCOURSE_WORKER_RELAY_ENABLED | false | Enable relay mode |
| CONCOURSE_WORKER_RELAY_MAX_CONNECTIONS | 100 | Maximum concurrent connections |
| CONCOURSE_WORKER_RELAY_BANDWIDTH_LIMIT | 1GB/s | Bandwidth limit per worker |
| CONCOURSE_WORKER_RELAY_CONNECTION_TIMEOUT | 30s | Connection timeout |
| CONCOURSE_WORKER_RELAY_BUFFER_SIZE | 64KB | Stream buffer size |
| CONCOURSE_WORKER_RELAY_KEEPALIVE | 30s | TCP keepalive interval |

### B. API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| /api/v1/streams | GET | List active streams |
| /api/v1/capacity | GET | Get capacity info |
| /api/v1/drain | PUT | Drain worker |
| /api/v1/config | GET/PUT | Get/Set configuration |
| /api/v1/metrics | GET | Prometheus metrics |
| /health | GET | Health check |

### C. Emergency Contacts

- On-call Engineer: PagerDuty relay-oncall
- Platform Team: #platform-team slack
- Network Team: #network-ops slack
- Escalation: platform-escalation@example.com