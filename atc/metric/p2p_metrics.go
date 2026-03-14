package metric

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// P2P Streaming Metrics
var (
	// VolumesStreamedCount tracks the number of volumes streamed with detailed labels
	VolumesStreamedCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "concourse",
			Subsystem: "volumes",
			Name:      "streamed_count",
			Help:      "Total number of volumes streamed between workers",
		},
		[]string{
			"source_worker",
			"destination_worker",
			"streaming_type", // "p2p_direct", "p2p_relay", "atc_mediated"
			"step_type",
			"step_name",
			"pipeline_name",
			"job_name",
			"team_name",
			"network_segment",
			"success", // "true" or "false"
		},
	)

	// VolumesStreamedBytes tracks the total bytes streamed between workers
	VolumesStreamedBytes = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "concourse",
			Subsystem: "volumes",
			Name:      "streamed_bytes",
			Help:      "Total bytes streamed between workers",
		},
		[]string{
			"source_worker",
			"destination_worker",
			"streaming_type",
			"step_type",
			"step_name",
			"pipeline_name",
			"job_name",
			"team_name",
			"network_segment",
		},
	)

	// VolumeStreamingDuration tracks the duration of streaming operations
	VolumeStreamingDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "concourse",
			Subsystem: "volumes",
			Name:      "streaming_duration_seconds",
			Help:      "Duration of volume streaming operations in seconds",
			Buckets:   []float64{0.1, 0.5, 1, 5, 10, 30, 60, 120, 300, 600},
		},
		[]string{
			"source_worker",
			"destination_worker",
			"streaming_type",
			"step_type",
			"pipeline_name",
			"job_name",
		},
	)

	// P2PStreamingAttempts tracks streaming attempts by type
	P2PStreamingAttempts = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "concourse",
			Subsystem: "p2p",
			Name:      "streaming_attempts_total",
			Help:      "Total number of P2P streaming attempts",
		},
		[]string{
			"source_worker",
			"destination_worker",
			"attempt_type", // "direct", "relay", "fallback_to_atc"
			"network_segment",
		},
	)

	// P2PStreamingSuccess tracks successful P2P streams
	P2PStreamingSuccess = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "concourse",
			Subsystem: "p2p",
			Name:      "streaming_success_total",
			Help:      "Total number of successful P2P streaming operations",
		},
		[]string{
			"source_worker",
			"destination_worker",
			"streaming_type",
			"network_segment",
		},
	)

	// P2PStreamingFailures tracks failed P2P streams
	P2PStreamingFailures = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "concourse",
			Subsystem: "p2p",
			Name:      "streaming_failures_total",
			Help:      "Total number of failed P2P streaming operations",
		},
		[]string{
			"source_worker",
			"destination_worker",
			"failure_reason", // "connectivity", "timeout", "relay_unavailable", "network_error"
			"network_segment",
		},
	)

	// P2PRelayStreams tracks streams that used relay workers
	P2PRelayStreams = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "concourse",
			Subsystem: "p2p",
			Name:      "relay_streams_total",
			Help:      "Total number of streams that used relay workers",
		},
		[]string{
			"source_worker",
			"destination_worker",
			"relay_worker",
			"source_segment",
			"destination_segment",
		},
	)

	// P2PConnectivityTests tracks connectivity test results
	P2PConnectivityTests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "concourse",
			Subsystem: "p2p",
			Name:      "connectivity_tests_total",
			Help:      "Total number of P2P connectivity tests performed",
		},
		[]string{
			"source_worker",
			"destination_worker",
			"result", // "success", "failure"
			"network_segment",
		},
	)

	// P2PConnectivityLatency tracks connectivity test latency
	P2PConnectivityLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "concourse",
			Subsystem: "p2p",
			Name:      "connectivity_latency_milliseconds",
			Help:      "Latency of P2P connectivity tests in milliseconds",
			Buckets:   []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000},
		},
		[]string{
			"source_worker",
			"destination_worker",
			"network_segment",
		},
	)

	// NetworkTopologyChanges tracks changes to network topology
	NetworkTopologyChanges = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: "concourse",
			Subsystem: "p2p",
			Name:      "network_topology_changes_total",
			Help:      "Total number of network topology changes detected",
		},
	)

	// WorkerNetworkSegments tracks the number of network segments per worker
	WorkerNetworkSegments = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "concourse",
			Subsystem: "p2p",
			Name:      "worker_network_segments",
			Help:      "Number of network segments each worker belongs to",
		},
		[]string{
			"worker_name",
			"is_relay_capable",
		},
	)

	// ActiveP2PStreams tracks currently active P2P streams
	ActiveP2PStreams = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "concourse",
			Subsystem: "p2p",
			Name:      "active_streams",
			Help:      "Number of currently active P2P streams",
		},
		[]string{
			"source_worker",
			"destination_worker",
			"streaming_type",
		},
	)

	// P2PBandwidthUtilization tracks bandwidth utilization
	P2PBandwidthUtilization = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "concourse",
			Subsystem: "p2p",
			Name:      "bandwidth_utilization_mbps",
			Help:      "Current bandwidth utilization in Mbps for P2P streaming",
		},
		[]string{
			"source_worker",
			"destination_worker",
			"network_segment",
		},
	)

	// VolumeStreamingQueueSize tracks the queue size for pending streams
	VolumeStreamingQueueSize = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "concourse",
			Subsystem: "volumes",
			Name:      "streaming_queue_size",
			Help:      "Number of volumes queued for streaming",
		},
		[]string{
			"worker_name",
			"queue_type", // "outbound", "inbound"
		},
	)
)

// P2PStreamingLabels contains labels for P2P streaming metrics
type P2PStreamingLabels struct {
	SourceWorker      string
	DestinationWorker string
	StreamingType     string
	StepType          string
	StepName          string
	PipelineName      string
	JobName           string
	TeamName          string
	NetworkSegment    string
}

// RecordVolumeStreamed records a volume streaming event with all labels
func RecordVolumeStreamed(labels P2PStreamingLabels, sizeBytes int64, duration time.Duration, success bool) {
	successStr := "false"
	if success {
		successStr = "true"
	}

	// Record count
	VolumesStreamedCount.WithLabelValues(
		labels.SourceWorker,
		labels.DestinationWorker,
		labels.StreamingType,
		labels.StepType,
		labels.StepName,
		labels.PipelineName,
		labels.JobName,
		labels.TeamName,
		labels.NetworkSegment,
		successStr,
	).Inc()

	// Record bytes (only if successful)
	if success && sizeBytes > 0 {
		VolumesStreamedBytes.WithLabelValues(
			labels.SourceWorker,
			labels.DestinationWorker,
			labels.StreamingType,
			labels.StepType,
			labels.StepName,
			labels.PipelineName,
			labels.JobName,
			labels.TeamName,
			labels.NetworkSegment,
		).Add(float64(sizeBytes))
	}

	// Record duration
	VolumeStreamingDuration.WithLabelValues(
		labels.SourceWorker,
		labels.DestinationWorker,
		labels.StreamingType,
		labels.StepType,
		labels.PipelineName,
		labels.JobName,
	).Observe(duration.Seconds())
}

// StreamingTypeP2PDirect indicates direct P2P streaming
const StreamingTypeP2PDirect = "p2p_direct"

// StreamingTypeP2PRelay indicates P2P streaming through a relay
const StreamingTypeP2PRelay = "p2p_relay"

// StreamingTypeATCMediated indicates streaming through ATC
const StreamingTypeATCMediated = "atc_mediated"

func init() {
	prometheus.MustRegister(
		VolumesStreamedCount,
		VolumesStreamedBytes,
		VolumeStreamingDuration,
		P2PStreamingAttempts,
		P2PStreamingSuccess,
		P2PStreamingFailures,
		P2PRelayStreams,
		P2PConnectivityTests,
		P2PConnectivityLatency,
		NetworkTopologyChanges,
		WorkerNetworkSegments,
		ActiveP2PStreams,
		P2PBandwidthUtilization,
		VolumeStreamingQueueSize,
	)
}