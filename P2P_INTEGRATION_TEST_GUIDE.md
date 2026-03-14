# P2P Multi-Network Integration Test Guide

## Prerequisites

1. Docker and Docker Compose installed
2. Concourse `fly` CLI installed
3. Go development environment
4. Network analysis tools (tcpdump, netstat)

## Test Environment Setup

### Step 1: Create Multi-Network Docker Compose Configuration

Create `docker-compose-p2p-multinetwork.yml`:

```yaml
version: '3.8'

networks:
  # Management network - all services connected
  mgmt:
    driver: bridge
    ipam:
      config:
        - subnet: 172.19.0.0/16

  # Network Segment 1 - Worker Group A
  segment1:
    driver: bridge
    ipam:
      config:
        - subnet: 172.20.0.0/16

  # Network Segment 2 - Worker Group B
  segment2:
    driver: bridge
    ipam:
      config:
        - subnet: 172.21.0.0/16

  # Network Segment 3 - Isolated
  segment3:
    driver: bridge
    ipam:
      config:
        - subnet: 172.22.0.0/16

services:
  db:
    image: postgres:15
    networks:
      - mgmt
    environment:
      POSTGRES_DB: concourse
      POSTGRES_USER: dev
      POSTGRES_PASSWORD: dev
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U dev -d concourse"]
      interval: 3s
      timeout: 3s
      retries: 5

  web:
    build: .
    image: concourse/concourse:p2p-multinetwork
    command: web
    depends_on:
      db:
        condition: service_healthy
    ports:
      - "8080:8080"
      - "9090:9090"  # Prometheus metrics
    networks:
      - mgmt
      - segment1
      - segment2
      - segment3
    volumes:
      - "./hack/keys:/concourse-keys"
    environment:
      CONCOURSE_SESSION_SIGNING_KEY: /concourse-keys/session_signing_key
      CONCOURSE_TSA_AUTHORIZED_KEYS: /concourse-keys/authorized_worker_keys
      CONCOURSE_TSA_HOST_KEY: /concourse-keys/tsa_host_key
      CONCOURSE_LOG_LEVEL: debug
      CONCOURSE_POSTGRES_HOST: db
      CONCOURSE_POSTGRES_USER: dev
      CONCOURSE_POSTGRES_PASSWORD: dev
      CONCOURSE_POSTGRES_DATABASE: concourse
      CONCOURSE_EXTERNAL_URL: http://localhost:8080
      CONCOURSE_ADD_LOCAL_USER: test:test
      CONCOURSE_MAIN_TEAM_LOCAL_USER: test
      CONCOURSE_CLUSTER_NAME: p2p-test
      # P2P Configuration
      CONCOURSE_ENABLE_P2P_VOLUME_STREAMING: "true"
      CONCOURSE_P2P_MULTI_NETWORK_ENABLED: "true"
      CONCOURSE_P2P_RELAY_WORKERS_ENABLED: "true"
      CONCOURSE_P2P_NETWORK_TOPOLOGY_REFRESH: "30s"
      CONCOURSE_P2P_VOLUME_STREAMING_TIMEOUT: "5m"
      # Metrics
      CONCOURSE_PROMETHEUS_BIND_IP: "0.0.0.0"
      CONCOURSE_PROMETHEUS_BIND_PORT: "9090"

  # Worker in segment1 only
  worker-s1:
    build: .
    image: concourse/concourse:p2p-multinetwork
    command: worker
    privileged: true
    cgroup: host
    depends_on:
      - web
    networks:
      - mgmt
      - segment1
    volumes:
      - "./hack/keys:/concourse-keys"
      - "/var/run/docker.sock:/var/run/docker.sock"
    stop_signal: SIGUSR2
    environment:
      CONCOURSE_NAME: worker-s1
      CONCOURSE_RUNTIME: containerd
      CONCOURSE_TSA_PUBLIC_KEY: /concourse-keys/tsa_host_key.pub
      CONCOURSE_TSA_WORKER_PRIVATE_KEY: /concourse-keys/worker_key
      CONCOURSE_LOG_LEVEL: debug
      CONCOURSE_TSA_HOST: web:2222
      CONCOURSE_BIND_IP: 0.0.0.0
      CONCOURSE_BAGGAGECLAIM_BIND_IP: 0.0.0.0
      CONCOURSE_BAGGAGECLAIM_DRIVER: overlay
      # P2P Multi-Network Config
      CONCOURSE_BAGGAGECLAIM_P2P_INTERFACES: |
        - pattern: "eth1"
          network_segment: "segment1"
          priority: 1
      CONCOURSE_BAGGAGECLAIM_P2P_NETWORK_DETECTION: "auto"

  # Worker in segment2 only
  worker-s2:
    build: .
    image: concourse/concourse:p2p-multinetwork
    command: worker
    privileged: true
    cgroup: host
    depends_on:
      - web
    networks:
      - mgmt
      - segment2
    volumes:
      - "./hack/keys:/concourse-keys"
      - "/var/run/docker.sock:/var/run/docker.sock"
    stop_signal: SIGUSR2
    environment:
      CONCOURSE_NAME: worker-s2
      CONCOURSE_RUNTIME: containerd
      CONCOURSE_TSA_PUBLIC_KEY: /concourse-keys/tsa_host_key.pub
      CONCOURSE_TSA_WORKER_PRIVATE_KEY: /concourse-keys/worker_key
      CONCOURSE_LOG_LEVEL: debug
      CONCOURSE_TSA_HOST: web:2222
      CONCOURSE_BIND_IP: 0.0.0.0
      CONCOURSE_BAGGAGECLAIM_BIND_IP: 0.0.0.0
      CONCOURSE_BAGGAGECLAIM_DRIVER: overlay
      # P2P Multi-Network Config
      CONCOURSE_BAGGAGECLAIM_P2P_INTERFACES: |
        - pattern: "eth1"
          network_segment: "segment2"
          priority: 1
      CONCOURSE_BAGGAGECLAIM_P2P_NETWORK_DETECTION: "auto"

  # Relay worker bridging segment1 and segment2
  worker-relay:
    build: .
    image: concourse/concourse:p2p-multinetwork
    command: worker
    privileged: true
    cgroup: host
    depends_on:
      - web
    networks:
      - mgmt
      - segment1
      - segment2
    volumes:
      - "./hack/keys:/concourse-keys"
      - "/var/run/docker.sock:/var/run/docker.sock"
    stop_signal: SIGUSR2
    environment:
      CONCOURSE_NAME: worker-relay
      CONCOURSE_RUNTIME: containerd
      CONCOURSE_TSA_PUBLIC_KEY: /concourse-keys/tsa_host_key.pub
      CONCOURSE_TSA_WORKER_PRIVATE_KEY: /concourse-keys/worker_key
      CONCOURSE_LOG_LEVEL: debug
      CONCOURSE_TSA_HOST: web:2222
      CONCOURSE_BIND_IP: 0.0.0.0
      CONCOURSE_BAGGAGECLAIM_BIND_IP: 0.0.0.0
      CONCOURSE_BAGGAGECLAIM_DRIVER: overlay
      # P2P Relay Configuration
      CONCOURSE_BAGGAGECLAIM_P2P_RELAY_ENABLED: "true"
      CONCOURSE_BAGGAGECLAIM_P2P_INTERFACES: |
        - pattern: "eth1"
          network_segment: "segment1"
          priority: 1
        - pattern: "eth2"
          network_segment: "segment2"
          priority: 1
      CONCOURSE_BAGGAGECLAIM_P2P_NETWORK_DETECTION: "auto"

  # Isolated worker in segment3 (no relay)
  worker-isolated:
    build: .
    image: concourse/concourse:p2p-multinetwork
    command: worker
    privileged: true
    cgroup: host
    depends_on:
      - web
    networks:
      - mgmt
      - segment3
    volumes:
      - "./hack/keys:/concourse-keys"
      - "/var/run/docker.sock:/var/run/docker.sock"
    stop_signal: SIGUSR2
    environment:
      CONCOURSE_NAME: worker-isolated
      CONCOURSE_RUNTIME: containerd
      CONCOURSE_TSA_PUBLIC_KEY: /concourse-keys/tsa_host_key.pub
      CONCOURSE_TSA_WORKER_PRIVATE_KEY: /concourse-keys/worker_key
      CONCOURSE_LOG_LEVEL: debug
      CONCOURSE_TSA_HOST: web:2222
      CONCOURSE_BIND_IP: 0.0.0.0
      CONCOURSE_BAGGAGECLAIM_BIND_IP: 0.0.0.0
      CONCOURSE_BAGGAGECLAIM_DRIVER: overlay
      # P2P Multi-Network Config
      CONCOURSE_BAGGAGECLAIM_P2P_INTERFACES: |
        - pattern: "eth1"
          network_segment: "segment3"
          priority: 1
      CONCOURSE_BAGGAGECLAIM_P2P_NETWORK_DETECTION: "auto"

  # Monitoring Stack
  prometheus:
    image: prom/prometheus:latest
    networks:
      - mgmt
    ports:
      - "9091:9090"
    volumes:
      - "./monitoring/prometheus.yml:/etc/prometheus/prometheus.yml"
    command:
      - '--config.file=/etc/prometheus/prometheus.yml'
      - '--storage.tsdb.path=/prometheus'
```

### Step 2: Create Test Pipeline

Create `test-pipelines/p2p-streaming-test.yml`:

```yaml
jobs:
- name: test-p2p-streaming
  plan:
  # Create volume on worker-s1
  - task: create-test-data
    tags: [worker-s1]
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: {repository: busybox}
      outputs:
      - name: test-data
      run:
        path: sh
        args:
        - -exc
        - |
          dd if=/dev/urandom of=test-data/large-file.dat bs=1M count=100
          echo "Data created on worker-s1" > test-data/source.txt
          date > test-data/timestamp.txt

  # Force streaming to worker-s2 (different network)
  - task: process-on-different-network
    tags: [worker-s2]
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: {repository: busybox}
      inputs:
      - name: test-data
      outputs:
      - name: processed-data
      run:
        path: sh
        args:
        - -exc
        - |
          echo "Processing on worker-s2" >> test-data/source.txt
          cp -r test-data/* processed-data/
          ls -la processed-data/

  # Test relay through worker-relay
  - task: process-on-isolated
    tags: [worker-isolated]
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: {repository: busybox}
      inputs:
      - name: processed-data
      run:
        path: sh
        args:
        - -exc
        - |
          echo "Final processing on isolated worker"
          cat processed-data/source.txt
          ls -la processed-data/
```

### Step 3: Create Monitoring Configuration

Create `monitoring/prometheus.yml`:

```yaml
global:
  scrape_interval: 15s
  evaluation_interval: 15s

scrape_configs:
  - job_name: 'concourse-web'
    static_configs:
      - targets: ['web:9090']

  - job_name: 'workers'
    static_configs:
      - targets:
        - 'worker-s1:9090'
        - 'worker-s2:9090'
        - 'worker-relay:9090'
        - 'worker-isolated:9090'
```

### Step 4: Create Test Scripts

Create `test-scripts/run-p2p-tests.sh`:

```bash
#!/bin/bash
set -e

echo "=== P2P Multi-Network Integration Tests ==="

# Build custom Concourse image with P2P enhancements
echo "Building Concourse with P2P multi-network support..."
docker build -t concourse/concourse:p2p-multinetwork .

# Start the test environment
echo "Starting multi-network test environment..."
docker-compose -f docker-compose-p2p-multinetwork.yml up -d

# Wait for all workers to register
echo "Waiting for workers to register..."
sleep 45

# Check worker status
echo "Checking worker registration..."
fly -t p2p login -c http://localhost:8080 -u test -p test
fly -t p2p workers

# Run network topology test
echo "Testing network topology discovery..."
curl -s http://localhost:8080/api/v1/workers | jq '.[] | {name: .name, networks: .networks}'

# Set the test pipeline
echo "Setting test pipeline..."
fly -t p2p set-pipeline -p p2p-test -c test-pipelines/p2p-streaming-test.yml -n

# Unpause pipeline
fly -t p2p unpause-pipeline -p p2p-test

# Trigger the job
echo "Triggering P2P streaming test..."
fly -t p2p trigger-job -j p2p-test/test-p2p-streaming -w

# Check metrics
echo "Checking P2P metrics..."
curl -s http://localhost:9090/metrics | grep -E 'p2p_streams_total|p2p_streams_success|p2p_relay_streams|volume_streaming_duration'

# Test connectivity between workers
echo "Testing P2P connectivity..."
docker exec concourse-worker-s1-1 curl -s http://worker-s2:7788/p2p-urls || echo "Expected: No direct connectivity"
docker exec concourse-worker-relay-1 curl -s http://worker-s2:7788/p2p-urls || echo "Relay can reach worker-s2"

echo "=== Tests completed ==="
```

Create `test-scripts/test-failure-scenarios.sh`:

```bash
#!/bin/bash
set -e

echo "=== P2P Failure Scenario Tests ==="

# Test 1: Relay worker failure
echo "Test 1: Simulating relay worker failure..."
docker-compose -f docker-compose-p2p-multinetwork.yml stop worker-relay
sleep 10

echo "Running job with relay worker down..."
fly -t p2p trigger-job -j p2p-test/test-p2p-streaming -w || echo "Expected: Should fallback to ATC streaming"

# Check fallback metrics
curl -s http://localhost:9090/metrics | grep 'volumes_streamed_via_fallback'

# Restart relay worker
docker-compose -f docker-compose-p2p-multinetwork.yml start worker-relay
sleep 20

# Test 2: Network partition
echo "Test 2: Simulating network partition..."
docker network disconnect segment1 concourse-worker-relay-1
fly -t p2p trigger-job -j p2p-test/test-p2p-streaming -w || echo "Expected: Should use alternative route"

# Reconnect network
docker network connect segment1 concourse-worker-relay-1

# Test 3: High load
echo "Test 3: Simulating high load..."
for i in {1..5}; do
  fly -t p2p trigger-job -j p2p-test/test-p2p-streaming &
done
wait

echo "=== Failure tests completed ==="
```

### Step 5: Performance Testing

Create `test-scripts/benchmark-p2p.sh`:

```bash
#!/bin/bash
set -e

echo "=== P2P Performance Benchmarking ==="

# Test different file sizes
for size in 1 10 100 500; do
  echo "Testing with ${size}MB file..."

  # Create test pipeline with specific size
  cat > /tmp/bench-pipeline.yml <<EOF
jobs:
- name: bench-${size}mb
  plan:
  - task: create-data
    tags: [worker-s1]
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: {repository: busybox}
      outputs:
      - name: data
      run:
        path: dd
        args: ["if=/dev/urandom", "of=data/test.dat", "bs=1M", "count=${size}"]

  - task: consume-data
    tags: [worker-s2]
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: {repository: busybox}
      inputs:
      - name: data
      run:
        path: ls
        args: ["-la", "data/"]
EOF

  fly -t p2p set-pipeline -p bench-${size}mb -c /tmp/bench-pipeline.yml -n
  fly -t p2p unpause-pipeline -p bench-${size}mb

  # Measure time
  START=$(date +%s)
  fly -t p2p trigger-job -j bench-${size}mb/bench-${size}mb -w
  END=$(date +%s)

  echo "Time for ${size}MB: $((END-START)) seconds"

  # Collect metrics
  curl -s http://localhost:9090/metrics | grep -E "volume_streaming_duration.*${size}"
done

# Compare P2P vs ATC-mediated streaming
echo "Comparing P2P vs ATC-mediated streaming..."

# Disable P2P temporarily
docker exec concourse-web-1 sh -c "export CONCOURSE_ENABLE_P2P_VOLUME_STREAMING=false"
docker-compose -f docker-compose-p2p-multinetwork.yml restart web
sleep 30

echo "Running with ATC-mediated streaming..."
START=$(date +%s)
fly -t p2p trigger-job -j bench-100mb/bench-100mb -w
END=$(date +%s)
ATC_TIME=$((END-START))

# Re-enable P2P
docker exec concourse-web-1 sh -c "export CONCOURSE_ENABLE_P2P_VOLUME_STREAMING=true"
docker-compose -f docker-compose-p2p-multinetwork.yml restart web
sleep 30

echo "Running with P2P streaming..."
START=$(date +%s)
fly -t p2p trigger-job -j bench-100mb/bench-100mb -w
END=$(date +%s)
P2P_TIME=$((END-START))

echo "Results:"
echo "ATC-mediated: ${ATC_TIME} seconds"
echo "P2P streaming: ${P2P_TIME} seconds"
echo "Improvement: $(( (ATC_TIME - P2P_TIME) * 100 / ATC_TIME ))%"

echo "=== Benchmarking completed ==="
```

### Step 6: Network Analysis

Create `test-scripts/analyze-network.sh`:

```bash
#!/bin/bash

echo "=== Network Traffic Analysis ==="

# Start packet capture on relay worker
echo "Starting packet capture..."
docker exec concourse-worker-relay-1 tcpdump -i any -w /tmp/p2p-traffic.pcap -s 0 'port 7788' &
TCPDUMP_PID=$!

# Run streaming job
fly -t p2p trigger-job -j p2p-test/test-p2p-streaming -w

# Stop capture
sleep 5
kill $TCPDUMP_PID

# Analyze traffic
echo "Analyzing captured traffic..."
docker exec concourse-worker-relay-1 tcpdump -r /tmp/p2p-traffic.pcap -nn | head -20

# Show network routes
echo "Network routes on relay worker:"
docker exec concourse-worker-relay-1 ip route

# Show active connections during streaming
echo "Active P2P connections:"
docker exec concourse-worker-relay-1 netstat -an | grep 7788

echo "=== Analysis completed ==="
```

## Running the Tests

### Complete Test Sequence

```bash
#!/bin/bash
# run-all-tests.sh

set -e

# 1. Setup
./test-scripts/setup-test-env.sh

# 2. Basic P2P tests
./test-scripts/run-p2p-tests.sh

# 3. Failure scenarios
./test-scripts/test-failure-scenarios.sh

# 4. Performance benchmarks
./test-scripts/benchmark-p2p.sh

# 5. Network analysis
./test-scripts/analyze-network.sh

# 6. Cleanup
docker-compose -f docker-compose-p2p-multinetwork.yml down

echo "=== All tests completed successfully ==="
```

## Validation Checklist

### Functional Validation
- [ ] Workers register with correct network segments
- [ ] Network topology is correctly discovered
- [ ] P2P streaming works within same network
- [ ] P2P streaming works across networks via relay
- [ ] Fallback to ATC streaming when P2P fails
- [ ] Isolated workers use ATC streaming

### Performance Validation
- [ ] P2P streaming is faster than ATC-mediated
- [ ] Relay streaming has acceptable overhead
- [ ] Large files stream efficiently
- [ ] Multiple concurrent streams work

### Failure Handling
- [ ] Relay worker failure triggers fallback
- [ ] Network partition handled gracefully
- [ ] Connectivity test failures logged
- [ ] Recovery after transient failures

### Monitoring & Observability
- [ ] Metrics exported correctly
- [ ] Network topology visible in API
- [ ] P2P streaming events logged
- [ ] Performance metrics accurate

## Troubleshooting Guide

### Common Issues

1. **Workers not discovering networks**
   - Check worker logs: `docker logs concourse-worker-s1-1`
   - Verify network interfaces: `docker exec concourse-worker-s1-1 ip addr`
   - Check P2P configuration environment variables

2. **P2P streaming not working**
   - Check connectivity: `docker exec worker1 ping worker2`
   - Verify P2P URLs: `curl http://worker:7788/p2p-urls`
   - Check firewall rules in containers

3. **Relay not working**
   - Verify relay configuration
   - Check network bridges
   - Monitor relay worker logs

4. **Performance issues**
   - Check network bandwidth
   - Verify compression settings
   - Monitor CPU/memory usage

### Debug Commands

```bash
# Check worker network info
fly -t p2p curl /api/v1/workers/worker-s1/networks

# View P2P routing decisions
docker logs concourse-web-1 2>&1 | grep "p2p-routing"

# Monitor live P2P streams
watch 'curl -s http://localhost:9090/metrics | grep p2p_active_streams'

# Check worker connectivity matrix
fly -t p2p curl /api/v1/network-topology

# View detailed streaming logs
docker logs concourse-worker-relay-1 2>&1 | grep -E "stream|p2p"
```

## Metrics to Monitor

### Key Metrics
```
# P2P Streaming Success Rate
p2p_streams_success_total / p2p_streams_total

# Average Streaming Duration
rate(volume_streaming_duration_sum[5m]) / rate(volume_streaming_duration_count[5m])

# Relay Usage
p2p_relay_streams_total / p2p_streams_total

# Fallback Rate
volumes_streamed_via_fallback_total / volumes_streamed_total

# Network Topology Changes
network_topology_changes_total

# Connectivity Test Failures
p2p_connectivity_test_failures_total
```

## Success Criteria

1. **All workers registered**: 5 workers online
2. **Network topology discovered**: Each worker shows correct segments
3. **P2P streaming works**: >90% success rate
4. **Relay functioning**: Cross-network streaming via relay
5. **Fallback working**: ATC streaming when P2P unavailable
6. **Performance improved**: >40% faster than ATC-only
7. **Monitoring active**: All metrics visible
8. **Tests passing**: All test scripts complete successfully

## Next Steps

1. Run basic tests to validate implementation
2. Perform load testing with concurrent jobs
3. Test in production-like environment
4. Document any issues or improvements needed
5. Create automated CI tests for P2P functionality