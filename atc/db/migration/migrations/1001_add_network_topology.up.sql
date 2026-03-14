BEGIN;

-- Table to store network segments
CREATE TABLE IF NOT EXISTS network_segments (
    id TEXT PRIMARY KEY,
    cidr TEXT NOT NULL,
    gateway TEXT,
    type TEXT NOT NULL CHECK (type IN ('private', 'public', 'overlay')),
    priority INTEGER DEFAULT 1,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- Table to store worker network information
CREATE TABLE IF NOT EXISTS worker_networks (
    id SERIAL PRIMARY KEY,
    worker_name TEXT NOT NULL REFERENCES workers(name) ON DELETE CASCADE,
    network_segment_id TEXT NOT NULL REFERENCES network_segments(id) ON DELETE CASCADE,
    interface_name TEXT NOT NULL,
    ip_address TEXT NOT NULL,
    port INTEGER NOT NULL DEFAULT 7788,
    bandwidth TEXT,
    is_relay_capable BOOLEAN DEFAULT FALSE,
    last_seen TIMESTAMP NOT NULL DEFAULT NOW(),
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    UNIQUE(worker_name, network_segment_id)
);

-- Table to track worker connectivity
CREATE TABLE IF NOT EXISTS worker_connectivity (
    id SERIAL PRIMARY KEY,
    source_worker TEXT NOT NULL REFERENCES workers(name) ON DELETE CASCADE,
    dest_worker TEXT NOT NULL REFERENCES workers(name) ON DELETE CASCADE,
    network_segment_id TEXT REFERENCES network_segments(id) ON DELETE CASCADE,
    is_direct BOOLEAN NOT NULL DEFAULT TRUE,
    relay_worker TEXT REFERENCES workers(name) ON DELETE SET NULL,
    latency_ms INTEGER,
    bandwidth_mbps INTEGER,
    success_rate DECIMAL(5,2),
    last_tested TIMESTAMP NOT NULL DEFAULT NOW(),
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    UNIQUE(source_worker, dest_worker, network_segment_id)
);

-- Table for P2P streaming metrics
CREATE TABLE IF NOT EXISTS p2p_streaming_metrics (
    id SERIAL PRIMARY KEY,
    source_worker TEXT NOT NULL,
    dest_worker TEXT NOT NULL,
    volume_handle TEXT NOT NULL,
    streaming_type TEXT NOT NULL CHECK (streaming_type IN ('direct', 'relay', 'atc')),
    relay_worker TEXT,
    network_segment_id TEXT REFERENCES network_segments(id),
    size_bytes BIGINT,
    duration_ms BIGINT,
    success BOOLEAN NOT NULL,
    error_message TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- Indexes for performance
CREATE INDEX idx_worker_networks_worker ON worker_networks(worker_name);
CREATE INDEX idx_worker_networks_segment ON worker_networks(network_segment_id);
CREATE INDEX idx_worker_connectivity_source ON worker_connectivity(source_worker);
CREATE INDEX idx_worker_connectivity_dest ON worker_connectivity(dest_worker);
CREATE INDEX idx_p2p_metrics_workers ON p2p_streaming_metrics(source_worker, dest_worker);
CREATE INDEX idx_p2p_metrics_created ON p2p_streaming_metrics(created_at DESC);

COMMIT;