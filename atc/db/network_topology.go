package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/lib/pq"
)

// NetworkSegmentType represents the type of network segment
type NetworkSegmentType string

const (
	NetworkSegmentPrivate NetworkSegmentType = "private"
	NetworkSegmentPublic  NetworkSegmentType = "public"
	NetworkSegmentOverlay NetworkSegmentType = "overlay"
)

// NetworkSegment represents a network segment in the cluster
type NetworkSegment struct {
	ID       string             `json:"id"`
	CIDR     string             `json:"cidr"`
	Gateway  string             `json:"gateway,omitempty"`
	Type     NetworkSegmentType `json:"type"`
	Priority int                `json:"priority"`
}

// WorkerNetworkInfo represents network information for a worker
type WorkerNetworkInfo struct {
	WorkerName      string          `json:"worker_name"`
	NetworkSegment  NetworkSegment  `json:"network_segment"`
	InterfaceName   string          `json:"interface_name"`
	IPAddress       string          `json:"ip_address"`
	Port            int             `json:"port"`
	Bandwidth       string          `json:"bandwidth,omitempty"`
	IsRelayCapable  bool            `json:"is_relay_capable"`
	LastSeen        time.Time       `json:"last_seen"`
}

// WorkerConnectivity represents connectivity between two workers
type WorkerConnectivity struct {
	SourceWorker   string         `json:"source_worker"`
	DestWorker     string         `json:"dest_worker"`
	NetworkSegment *NetworkSegment `json:"network_segment,omitempty"`
	IsDirect       bool           `json:"is_direct"`
	RelayWorker    string         `json:"relay_worker,omitempty"`
	LatencyMs      int            `json:"latency_ms,omitempty"`
	BandwidthMbps  int            `json:"bandwidth_mbps,omitempty"`
	SuccessRate    float64        `json:"success_rate,omitempty"`
	LastTested     time.Time      `json:"last_tested"`
}

// P2PRoute represents a route for P2P streaming
type P2PRoute struct {
	Type           P2PRouteType    `json:"type"`
	NetworkSegment *NetworkSegment `json:"network_segment,omitempty"`
	RelayWorker    string          `json:"relay_worker,omitempty"`
	Priority       int             `json:"priority"`
	EstimatedSpeed string          `json:"estimated_speed,omitempty"`
}

// P2PRouteType represents the type of P2P route
type P2PRouteType string

const (
	P2PRouteDirect P2PRouteType = "direct"
	P2PRouteRelay  P2PRouteType = "relay"
	P2PRouteATC    P2PRouteType = "atc"
)

// P2PEndpoint represents a P2P endpoint for a worker
type P2PEndpoint struct {
	URL            string         `json:"url"`
	NetworkSegment NetworkSegment `json:"network_segment"`
	Priority       int            `json:"priority"`
	Bandwidth      string         `json:"bandwidth,omitempty"`
}

// P2PStreamingMetric represents metrics for a P2P streaming operation
type P2PStreamingMetric struct {
	ID              int        `json:"id"`
	SourceWorker    string     `json:"source_worker"`
	DestWorker      string     `json:"dest_worker"`
	VolumeHandle    string     `json:"volume_handle"`
	StreamingType   string     `json:"streaming_type"`
	RelayWorker     string     `json:"relay_worker,omitempty"`
	NetworkSegmentID string    `json:"network_segment_id,omitempty"`
	SizeBytes       int64      `json:"size_bytes"`
	DurationMs      int64      `json:"duration_ms"`
	Success         bool       `json:"success"`
	ErrorMessage    string     `json:"error_message,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

// NetworkTopologyFactory is used to manage network topology in the database
//
//counterfeiter:generate . NetworkTopologyFactory
type NetworkTopologyFactory interface {
	CreateOrUpdateNetworkSegment(segment NetworkSegment) error
	GetNetworkSegment(id string) (*NetworkSegment, bool, error)
	ListNetworkSegments() ([]NetworkSegment, error)

	CreateOrUpdateWorkerNetwork(info WorkerNetworkInfo) error
	GetWorkerNetworks(workerName string) ([]WorkerNetworkInfo, error)
	GetAllWorkerNetworks() (map[string][]WorkerNetworkInfo, error)

	UpdateWorkerConnectivity(connectivity WorkerConnectivity) error
	GetWorkerConnectivity(sourceWorker, destWorker string) ([]WorkerConnectivity, error)
	FindP2PRoute(sourceWorker, destWorker string) (*P2PRoute, error)

	RecordP2PStreamingMetric(metric P2PStreamingMetric) error
	GetP2PStreamingMetrics(since time.Time) ([]P2PStreamingMetric, error)
}

type networkTopologyFactory struct {
	conn        Conn
	lockFactory LockFactory
}

func NewNetworkTopologyFactory(conn Conn, lockFactory LockFactory) NetworkTopologyFactory {
	return &networkTopologyFactory{
		conn:        conn,
		lockFactory: lockFactory,
	}
}

func (f *networkTopologyFactory) CreateOrUpdateNetworkSegment(segment NetworkSegment) error {
	_, err := psql.Insert("network_segments").
		Columns("id", "cidr", "gateway", "type", "priority", "updated_at").
		Values(segment.ID, segment.CIDR, segment.Gateway, string(segment.Type), segment.Priority, sq.Expr("NOW()")).
		Suffix(`ON CONFLICT (id) DO UPDATE SET
			cidr = EXCLUDED.cidr,
			gateway = EXCLUDED.gateway,
			type = EXCLUDED.type,
			priority = EXCLUDED.priority,
			updated_at = EXCLUDED.updated_at`).
		RunWith(f.conn).
		Exec()
	return err
}

func (f *networkTopologyFactory) GetNetworkSegment(id string) (*NetworkSegment, bool, error) {
	var segment NetworkSegment
	err := psql.Select("id", "cidr", "gateway", "type", "priority").
		From("network_segments").
		Where(sq.Eq{"id": id}).
		RunWith(f.conn).
		QueryRow().
		Scan(&segment.ID, &segment.CIDR, &segment.Gateway, &segment.Type, &segment.Priority)

	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &segment, true, nil
}

func (f *networkTopologyFactory) ListNetworkSegments() ([]NetworkSegment, error) {
	rows, err := psql.Select("id", "cidr", "gateway", "type", "priority").
		From("network_segments").
		OrderBy("priority ASC, id ASC").
		RunWith(f.conn).
		Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var segments []NetworkSegment
	for rows.Next() {
		var segment NetworkSegment
		err := rows.Scan(&segment.ID, &segment.CIDR, &segment.Gateway, &segment.Type, &segment.Priority)
		if err != nil {
			return nil, err
		}
		segments = append(segments, segment)
	}
	return segments, nil
}

func (f *networkTopologyFactory) CreateOrUpdateWorkerNetwork(info WorkerNetworkInfo) error {
	// First ensure the network segment exists
	err := f.CreateOrUpdateNetworkSegment(info.NetworkSegment)
	if err != nil {
		return err
	}

	_, err = psql.Insert("worker_networks").
		Columns("worker_name", "network_segment_id", "interface_name", "ip_address",
			"port", "bandwidth", "is_relay_capable", "last_seen", "updated_at").
		Values(info.WorkerName, info.NetworkSegment.ID, info.InterfaceName, info.IPAddress,
			info.Port, info.Bandwidth, info.IsRelayCapable, sq.Expr("NOW()"), sq.Expr("NOW()")).
		Suffix(`ON CONFLICT (worker_name, network_segment_id) DO UPDATE SET
			interface_name = EXCLUDED.interface_name,
			ip_address = EXCLUDED.ip_address,
			port = EXCLUDED.port,
			bandwidth = EXCLUDED.bandwidth,
			is_relay_capable = EXCLUDED.is_relay_capable,
			last_seen = EXCLUDED.last_seen,
			updated_at = EXCLUDED.updated_at`).
		RunWith(f.conn).
		Exec()
	return err
}

func (f *networkTopologyFactory) GetWorkerNetworks(workerName string) ([]WorkerNetworkInfo, error) {
	rows, err := psql.Select(
		"wn.worker_name", "wn.interface_name", "wn.ip_address", "wn.port",
		"wn.bandwidth", "wn.is_relay_capable", "wn.last_seen",
		"ns.id", "ns.cidr", "ns.gateway", "ns.type", "ns.priority").
		From("worker_networks wn").
		Join("network_segments ns ON wn.network_segment_id = ns.id").
		Where(sq.Eq{"wn.worker_name": workerName}).
		OrderBy("ns.priority ASC").
		RunWith(f.conn).
		Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var networks []WorkerNetworkInfo
	for rows.Next() {
		var info WorkerNetworkInfo
		var gateway sql.NullString
		var bandwidth sql.NullString

		err := rows.Scan(
			&info.WorkerName, &info.InterfaceName, &info.IPAddress, &info.Port,
			&bandwidth, &info.IsRelayCapable, &info.LastSeen,
			&info.NetworkSegment.ID, &info.NetworkSegment.CIDR, &gateway,
			&info.NetworkSegment.Type, &info.NetworkSegment.Priority)
		if err != nil {
			return nil, err
		}

		if gateway.Valid {
			info.NetworkSegment.Gateway = gateway.String
		}
		if bandwidth.Valid {
			info.Bandwidth = bandwidth.String
		}

		networks = append(networks, info)
	}
	return networks, nil
}

func (f *networkTopologyFactory) GetAllWorkerNetworks() (map[string][]WorkerNetworkInfo, error) {
	rows, err := psql.Select(
		"wn.worker_name", "wn.interface_name", "wn.ip_address", "wn.port",
		"wn.bandwidth", "wn.is_relay_capable", "wn.last_seen",
		"ns.id", "ns.cidr", "ns.gateway", "ns.type", "ns.priority").
		From("worker_networks wn").
		Join("network_segments ns ON wn.network_segment_id = ns.id").
		OrderBy("wn.worker_name ASC, ns.priority ASC").
		RunWith(f.conn).
		Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]WorkerNetworkInfo)
	for rows.Next() {
		var info WorkerNetworkInfo
		var gateway sql.NullString
		var bandwidth sql.NullString

		err := rows.Scan(
			&info.WorkerName, &info.InterfaceName, &info.IPAddress, &info.Port,
			&bandwidth, &info.IsRelayCapable, &info.LastSeen,
			&info.NetworkSegment.ID, &info.NetworkSegment.CIDR, &gateway,
			&info.NetworkSegment.Type, &info.NetworkSegment.Priority)
		if err != nil {
			return nil, err
		}

		if gateway.Valid {
			info.NetworkSegment.Gateway = gateway.String
		}
		if bandwidth.Valid {
			info.Bandwidth = bandwidth.String
		}

		result[info.WorkerName] = append(result[info.WorkerName], info)
	}
	return result, nil
}

func (f *networkTopologyFactory) UpdateWorkerConnectivity(connectivity WorkerConnectivity) error {
	var networkSegmentID sql.NullString
	if connectivity.NetworkSegment != nil {
		networkSegmentID = sql.NullString{String: connectivity.NetworkSegment.ID, Valid: true}
	}

	_, err := psql.Insert("worker_connectivity").
		Columns("source_worker", "dest_worker", "network_segment_id", "is_direct",
			"relay_worker", "latency_ms", "bandwidth_mbps", "success_rate",
			"last_tested", "updated_at").
		Values(connectivity.SourceWorker, connectivity.DestWorker, networkSegmentID,
			connectivity.IsDirect, connectivity.RelayWorker, connectivity.LatencyMs,
			connectivity.BandwidthMbps, connectivity.SuccessRate, sq.Expr("NOW()"), sq.Expr("NOW()")).
		Suffix(`ON CONFLICT (source_worker, dest_worker, network_segment_id) DO UPDATE SET
			is_direct = EXCLUDED.is_direct,
			relay_worker = EXCLUDED.relay_worker,
			latency_ms = EXCLUDED.latency_ms,
			bandwidth_mbps = EXCLUDED.bandwidth_mbps,
			success_rate = EXCLUDED.success_rate,
			last_tested = EXCLUDED.last_tested,
			updated_at = EXCLUDED.updated_at`).
		RunWith(f.conn).
		Exec()
	return err
}

func (f *networkTopologyFactory) GetWorkerConnectivity(sourceWorker, destWorker string) ([]WorkerConnectivity, error) {
	rows, err := psql.Select(
		"wc.source_worker", "wc.dest_worker", "wc.is_direct", "wc.relay_worker",
		"wc.latency_ms", "wc.bandwidth_mbps", "wc.success_rate", "wc.last_tested",
		"ns.id", "ns.cidr", "ns.gateway", "ns.type", "ns.priority").
		From("worker_connectivity wc").
		LeftJoin("network_segments ns ON wc.network_segment_id = ns.id").
		Where(sq.And{
			sq.Eq{"wc.source_worker": sourceWorker},
			sq.Eq{"wc.dest_worker": destWorker},
		}).
		OrderBy("wc.success_rate DESC, wc.latency_ms ASC").
		RunWith(f.conn).
		Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var connectivities []WorkerConnectivity
	for rows.Next() {
		var conn WorkerConnectivity
		var relayWorker, nsID, nsCIDR, nsGateway, nsType sql.NullString
		var latencyMs, bandwidthMbps, nsPriority sql.NullInt64
		var successRate sql.NullFloat64

		err := rows.Scan(
			&conn.SourceWorker, &conn.DestWorker, &conn.IsDirect, &relayWorker,
			&latencyMs, &bandwidthMbps, &successRate, &conn.LastTested,
			&nsID, &nsCIDR, &nsGateway, &nsType, &nsPriority)
		if err != nil {
			return nil, err
		}

		if relayWorker.Valid {
			conn.RelayWorker = relayWorker.String
		}
		if latencyMs.Valid {
			conn.LatencyMs = int(latencyMs.Int64)
		}
		if bandwidthMbps.Valid {
			conn.BandwidthMbps = int(bandwidthMbps.Int64)
		}
		if successRate.Valid {
			conn.SuccessRate = successRate.Float64
		}
		if nsID.Valid {
			conn.NetworkSegment = &NetworkSegment{
				ID:       nsID.String,
				CIDR:     nsCIDR.String,
				Gateway:  nsGateway.String,
				Type:     NetworkSegmentType(nsType.String),
				Priority: int(nsPriority.Int64),
			}
		}

		connectivities = append(connectivities, conn)
	}
	return connectivities, nil
}

func (f *networkTopologyFactory) FindP2PRoute(sourceWorker, destWorker string) (*P2PRoute, error) {
	// First, check for direct connectivity
	directConns, err := f.GetWorkerConnectivity(sourceWorker, destWorker)
	if err != nil {
		return nil, err
	}

	// Find the best direct route
	for _, conn := range directConns {
		if conn.IsDirect && conn.SuccessRate > 0.5 {
			return &P2PRoute{
				Type:           P2PRouteDirect,
				NetworkSegment: conn.NetworkSegment,
				Priority:       1,
				EstimatedSpeed: fmt.Sprintf("%dMbps", conn.BandwidthMbps),
			}, nil
		}
	}

	// Check for relay routes
	for _, conn := range directConns {
		if !conn.IsDirect && conn.RelayWorker != "" && conn.SuccessRate > 0.5 {
			return &P2PRoute{
				Type:           P2PRouteRelay,
				NetworkSegment: conn.NetworkSegment,
				RelayWorker:    conn.RelayWorker,
				Priority:       2,
				EstimatedSpeed: fmt.Sprintf("%dMbps", conn.BandwidthMbps),
			}, nil
		}
	}

	// If no P2P route found, fallback to ATC
	return &P2PRoute{
		Type:     P2PRouteATC,
		Priority: 3,
	}, nil
}

func (f *networkTopologyFactory) RecordP2PStreamingMetric(metric P2PStreamingMetric) error {
	_, err := psql.Insert("p2p_streaming_metrics").
		Columns("source_worker", "dest_worker", "volume_handle", "streaming_type",
			"relay_worker", "network_segment_id", "size_bytes", "duration_ms",
			"success", "error_message", "created_at").
		Values(metric.SourceWorker, metric.DestWorker, metric.VolumeHandle, metric.StreamingType,
			metric.RelayWorker, metric.NetworkSegmentID, metric.SizeBytes, metric.DurationMs,
			metric.Success, metric.ErrorMessage, sq.Expr("NOW()")).
		RunWith(f.conn).
		Exec()
	return err
}

func (f *networkTopologyFactory) GetP2PStreamingMetrics(since time.Time) ([]P2PStreamingMetric, error) {
	rows, err := psql.Select("id", "source_worker", "dest_worker", "volume_handle",
		"streaming_type", "relay_worker", "network_segment_id", "size_bytes",
		"duration_ms", "success", "error_message", "created_at").
		From("p2p_streaming_metrics").
		Where(sq.Gt{"created_at": since}).
		OrderBy("created_at DESC").
		RunWith(f.conn).
		Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []P2PStreamingMetric
	for rows.Next() {
		var metric P2PStreamingMetric
		var relayWorker, networkSegmentID, errorMessage sql.NullString

		err := rows.Scan(&metric.ID, &metric.SourceWorker, &metric.DestWorker,
			&metric.VolumeHandle, &metric.StreamingType, &relayWorker, &networkSegmentID,
			&metric.SizeBytes, &metric.DurationMs, &metric.Success, &errorMessage,
			&metric.CreatedAt)
		if err != nil {
			return nil, err
		}

		if relayWorker.Valid {
			metric.RelayWorker = relayWorker.String
		}
		if networkSegmentID.Valid {
			metric.NetworkSegmentID = networkSegmentID.String
		}
		if errorMessage.Valid {
			metric.ErrorMessage = errorMessage.String
		}

		metrics = append(metrics, metric)
	}
	return metrics, nil
}