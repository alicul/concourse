# Migration Guide: Single to Multi-Network P2P Volume Streaming

## Overview
This guide provides a step-by-step process for migrating from Concourse's single-network P2P volume streaming to the enhanced multi-network implementation with relay support.

## Migration Timeline
- **Phase 1 (Week 1-2)**: Assessment and Planning
- **Phase 2 (Week 3-4)**: Infrastructure Preparation
- **Phase 3 (Week 5-6)**: Gradual Rollout
- **Phase 4 (Week 7-8)**: Full Migration and Optimization

## Pre-Migration Checklist

### Technical Requirements
- [ ] Concourse version 7.8.0 or higher
- [ ] Prometheus monitoring configured
- [ ] Grafana dashboards accessible
- [ ] Network topology documented
- [ ] Firewall rules reviewed
- [ ] Backup procedures tested

### Operational Requirements
- [ ] Migration team identified
- [ ] Rollback plan documented
- [ ] Maintenance windows scheduled
- [ ] Communication plan ready
- [ ] Success criteria defined

## Phase 1: Assessment and Planning

### 1.1 Current State Analysis

```bash
#!/bin/bash
# assess-current-p2p.sh

echo "=== Current P2P Configuration Assessment ==="

# Check current P2P status
echo -e "\n1. P2P Enabled Workers:"
fly -t main workers --json | jq '.[] | select(.p2p_enabled == true) | {name, version, p2p_urls}'

# Measure current performance
echo -e "\n2. Current P2P Performance:"
curl -s http://prometheus:9090/api/v1/query \
  -d 'query=sum(rate(concourse_volumes_streaming_p2p_success_total[24h])) / sum(rate(concourse_volumes_streaming_total[24h])) * 100' \
  | jq -r '.data.result[0].value[1]' | xargs printf "P2P Success Rate: %.2f%%\n"

# Check network topology
echo -e "\n3. Worker Network Distribution:"
fly -t main workers --json | jq -r '.[].addr' | cut -d: -f1 | xargs -I {} sh -c 'echo -n "{}: "; dig +short -x {} | head -1'

# Identify cross-network transfers
echo -e "\n4. Cross-Network Volume Streaming:"
# Query logs for cross-subnet streaming patterns
grep "volume stream" /var/log/concourse/atc.log | tail -1000 | \
  awk '{print $5, $7}' | sort | uniq -c | sort -rn | head -10

# Document findings
cat > migration-assessment.json <<EOF
{
  "date": "$(date -Iseconds)",
  "workers": $(fly -t main workers --json | jq length),
  "p2p_enabled": $(fly -t main workers --json | jq '[.[] | select(.p2p_enabled == true)] | length'),
  "networks": $(fly -t main workers --json | jq -r '.[].addr' | cut -d. -f1-3 | sort -u | wc -l),
  "p2p_success_rate": $(curl -s http://prometheus:9090/api/v1/query -d 'query=sum(rate(concourse_volumes_streaming_p2p_success_total[24h])) / sum(rate(concourse_volumes_streaming_total[24h]))' | jq -r '.data.result[0].value[1]')
}
EOF

echo -e "\nAssessment saved to migration-assessment.json"
```

### 1.2 Network Topology Mapping

```bash
#!/bin/bash
# map-network-topology.sh

echo "Mapping Network Topology..."

# Create network map
cat > network-topology.yaml <<EOF
network_segments:
  - name: datacenter-1
    cidr: 10.0.0.0/16
    type: private
    location: us-east-1
    workers: []

  - name: datacenter-2
    cidr: 10.1.0.0/16
    type: private
    location: us-west-2
    workers: []

  - name: cloud-vpc
    cidr: 172.16.0.0/12
    type: cloud
    location: aws
    workers: []

  - name: kubernetes-cluster
    cidr: 100.64.0.0/10
    type: overlay
    location: k8s
    workers: []

connectivity:
  - from: datacenter-1
    to: datacenter-2
    type: vpn
    latency_ms: 50

  - from: datacenter-1
    to: cloud-vpc
    type: direct-connect
    latency_ms: 10

  - from: kubernetes-cluster
    to: cloud-vpc
    type: peering
    latency_ms: 5

relay_requirements:
  - between: [datacenter-1, kubernetes-cluster]
    reason: no_direct_route

  - between: [datacenter-2, kubernetes-cluster]
    reason: firewall_restrictions
EOF

# Map workers to segments
for worker in $(fly -t main workers --json | jq -r '.[].name'); do
  IP=$(fly -t main workers --json | jq -r ".[] | select(.name == \"$worker\") | .addr" | cut -d: -f1)

  # Determine network segment based on IP
  if [[ $IP =~ ^10\.0\. ]]; then
    SEGMENT="datacenter-1"
  elif [[ $IP =~ ^10\.1\. ]]; then
    SEGMENT="datacenter-2"
  elif [[ $IP =~ ^172\.(1[6-9]|2[0-9]|3[01])\. ]]; then
    SEGMENT="cloud-vpc"
  elif [[ $IP =~ ^100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\. ]]; then
    SEGMENT="kubernetes-cluster"
  else
    SEGMENT="unknown"
  fi

  echo "Worker $worker ($IP) -> Network Segment: $SEGMENT"
done
```

### 1.3 Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| P2P connectivity failures | Medium | High | Deploy relay workers, maintain ATC fallback |
| Performance degradation | Low | Medium | Gradual rollout, monitoring |
| Network topology changes | Medium | Low | Automated detection, dynamic routing |
| Relay worker failures | Low | Medium | Multiple relay workers, auto-failover |
| Configuration errors | Medium | High | Validation scripts, staged rollout |

## Phase 2: Infrastructure Preparation

### 2.1 Deploy Monitoring Infrastructure

```bash
#!/bin/bash
# setup-monitoring.sh

echo "Setting up Multi-Network P2P Monitoring..."

# 1. Import Grafana dashboards
for dashboard in multi-network-p2p.json relay-worker-operations.json; do
  curl -X POST http://grafana.example.com/api/dashboards/db \
    -H "Authorization: Bearer $GRAFANA_API_KEY" \
    -H "Content-Type: application/json" \
    -d @monitoring/dashboards/$dashboard
done

# 2. Configure Prometheus alerts
kubectl apply -f monitoring/alerts/p2p-streaming-alerts.yml

# 3. Set up alert routing
cat > alertmanager-routes.yml <<EOF
route:
  group_by: ['alertname', 'cluster', 'service']
  group_wait: 10s
  group_interval: 10s
  repeat_interval: 12h
  receiver: 'platform-team'

  routes:
  - match:
      component: p2p
    receiver: p2p-team

  - match:
      component: relay
    receiver: relay-oncall

receivers:
- name: 'p2p-team'
  slack_configs:
  - channel: '#p2p-alerts'

- name: 'relay-oncall'
  pagerduty_configs:
  - service_key: 'relay-service-key'
EOF

kubectl apply -f alertmanager-routes.yml
```

### 2.2 Prepare Relay Worker Infrastructure

```bash
#!/bin/bash
# prepare-relay-infrastructure.sh

echo "Preparing Relay Worker Infrastructure..."

# 1. Create dedicated relay worker nodes (AWS example)
aws ec2 run-instances \
  --image-id ami-0c55b159cbafe1f0 \
  --instance-type c5.2xlarge \
  --count 3 \
  --key-name concourse-key \
  --security-group-ids sg-relay-workers \
  --subnet-id subnet-datacenter-1 \
  --tag-specifications 'ResourceType=instance,Tags=[{Key=Name,Value=relay-worker},{Key=Role,Value=relay}]' \
  --user-data file://relay-worker-init.sh

# 2. Configure network interfaces for multi-network access
for instance in $(aws ec2 describe-instances --filters "Name=tag:Role,Values=relay" --query 'Reservations[].Instances[].InstanceId' --output text); do
  # Attach secondary network interface
  ENI_ID=$(aws ec2 create-network-interface --subnet-id subnet-datacenter-2 --query 'NetworkInterface.NetworkInterfaceId' --output text)
  aws ec2 attach-network-interface --network-interface-id $ENI_ID --instance-id $instance --device-index 1
done

# 3. Configure load balancer for relay workers
cat > relay-lb.yaml <<EOF
apiVersion: v1
kind: Service
metadata:
  name: relay-workers-lb
  namespace: concourse
spec:
  type: LoadBalancer
  selector:
    role: relay
  ports:
  - port: 8080
    targetPort: 8080
    name: relay
EOF

kubectl apply -f relay-lb.yaml
```

### 2.3 Update Worker Configurations

```yaml
# worker-config-template.yml
# Template for updating worker configurations

# For standard workers
CONCOURSE_BAGGAGECLAIM_P2P_ENABLED: "true"
CONCOURSE_BAGGAGECLAIM_P2P_INTERFACES: "${P2P_INTERFACES}"
CONCOURSE_BAGGAGECLAIM_P2P_PORT_RANGE: "7000-7100"

# Network detection
CONCOURSE_WORKER_NETWORK_PRIVATE_PATTERNS: "10.0.0.0/8,172.16.0.0/12,192.168.0.0/16"
CONCOURSE_WORKER_NETWORK_OVERLAY_PATTERNS: "100.64.0.0/10"
CONCOURSE_WORKER_NETWORK_REPORT_INTERVAL: "60s"

# For relay workers (additional)
CONCOURSE_WORKER_RELAY_ENABLED: "true"
CONCOURSE_WORKER_RELAY_MAX_CONNECTIONS: "200"
CONCOURSE_WORKER_RELAY_BANDWIDTH_LIMIT: "10GB/s"
```

## Phase 3: Gradual Rollout

### 3.1 Stage 1: Enable Monitoring Only

```bash
#!/bin/bash
# stage1-monitoring-only.sh

echo "Stage 1: Enabling monitoring without changing behavior"

# Enable metrics collection
for worker in $(fly -t main workers --json | jq -r '.[].name'); do
  echo "Updating $worker with monitoring..."

  # Add monitoring environment variables
  kubectl set env deployment/$worker \
    CONCOURSE_ENABLE_P2P_METRICS=true \
    CONCOURSE_NETWORK_TOPOLOGY_DETECTION=true \
    CONCOURSE_P2P_DRY_RUN=true  # Don't actually use multi-network yet
done

# Verify metrics are being collected
sleep 60
curl -s http://prometheus:9090/api/v1/query \
  -d 'query=concourse_network_segments_discovered' | jq '.data.result'
```

### 3.2 Stage 2: Deploy Relay Workers

```bash
#!/bin/bash
# stage2-deploy-relays.sh

echo "Stage 2: Deploying relay workers"

# Deploy relay workers (3 for redundancy)
for i in {1..3}; do
  cat > relay-worker-$i.yaml <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: relay-worker-$i
  namespace: concourse
spec:
  replicas: 1
  selector:
    matchLabels:
      app: relay-worker-$i
  template:
    metadata:
      labels:
        app: relay-worker-$i
        role: relay
    spec:
      containers:
      - name: worker
        image: concourse/concourse:7.9.0
        env:
        - name: CONCOURSE_WORKER_NAME
          value: "relay-worker-$i"
        - name: CONCOURSE_WORKER_RELAY_ENABLED
          value: "true"
        # ... additional config
EOF

  kubectl apply -f relay-worker-$i.yaml
done

# Wait for relay workers to register
for i in {1..30}; do
  RELAY_COUNT=$(fly -t main workers --json | jq '[.[] | select(.relay == true)] | length')
  if [ $RELAY_COUNT -ge 3 ]; then
    echo "All relay workers registered"
    break
  fi
  sleep 10
done

# Test relay connectivity
for worker in relay-worker-{1..3}; do
  curl -f http://$worker:8080/health || echo "WARNING: $worker health check failed"
done
```

### 3.3 Stage 3: Enable for Test Workloads

```bash
#!/bin/bash
# stage3-test-workloads.sh

echo "Stage 3: Enable multi-network P2P for test workloads"

# Create test team with multi-network enabled
fly -t main set-team -n p2p-test \
  --local-user=admin

# Configure test workers
TEST_WORKERS=(test-worker-1 test-worker-2 test-worker-3)

for worker in "${TEST_WORKERS[@]}"; do
  kubectl set env deployment/$worker \
    CONCOURSE_P2P_DRY_RUN=false \
    CONCOURSE_P2P_MULTI_NETWORK_ENABLED=true \
    CONCOURSE_P2P_RELAY_ENABLED=true
done

# Run test pipelines
fly -t main set-pipeline -p p2p-test-pipeline \
  -c test-pipelines/p2p-validation.yml \
  -t p2p-test

fly -t main unpause-pipeline -p p2p-test-pipeline
fly -t main trigger-job -j p2p-test-pipeline/validate-p2p

# Monitor test results
watch -n 5 'fly -t main watch -j p2p-test-pipeline/validate-p2p'
```

### 3.4 Stage 4: Progressive Rollout

```bash
#!/bin/bash
# stage4-progressive-rollout.sh

WORKER_GROUPS=(
  "10%:critical-workers"
  "25%:production-workers"
  "50%:staging-workers"
  "100%:all-workers"
)

for group in "${WORKER_GROUPS[@]}"; do
  PERCENTAGE=$(echo $group | cut -d: -f1)
  TAG=$(echo $group | cut -d: -f2)

  echo "Rolling out to $PERCENTAGE of workers (tag: $TAG)"

  # Get workers with tag
  WORKERS=$(fly -t main workers --json | jq -r ".[] | select(.tags[] == \"$TAG\") | .name")

  # Calculate how many to update
  TOTAL=$(echo "$WORKERS" | wc -l)
  TO_UPDATE=$(echo "($TOTAL * ${PERCENTAGE%\%}) / 100" | bc)

  # Update workers
  echo "$WORKERS" | head -n $TO_UPDATE | while read worker; do
    echo "Enabling multi-network P2P for $worker"
    kubectl set env deployment/$worker \
      CONCOURSE_P2P_MULTI_NETWORK_ENABLED=true \
      CONCOURSE_P2P_RELAY_ENABLED=true
  done

  # Monitor for issues
  echo "Monitoring for 1 hour..."
  ./monitor-rollout.sh 3600

  # Check success criteria
  SUCCESS_RATE=$(curl -s http://prometheus:9090/api/v1/query \
    -d 'query=sum(rate(concourse_volumes_streaming_p2p_success_total[1h])) / sum(rate(concourse_volumes_streaming_total[1h]))' \
    | jq -r '.data.result[0].value[1]')

  if (( $(echo "$SUCCESS_RATE < 0.6" | bc -l) )); then
    echo "ERROR: P2P success rate too low ($SUCCESS_RATE). Halting rollout."
    exit 1
  fi

  echo "Stage successful. P2P success rate: $SUCCESS_RATE"
done
```

## Phase 4: Full Migration and Optimization

### 4.1 Complete Migration

```bash
#!/bin/bash
# complete-migration.sh

echo "Completing multi-network P2P migration..."

# Enable for all remaining workers
fly -t main workers --json | jq -r '.[].name' | while read worker; do
  kubectl set env deployment/$worker \
    CONCOURSE_P2P_MULTI_NETWORK_ENABLED=true \
    CONCOURSE_P2P_RELAY_ENABLED=true \
    CONCOURSE_P2P_ROUTING_STRATEGY=latency
done

# Update ATC configuration
kubectl set env deployment/concourse-web \
  CONCOURSE_P2P_VOLUME_STREAMING_ENABLED=true \
  CONCOURSE_P2P_MULTI_NETWORK_ENABLED=true \
  CONCOURSE_P2P_RELAY_ENABLED=true \
  CONCOURSE_RELAY_LOAD_BALANCING_STRATEGY=latency-based

# Restart ATC to apply changes
kubectl rollout restart deployment/concourse-web -n concourse
kubectl rollout status deployment/concourse-web -n concourse
```

### 4.2 Performance Optimization

```bash
#!/bin/bash
# optimize-performance.sh

echo "Optimizing multi-network P2P performance..."

# Analyze current performance
METRICS=$(curl -s http://prometheus:9090/api/v1/query_range \
  -d 'query=histogram_quantile(0.95, sum(rate(concourse_volumes_streaming_duration_seconds_bucket[24h])) by (method, le))' \
  -d "start=$(date -d '24 hours ago' +%s)" \
  -d "end=$(date +%s)" \
  -d 'step=3600')

# Adjust based on metrics
P2P_P95=$(echo $METRICS | jq '.data.result[] | select(.metric.method == "p2p") | .values[-1][1]' | xargs printf "%.2f")
RELAY_P95=$(echo $METRICS | jq '.data.result[] | select(.metric.method == "relay") | .values[-1][1]' | xargs printf "%.2f")

if (( $(echo "$P2P_P95 > 10" | bc -l) )); then
  echo "High P2P latency detected. Adjusting settings..."

  # Increase cache TTL
  kubectl set env deployment/concourse-web \
    CONCOURSE_P2P_ROUTE_CACHE_TTL=10m

  # Optimize buffer sizes
  for worker in $(fly -t main workers --json | jq -r '.[].name'); do
    kubectl set env deployment/$worker \
      CONCOURSE_P2P_STREAM_BUFFER_SIZE=256KB
  done
fi

if (( $(echo "$RELAY_P95 > 30" | bc -l) )); then
  echo "High relay latency detected. Scaling relay workers..."
  kubectl scale deployment relay-workers --replicas=5
fi
```

### 4.3 Validation and Testing

```bash
#!/bin/bash
# validate-migration.sh

echo "=== Migration Validation ==="

# 1. Functional tests
echo "Running functional tests..."
fly -t main execute -c validation-tests/p2p-functional.yml

# 2. Performance tests
echo "Running performance benchmarks..."
./benchmark-p2p.sh > benchmark-results.txt

# 3. Failure scenarios
echo "Testing failure scenarios..."

# Test relay failure
kubectl scale deployment relay-workers --replicas=0
sleep 30
P2P_FALLBACK=$(curl -s http://prometheus:9090/api/v1/query \
  -d 'query=rate(concourse_volumes_streaming_atc_success_total[1m])' \
  | jq -r '.data.result[0].value[1]')

if (( $(echo "$P2P_FALLBACK > 0" | bc -l) )); then
  echo "✓ Fallback to ATC working"
else
  echo "✗ Fallback to ATC not working!"
fi

# Restore relay workers
kubectl scale deployment relay-workers --replicas=3

# 4. Generate report
cat > migration-report.md <<EOF
# Migration Report

## Summary
- Migration Date: $(date)
- Workers Migrated: $(fly -t main workers --json | jq length)
- Relay Workers: $(fly -t main workers --json | jq '[.[] | select(.relay == true)] | length')
- Network Segments: $(curl -s http://atc.example.com/api/v1/network-topology | jq '.segments | length')

## Performance Metrics
- P2P Success Rate: $P2P_SUCCESS_RATE%
- P2P p95 Latency: ${P2P_P95}s
- Relay p95 Latency: ${RELAY_P95}s

## Test Results
$(cat benchmark-results.txt)

## Issues Encountered
- None (or list issues)

## Recommendations
- Monitor metrics for 48 hours
- Tune relay worker placement
- Consider adding more relay workers for redundancy
EOF

echo "Validation complete. Report saved to migration-report.md"
```

## Rollback Procedures

### Immediate Rollback

```bash
#!/bin/bash
# emergency-rollback.sh

echo "EMERGENCY: Rolling back to single-network P2P"

# 1. Disable multi-network features
kubectl set env deployment/concourse-web \
  CONCOURSE_P2P_MULTI_NETWORK_ENABLED=false \
  CONCOURSE_P2P_RELAY_ENABLED=false

# 2. Disable on all workers
fly -t main workers --json | jq -r '.[].name' | while read worker; do
  kubectl set env deployment/$worker \
    CONCOURSE_P2P_MULTI_NETWORK_ENABLED=false \
    CONCOURSE_P2P_RELAY_ENABLED=false
done

# 3. Stop relay workers
kubectl scale deployment relay-workers --replicas=0

# 4. Clear route cache
curl -X DELETE http://atc.example.com/api/v1/p2p-route-cache

echo "Rollback complete. System reverted to single-network P2P."
```

### Gradual Rollback

```bash
#!/bin/bash
# gradual-rollback.sh

# Rollback in reverse order of deployment
ROLLBACK_STAGES=(
  "100%:Disable for new deployments"
  "50%:Rollback half of workers"
  "25%:Keep only critical workers"
  "0%:Complete rollback"
)

for stage in "${ROLLBACK_STAGES[@]}"; do
  PERCENTAGE=$(echo $stage | cut -d: -f1)
  DESCRIPTION=$(echo $stage | cut -d: -f2)

  echo "Rollback Stage: $DESCRIPTION ($PERCENTAGE remaining)"

  # Calculate workers to keep multi-network
  TOTAL=$(fly -t main workers --json | jq length)
  KEEP=$((TOTAL * ${PERCENTAGE%\%} / 100))

  # Disable for workers beyond threshold
  fly -t main workers --json | jq -r '.[].name' | tail -n +$((KEEP + 1)) | while read worker; do
    kubectl set env deployment/$worker \
      CONCOURSE_P2P_MULTI_NETWORK_ENABLED=false
  done

  # Monitor stability
  sleep 300
done
```

## Post-Migration Tasks

### 1. Documentation Updates

```markdown
# Update team documentation with:

## New Operational Procedures
- Multi-network P2P monitoring dashboards
- Relay worker management
- Network topology updates

## Configuration Changes
- Worker environment variables
- ATC configuration parameters
- Firewall rules for P2P ports

## Support Procedures
- Troubleshooting guide location
- Alert response procedures
- Escalation paths
```

### 2. Training Sessions

```yaml
training_schedule:
  - session: "Multi-Network P2P Overview"
    audience: "All Engineers"
    duration: "1 hour"
    topics:
      - Architecture overview
      - Benefits and limitations
      - Common issues

  - session: "Relay Worker Operations"
    audience: "Platform Team"
    duration: "2 hours"
    topics:
      - Deployment procedures
      - Monitoring and alerts
      - Troubleshooting

  - session: "Performance Tuning"
    audience: "SRE Team"
    duration: "2 hours"
    topics:
      - Metrics interpretation
      - Optimization techniques
      - Capacity planning
```

### 3. Continuous Improvement

```bash
#!/bin/bash
# setup-improvement-tracking.sh

# Create improvement tracking dashboard
cat > improvement-metrics.json <<EOF
{
  "metrics": [
    {
      "name": "p2p_success_rate",
      "baseline": "$BASELINE_SUCCESS_RATE",
      "target": "85%",
      "query": "sum(rate(concourse_volumes_streaming_p2p_success_total[24h])) / sum(rate(concourse_volumes_streaming_total[24h]))"
    },
    {
      "name": "relay_utilization",
      "baseline": "0%",
      "target": "40-60%",
      "query": "avg(concourse_relay_capacity_used / concourse_relay_capacity_available)"
    },
    {
      "name": "atc_offload_rate",
      "baseline": "$BASELINE_ATC_RATE",
      "target": "<20%",
      "query": "sum(rate(concourse_volumes_streaming_atc_success_total[24h])) / sum(rate(concourse_volumes_streaming_total[24h]))"
    }
  ]
}
EOF

# Schedule weekly review
crontab -l | { cat; echo "0 9 * * 1 /opt/concourse/scripts/weekly-p2p-review.sh"; } | crontab -
```

## Success Criteria

### Immediate Success (Week 1)
- ✓ All workers reporting network topology
- ✓ Relay workers deployed and healthy
- ✓ No increase in streaming failures
- ✓ Monitoring dashboards functional

### Short-term Success (Month 1)
- ✓ P2P success rate > 70%
- ✓ Relay utilization 30-70%
- ✓ ATC streaming reduced by 50%
- ✓ No critical incidents

### Long-term Success (Quarter 1)
- ✓ P2P success rate > 80%
- ✓ p95 latency improved by 30%
- ✓ Infrastructure costs reduced
- ✓ Team trained and confident

## Appendix

### A. Configuration Reference
[See worker configuration template above]

### B. Troubleshooting Quick Reference
- Low P2P success rate: Check connectivity matrix
- High relay latency: Scale relay workers
- Network topology unstable: Review network detection patterns
- Fallback to ATC high: Verify P2P ports open

### C. Support Contacts
- Migration Team: #p2p-migration slack
- On-call: p2p-oncall@example.com
- Documentation: docs/multi-network-p2p/