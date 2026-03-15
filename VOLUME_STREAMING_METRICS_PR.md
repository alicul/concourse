# PR #0: Comprehensive Volume Streaming Metrics

## Summary

This PR adds comprehensive observability for volume streaming operations in Concourse. The existing metrics were limited to a simple counter, providing no insight into streaming performance, success rates, or method effectiveness. This PR addresses these gaps by adding detailed metrics that enable operators to monitor and optimize volume streaming.

## Problem Statement

Current issues with volume streaming observability:
- Only a basic counter (`volumes_streamed`) exists
- No visibility into P2P vs ATC streaming performance
- Missing `volumesStreamedViaFallback` metric in Prometheus (bug)
- No latency/duration metrics
- No success/failure rates
- No volume size tracking
- Cannot determine P2P effectiveness

## Changes

### 1. Fixed Missing Metric
- Added `volumesStreamedViaFallback` to Prometheus emitter (was tracked but not exposed)

### 2. New Metrics Added

#### Counters
- `concourse_volumes_streaming_p2p_success_total` - Successful P2P streams
- `concourse_volumes_streaming_p2p_failure_total` - Failed P2P streams
- `concourse_volumes_streaming_atc_success_total` - Successful ATC streams
- `concourse_volumes_streaming_atc_failure_total` - Failed ATC streams

#### Histograms
- `concourse_volumes_streaming_duration_seconds{method,status}` - Stream duration by method and status
- `concourse_volumes_streaming_size_bytes` - Volume size distribution

#### Gauges
- `concourse_volumes_streaming_in_progress{method}` - Currently active streams

### 3. Streamer Instrumentation
- Updated `worker/streamer.go` to emit all new metrics
- Added timing measurements for both P2P and ATC streaming
- Track success/failure for each method separately
- Emit volume size when available

### 4. Grafana Dashboard
- Created comprehensive dashboard (`monitoring/dashboards/volume-streaming.json`)
- Visualizes P2P effectiveness, latency percentiles, failure rates
- Shows streaming method distribution and volume size patterns

## Benefits

### Immediate Value (with existing P2P implementation)
1. **P2P Effectiveness Monitoring**
   - Track P2P success rate vs fallback rate
   - Identify which worker pairs have connectivity issues
   - Measure actual P2P vs ATC performance difference

2. **Performance Analysis**
   - p50/p95/p99 latencies for both methods
   - Volume size impact on streaming time
   - Identify performance bottlenecks

3. **Operational Insights**
   - Real-time streaming health monitoring
   - Failure pattern detection
   - Worker load distribution visibility

4. **Capacity Planning**
   - Understand volume size distributions
   - Track streaming rate trends
   - Identify high-load workers

### Foundation for Multi-Network P2P
These metrics provide the baseline measurements needed to:
- Validate multi-network improvements
- Compare performance across network topologies
- Track relay worker effectiveness (future)
- Monitor network segment performance (future)

## Testing

### Manual Testing
```bash
# Start Concourse with P2P enabled
export CONCOURSE_ENABLE_P2P_VOLUME_STREAMING=true

# Run jobs that stream volumes between workers
fly -t dev execute -c task-with-outputs.yml

# Check metrics endpoint
curl http://localhost:9090/metrics | grep concourse_volumes_streaming

# View in Grafana
# Import dashboard from monitoring/dashboards/volume-streaming.json
```

### Metrics Validation
- Verified all counters increment correctly
- Confirmed histogram buckets capture typical durations
- Tested gauge increment/decrement for in-progress streams
- Validated fallback metric now appears in Prometheus

## Performance Impact
- Minimal overhead: Simple counter increments and timestamp captures
- Histogram observations are efficient Prometheus operations
- No impact on streaming performance itself

## Migration Notes
- Fully backward compatible
- No configuration changes required
- Existing `volumes_streamed` metric unchanged
- New metrics start at zero, no historical data migration needed

## Next Steps

### Immediate Follow-ups
1. Add integration tests for new metrics
2. Update documentation with metric descriptions
3. Create alerts based on failure rates and latencies

### Future Enhancements (separate PRs)
1. **PR #1**: Network topology discovery
2. **PR #2**: Multi-network P2P routing
3. **PR #3**: Relay worker support
4. **PR #4**: Enhanced documentation

## Dashboard Preview

Key panels in the Grafana dashboard:
- **P2P Success Rate**: Gauge showing current P2P effectiveness
- **Streaming Method Distribution**: Pie chart of P2P vs ATC vs Fallback
- **Duration Percentiles**: Time series comparing P2P and ATC latencies
- **Failure Rates**: Trends for each streaming method
- **Volume Size Distribution**: Histogram of streamed volume sizes
- **Top Workers**: Table of workers by streaming volume

## Review Checklist
- [ ] Code compiles without warnings
- [ ] Metrics appear in `/metrics` endpoint
- [ ] Grafana dashboard loads correctly
- [ ] No performance regression in streaming
- [ ] Backward compatibility maintained

## Files Changed
- `atc/metric/emit.go` - Added new metric definitions
- `atc/metric/emitter/prometheus.go` - Prometheus emitter updates
- `atc/worker/streamer.go` - Instrumentation for metrics
- `monitoring/dashboards/volume-streaming.json` - New Grafana dashboard

## Commits
1. Add comprehensive volume streaming metrics
2. Add Grafana dashboard for volume streaming metrics

---

This PR provides immediate value for monitoring the existing P2P implementation while laying the groundwork for comprehensive observability of the planned multi-network enhancements.