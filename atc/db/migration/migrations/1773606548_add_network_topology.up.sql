BEGIN;

-- Network segments table for defining network boundaries
CREATE TABLE IF NOT EXISTS network_segments (
    id TEXT PRIMARY KEY,
    cidr TEXT NOT NULL,
    gateway TEXT,
    type TEXT CHECK (type IN ('private', 'public', 'overlay')) DEFAULT 'private',
    priority INTEGER DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- Worker networks mapping - which workers are on which network segments
CREATE TABLE IF NOT EXISTS worker_networks (
    worker_name TEXT NOT NULL,
    segment_id TEXT NOT NULL,
    p2p_endpoint TEXT NOT NULL,
    interface_name TEXT,
    ip_address TEXT,
    bandwidth_mbps INTEGER,
    last_updated TIMESTAMP NOT NULL DEFAULT NOW(),
    PRIMARY KEY (worker_name, segment_id),
    FOREIGN KEY (worker_name) REFERENCES workers(name) ON DELETE CASCADE,
    FOREIGN KEY (segment_id) REFERENCES network_segments(id) ON DELETE CASCADE
);

-- Connectivity matrix - tracks which workers can reach each other
CREATE TABLE IF NOT EXISTS worker_connectivity (
    source_worker TEXT NOT NULL,
    dest_worker TEXT NOT NULL,
    can_connect BOOLEAN NOT NULL DEFAULT false,
    latency_ms INTEGER,
    bandwidth_mbps INTEGER,
    last_tested TIMESTAMP NOT NULL DEFAULT NOW(),
    test_error TEXT,
    PRIMARY KEY (source_worker, dest_worker),
    FOREIGN KEY (source_worker) REFERENCES workers(name) ON DELETE CASCADE,
    FOREIGN KEY (dest_worker) REFERENCES workers(name) ON DELETE CASCADE,
    CHECK (source_worker != dest_worker)
);

-- Relay worker capabilities
CREATE TABLE IF NOT EXISTS relay_workers (
    worker_name TEXT PRIMARY KEY,
    enabled BOOLEAN NOT NULL DEFAULT true,
    max_connections INTEGER DEFAULT 10,
    bandwidth_limit_mbps INTEGER,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    FOREIGN KEY (worker_name) REFERENCES workers(name) ON DELETE CASCADE
);

-- Network bridges that relay workers can provide
CREATE TABLE IF NOT EXISTS relay_network_bridges (
    worker_name TEXT NOT NULL,
    from_segment TEXT NOT NULL,
    to_segment TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    priority INTEGER DEFAULT 0,
    PRIMARY KEY (worker_name, from_segment, to_segment),
    FOREIGN KEY (worker_name) REFERENCES relay_workers(worker_name) ON DELETE CASCADE,
    FOREIGN KEY (from_segment) REFERENCES network_segments(id) ON DELETE CASCADE,
    FOREIGN KEY (to_segment) REFERENCES network_segments(id) ON DELETE CASCADE
);

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_worker_networks_worker ON worker_networks(worker_name);
CREATE INDEX IF NOT EXISTS idx_worker_networks_segment ON worker_networks(segment_id);
CREATE INDEX IF NOT EXISTS idx_worker_connectivity_source ON worker_connectivity(source_worker);
CREATE INDEX IF NOT EXISTS idx_worker_connectivity_dest ON worker_connectivity(dest_worker);
CREATE INDEX IF NOT EXISTS idx_worker_connectivity_can_connect ON worker_connectivity(can_connect);
CREATE INDEX IF NOT EXISTS idx_relay_network_bridges_from ON relay_network_bridges(from_segment);
CREATE INDEX IF NOT EXISTS idx_relay_network_bridges_to ON relay_network_bridges(to_segment);

COMMIT;