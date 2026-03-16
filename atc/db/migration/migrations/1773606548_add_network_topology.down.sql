BEGIN;

-- Drop indexes
DROP INDEX IF EXISTS idx_relay_network_bridges_to;
DROP INDEX IF EXISTS idx_relay_network_bridges_from;
DROP INDEX IF EXISTS idx_worker_connectivity_can_connect;
DROP INDEX IF EXISTS idx_worker_connectivity_dest;
DROP INDEX IF EXISTS idx_worker_connectivity_source;
DROP INDEX IF EXISTS idx_worker_networks_segment;
DROP INDEX IF EXISTS idx_worker_networks_worker;

-- Drop tables in reverse order of dependencies
DROP TABLE IF EXISTS relay_network_bridges;
DROP TABLE IF EXISTS relay_workers;
DROP TABLE IF EXISTS worker_connectivity;
DROP TABLE IF EXISTS worker_networks;
DROP TABLE IF EXISTS network_segments;

COMMIT;