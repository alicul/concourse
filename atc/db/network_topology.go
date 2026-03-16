package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
)

// NetworkSegmentType defines the type of network segment
type NetworkSegmentType string

const (
	NetworkSegmentPrivate NetworkSegmentType = "private"
	NetworkSegmentPublic  NetworkSegmentType = "public"
	NetworkSegmentOverlay NetworkSegmentType = "overlay"
)

// NetworkSegment represents a network segment in the topology
type NetworkSegment struct {
	ID        string             `db:"id" json:"id"`
	CIDR      string             `db:"cidr" json:"cidr"`
	Gateway   string             `db:"gateway" json:"gateway,omitempty"`
	Type      NetworkSegmentType `db:"type" json:"type"`
	Priority  int                `db:"priority" json:"priority"`
	CreatedAt time.Time          `db:"created_at" json:"created_at"`
}

// WorkerNetwork represents a worker's presence on a network segment
type WorkerNetwork struct {
	WorkerName    string    `db:"worker_name" json:"worker_name"`
	SegmentID     string    `db:"segment_id" json:"segment_id"`
	P2PEndpoint   string    `db:"p2p_endpoint" json:"p2p_endpoint"`
	InterfaceName string    `db:"interface_name" json:"interface_name,omitempty"`
	IPAddress     string    `db:"ip_address" json:"ip_address,omitempty"`
	BandwidthMbps int       `db:"bandwidth_mbps" json:"bandwidth_mbps,omitempty"`
	LastUpdated   time.Time `db:"last_updated" json:"last_updated"`
}

// WorkerConnectivity represents connectivity between two workers
type WorkerConnectivity struct {
	SourceWorker  string    `db:"source_worker" json:"source_worker"`
	DestWorker    string    `db:"dest_worker" json:"dest_worker"`
	CanConnect    bool      `db:"can_connect" json:"can_connect"`
	LatencyMs     int       `db:"latency_ms" json:"latency_ms,omitempty"`
	BandwidthMbps int       `db:"bandwidth_mbps" json:"bandwidth_mbps,omitempty"`
	LastTested    time.Time `db:"last_tested" json:"last_tested"`
	TestError     string    `db:"test_error" json:"test_error,omitempty"`
}

// RelayWorker represents a worker capable of relaying P2P streams
type RelayWorker struct {
	WorkerName        string    `db:"worker_name" json:"worker_name"`
	Enabled           bool      `db:"enabled" json:"enabled"`
	MaxConnections    int       `db:"max_connections" json:"max_connections"`
	BandwidthLimitMbps int      `db:"bandwidth_limit_mbps" json:"bandwidth_limit_mbps,omitempty"`
	CreatedAt         time.Time `db:"created_at" json:"created_at"`
}

// RelayNetworkBridge represents a network bridge that a relay worker can provide
type RelayNetworkBridge struct {
	WorkerName  string `db:"worker_name" json:"worker_name"`
	FromSegment string `db:"from_segment" json:"from_segment"`
	ToSegment   string `db:"to_segment" json:"to_segment"`
	Enabled     bool   `db:"enabled" json:"enabled"`
	Priority    int    `db:"priority" json:"priority"`
}

// NetworkTopology represents the complete network topology
type NetworkTopology struct {
	Segments      []NetworkSegment      `json:"segments"`
	WorkerNetworks []WorkerNetwork       `json:"worker_networks"`
	Connectivity  []WorkerConnectivity  `json:"connectivity"`
	RelayWorkers  []RelayWorker         `json:"relay_workers"`
	RelayBridges  []RelayNetworkBridge  `json:"relay_bridges"`
}

// NetworkTopologyFactory provides methods for managing network topology
//
//go:generate counterfeiter . NetworkTopologyFactory
type NetworkTopologyFactory interface {
	GetNetworkTopology() (NetworkTopology, error)

	// Network segments
	CreateOrUpdateNetworkSegment(segment NetworkSegment) error
	GetNetworkSegment(id string) (NetworkSegment, bool, error)
	DeleteNetworkSegment(id string) error

	// Worker networks
	UpdateWorkerNetworks(workerName string, networks []WorkerNetwork) error
	GetWorkerNetworks(workerName string) ([]WorkerNetwork, error)
	GetWorkersInSegment(segmentID string) ([]string, error)

	// Connectivity
	UpdateWorkerConnectivity(connectivity WorkerConnectivity) error
	GetWorkerConnectivity(sourceWorker, destWorker string) (WorkerConnectivity, bool, error)
	GetConnectivityMatrix() ([]WorkerConnectivity, error)
	TestAndUpdateConnectivity(sourceWorker, destWorker string) error

	// Relay workers
	RegisterRelayWorker(relay RelayWorker) error
	UpdateRelayWorker(relay RelayWorker) error
	GetRelayWorkers() ([]RelayWorker, error)
	AddRelayBridge(bridge RelayNetworkBridge) error
	GetRelayBridges(workerName string) ([]RelayNetworkBridge, error)
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

func (f *networkTopologyFactory) GetNetworkTopology() (NetworkTopology, error) {
	var topology NetworkTopology

	// Get all network segments
	rows, err := psql.Select("id", "cidr", "gateway", "type", "priority", "created_at").
		From("network_segments").
		RunWith(f.conn).
		Query()
	if err != nil {
		return topology, err
	}
	defer rows.Close()

	for rows.Next() {
		var segment NetworkSegment
		err := rows.Scan(&segment.ID, &segment.CIDR, &segment.Gateway, &segment.Type, &segment.Priority, &segment.CreatedAt)
		if err != nil {
			return topology, err
		}
		topology.Segments = append(topology.Segments, segment)
	}

	// Get all worker networks
	rows, err = psql.Select("worker_name", "segment_id", "p2p_endpoint", "interface_name", "ip_address", "bandwidth_mbps", "last_updated").
		From("worker_networks").
		RunWith(f.conn).
		Query()
	if err != nil {
		return topology, err
	}
	defer rows.Close()

	for rows.Next() {
		var wn WorkerNetwork
		err := rows.Scan(&wn.WorkerName, &wn.SegmentID, &wn.P2PEndpoint, &wn.InterfaceName, &wn.IPAddress, &wn.BandwidthMbps, &wn.LastUpdated)
		if err != nil {
			return topology, err
		}
		topology.WorkerNetworks = append(topology.WorkerNetworks, wn)
	}

	// Get connectivity matrix
	rows, err = psql.Select("source_worker", "dest_worker", "can_connect", "latency_ms", "bandwidth_mbps", "last_tested", "test_error").
		From("worker_connectivity").
		RunWith(f.conn).
		Query()
	if err != nil {
		return topology, err
	}
	defer rows.Close()

	for rows.Next() {
		var conn WorkerConnectivity
		err := rows.Scan(&conn.SourceWorker, &conn.DestWorker, &conn.CanConnect, &conn.LatencyMs, &conn.BandwidthMbps, &conn.LastTested, &conn.TestError)
		if err != nil {
			return topology, err
		}
		topology.Connectivity = append(topology.Connectivity, conn)
	}

	// Get relay workers
	rows, err = psql.Select("worker_name", "enabled", "max_connections", "bandwidth_limit_mbps", "created_at").
		From("relay_workers").
		RunWith(f.conn).
		Query()
	if err != nil {
		return topology, err
	}
	defer rows.Close()

	for rows.Next() {
		var relay RelayWorker
		err := rows.Scan(&relay.WorkerName, &relay.Enabled, &relay.MaxConnections, &relay.BandwidthLimitMbps, &relay.CreatedAt)
		if err != nil {
			return topology, err
		}
		topology.RelayWorkers = append(topology.RelayWorkers, relay)
	}

	// Get relay bridges
	rows, err = psql.Select("worker_name", "from_segment", "to_segment", "enabled", "priority").
		From("relay_network_bridges").
		RunWith(f.conn).
		Query()
	if err != nil {
		return topology, err
	}
	defer rows.Close()

	for rows.Next() {
		var bridge RelayNetworkBridge
		err := rows.Scan(&bridge.WorkerName, &bridge.FromSegment, &bridge.ToSegment, &bridge.Enabled, &bridge.Priority)
		if err != nil {
			return topology, err
		}
		topology.RelayBridges = append(topology.RelayBridges, bridge)
	}

	return topology, nil
}

func (f *networkTopologyFactory) CreateOrUpdateNetworkSegment(segment NetworkSegment) error {
	_, err := psql.Insert("network_segments").
		Columns("id", "cidr", "gateway", "type", "priority").
		Values(segment.ID, segment.CIDR, segment.Gateway, segment.Type, segment.Priority).
		Suffix("ON CONFLICT (id) DO UPDATE SET cidr = EXCLUDED.cidr, gateway = EXCLUDED.gateway, type = EXCLUDED.type, priority = EXCLUDED.priority").
		RunWith(f.conn).
		Exec()
	return err
}

func (f *networkTopologyFactory) GetNetworkSegment(id string) (NetworkSegment, bool, error) {
	var segment NetworkSegment
	err := psql.Select("id", "cidr", "gateway", "type", "priority", "created_at").
		From("network_segments").
		Where(sq.Eq{"id": id}).
		RunWith(f.conn).
		QueryRow().
		Scan(&segment.ID, &segment.CIDR, &segment.Gateway, &segment.Type, &segment.Priority, &segment.CreatedAt)

	if err == sql.ErrNoRows {
		return segment, false, nil
	}
	if err != nil {
		return segment, false, err
	}
	return segment, true, nil
}

func (f *networkTopologyFactory) DeleteNetworkSegment(id string) error {
	_, err := psql.Delete("network_segments").
		Where(sq.Eq{"id": id}).
		RunWith(f.conn).
		Exec()
	return err
}

func (f *networkTopologyFactory) UpdateWorkerNetworks(workerName string, networks []WorkerNetwork) error {
	tx, err := f.conn.Begin()
	if err != nil {
		return err
	}
	defer Rollback(tx)

	// Delete existing networks for this worker
	_, err = psql.Delete("worker_networks").
		Where(sq.Eq{"worker_name": workerName}).
		RunWith(tx).
		Exec()
	if err != nil {
		return err
	}

	// Insert new networks
	for _, network := range networks {
		_, err = psql.Insert("worker_networks").
			Columns("worker_name", "segment_id", "p2p_endpoint", "interface_name", "ip_address", "bandwidth_mbps").
			Values(workerName, network.SegmentID, network.P2PEndpoint, network.InterfaceName, network.IPAddress, network.BandwidthMbps).
			RunWith(tx).
			Exec()
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (f *networkTopologyFactory) GetWorkerNetworks(workerName string) ([]WorkerNetwork, error) {
	var networks []WorkerNetwork

	rows, err := psql.Select("worker_name", "segment_id", "p2p_endpoint", "interface_name", "ip_address", "bandwidth_mbps", "last_updated").
		From("worker_networks").
		Where(sq.Eq{"worker_name": workerName}).
		RunWith(f.conn).
		Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var network WorkerNetwork
		err := rows.Scan(&network.WorkerName, &network.SegmentID, &network.P2PEndpoint, &network.InterfaceName, &network.IPAddress, &network.BandwidthMbps, &network.LastUpdated)
		if err != nil {
			return nil, err
		}
		networks = append(networks, network)
	}

	return networks, nil
}

func (f *networkTopologyFactory) GetWorkersInSegment(segmentID string) ([]string, error) {
	var workers []string

	rows, err := psql.Select("DISTINCT worker_name").
		From("worker_networks").
		Where(sq.Eq{"segment_id": segmentID}).
		RunWith(f.conn).
		Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var worker string
		err := rows.Scan(&worker)
		if err != nil {
			return nil, err
		}
		workers = append(workers, worker)
	}

	return workers, nil
}

func (f *networkTopologyFactory) UpdateWorkerConnectivity(connectivity WorkerConnectivity) error {
	_, err := psql.Insert("worker_connectivity").
		Columns("source_worker", "dest_worker", "can_connect", "latency_ms", "bandwidth_mbps", "test_error").
		Values(connectivity.SourceWorker, connectivity.DestWorker, connectivity.CanConnect, connectivity.LatencyMs, connectivity.BandwidthMbps, connectivity.TestError).
		Suffix("ON CONFLICT (source_worker, dest_worker) DO UPDATE SET can_connect = EXCLUDED.can_connect, latency_ms = EXCLUDED.latency_ms, bandwidth_mbps = EXCLUDED.bandwidth_mbps, test_error = EXCLUDED.test_error, last_tested = NOW()").
		RunWith(f.conn).
		Exec()
	return err
}

func (f *networkTopologyFactory) GetWorkerConnectivity(sourceWorker, destWorker string) (WorkerConnectivity, bool, error) {
	var conn WorkerConnectivity
	err := psql.Select("source_worker", "dest_worker", "can_connect", "latency_ms", "bandwidth_mbps", "last_tested", "test_error").
		From("worker_connectivity").
		Where(sq.Eq{"source_worker": sourceWorker, "dest_worker": destWorker}).
		RunWith(f.conn).
		QueryRow().
		Scan(&conn.SourceWorker, &conn.DestWorker, &conn.CanConnect, &conn.LatencyMs, &conn.BandwidthMbps, &conn.LastTested, &conn.TestError)

	if err == sql.ErrNoRows {
		return conn, false, nil
	}
	if err != nil {
		return conn, false, err
	}
	return conn, true, nil
}

func (f *networkTopologyFactory) GetConnectivityMatrix() ([]WorkerConnectivity, error) {
	var matrix []WorkerConnectivity

	rows, err := psql.Select("source_worker", "dest_worker", "can_connect", "latency_ms", "bandwidth_mbps", "last_tested", "test_error").
		From("worker_connectivity").
		RunWith(f.conn).
		Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var conn WorkerConnectivity
		err := rows.Scan(&conn.SourceWorker, &conn.DestWorker, &conn.CanConnect, &conn.LatencyMs, &conn.BandwidthMbps, &conn.LastTested, &conn.TestError)
		if err != nil {
			return nil, err
		}
		matrix = append(matrix, conn)
	}

	return matrix, nil
}

func (f *networkTopologyFactory) TestAndUpdateConnectivity(sourceWorker, destWorker string) error {
	// This will be implemented by the actual connectivity testing logic
	// For now, just return an error indicating it needs implementation
	return fmt.Errorf("connectivity testing not yet implemented")
}

func (f *networkTopologyFactory) RegisterRelayWorker(relay RelayWorker) error {
	_, err := psql.Insert("relay_workers").
		Columns("worker_name", "enabled", "max_connections", "bandwidth_limit_mbps").
		Values(relay.WorkerName, relay.Enabled, relay.MaxConnections, relay.BandwidthLimitMbps).
		RunWith(f.conn).
		Exec()
	return err
}

func (f *networkTopologyFactory) UpdateRelayWorker(relay RelayWorker) error {
	_, err := psql.Update("relay_workers").
		Set("enabled", relay.Enabled).
		Set("max_connections", relay.MaxConnections).
		Set("bandwidth_limit_mbps", relay.BandwidthLimitMbps).
		Where(sq.Eq{"worker_name": relay.WorkerName}).
		RunWith(f.conn).
		Exec()
	return err
}

func (f *networkTopologyFactory) GetRelayWorkers() ([]RelayWorker, error) {
	var relays []RelayWorker

	rows, err := psql.Select("worker_name", "enabled", "max_connections", "bandwidth_limit_mbps", "created_at").
		From("relay_workers").
		Where(sq.Eq{"enabled": true}).
		RunWith(f.conn).
		Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var relay RelayWorker
		err := rows.Scan(&relay.WorkerName, &relay.Enabled, &relay.MaxConnections, &relay.BandwidthLimitMbps, &relay.CreatedAt)
		if err != nil {
			return nil, err
		}
		relays = append(relays, relay)
	}

	return relays, nil
}

func (f *networkTopologyFactory) AddRelayBridge(bridge RelayNetworkBridge) error {
	_, err := psql.Insert("relay_network_bridges").
		Columns("worker_name", "from_segment", "to_segment", "enabled", "priority").
		Values(bridge.WorkerName, bridge.FromSegment, bridge.ToSegment, bridge.Enabled, bridge.Priority).
		RunWith(f.conn).
		Exec()
	return err
}

func (f *networkTopologyFactory) GetRelayBridges(workerName string) ([]RelayNetworkBridge, error) {
	var bridges []RelayNetworkBridge

	query := psql.Select("worker_name", "from_segment", "to_segment", "enabled", "priority").
		From("relay_network_bridges")

	if workerName != "" {
		query = query.Where(sq.Eq{"worker_name": workerName})
	}

	rows, err := query.RunWith(f.conn).Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var bridge RelayNetworkBridge
		err := rows.Scan(&bridge.WorkerName, &bridge.FromSegment, &bridge.ToSegment, &bridge.Enabled, &bridge.Priority)
		if err != nil {
			return nil, err
		}
		bridges = append(bridges, bridge)
	}

	return bridges, nil
}