package api

import (
	"encoding/json"
	"net/http"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
)

// RelayHandler handles relay worker API endpoints
type RelayHandler struct {
	logger                 lager.Logger
	networkTopologyFactory db.NetworkTopologyFactory
}

// NewRelayHandler creates a new relay handler
func NewRelayHandler(
	logger lager.Logger,
	networkTopologyFactory db.NetworkTopologyFactory,
) *RelayHandler {
	return &RelayHandler{
		logger:                 logger.Session("relay-handler"),
		networkTopologyFactory: networkTopologyFactory,
	}
}

// UpdateRelayWorker handles PUT /api/v1/workers/:name/relay
func (h *RelayHandler) UpdateRelayWorker(w http.ResponseWriter, r *http.Request) {
	workerName := r.URL.Query().Get(":name")

	h.logger.Debug("update-relay-worker", lager.Data{
		"worker": workerName,
	})

	var req struct {
		RelayWorker     db.RelayWorker           `json:"relay_worker"`
		NetworkBridges  []db.RelayNetworkBridge  `json:"network_bridges"`
		Stats           RelayWorkerStats         `json:"stats,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logger.Error("failed-to-decode-request", err)
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid request body"})
		return
	}

	// Update relay worker in database
	err := h.networkTopologyFactory.UpdateRelayWorker(req.RelayWorker)
	if err != nil {
		h.logger.Error("failed-to-update-relay-worker", err, lager.Data{
			"worker": workerName,
		})
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "failed to update relay worker"})
		return
	}

	// Update network bridges
	err = h.networkTopologyFactory.UpdateRelayNetworkBridges(workerName, req.NetworkBridges)
	if err != nil {
		h.logger.Error("failed-to-update-network-bridges", err, lager.Data{
			"worker": workerName,
			"bridges": len(req.NetworkBridges),
		})
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "failed to update network bridges"})
		return
	}

	h.logger.Info("relay-worker-updated", lager.Data{
		"worker":  workerName,
		"enabled": req.RelayWorker.Enabled,
		"bridges": len(req.NetworkBridges),
	})

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

// GetRelayWorkers handles GET /api/v1/relay-workers
func (h *RelayHandler) GetRelayWorkers(w http.ResponseWriter, r *http.Request) {
	h.logger.Debug("get-relay-workers")

	topology, err := h.networkTopologyFactory.GetNetworkTopology()
	if err != nil {
		h.logger.Error("failed-to-get-topology", err)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "failed to get network topology"})
		return
	}

	response := struct {
		RelayWorkers []RelayWorkerInfo `json:"relay_workers"`
	}{
		RelayWorkers: make([]RelayWorkerInfo, 0),
	}

	// Build relay worker info with bridges
	for _, worker := range topology.RelayWorkers {
		info := RelayWorkerInfo{
			WorkerName:         worker.WorkerName,
			Enabled:            worker.Enabled,
			MaxConnections:     worker.MaxConnections,
			ActiveConnections:  worker.ActiveConnections,
			BandwidthLimitMbps: worker.BandwidthLimitMbps,
			TotalBytesRelayed:  worker.TotalBytesRelayed,
			NetworkBridges:     []db.RelayNetworkBridge{},
		}

		// Add bridges for this worker
		for _, bridge := range topology.RelayNetworkBridges {
			if bridge.WorkerName == worker.WorkerName {
				info.NetworkBridges = append(info.NetworkBridges, bridge)
			}
		}

		response.RelayWorkers = append(response.RelayWorkers, info)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// GetRelayWorker handles GET /api/v1/workers/:name/relay
func (h *RelayHandler) GetRelayWorker(w http.ResponseWriter, r *http.Request) {
	workerName := r.URL.Query().Get(":name")

	h.logger.Debug("get-relay-worker", lager.Data{
		"worker": workerName,
	})

	relay, found, err := h.networkTopologyFactory.GetRelayWorker(workerName)
	if err != nil {
		h.logger.Error("failed-to-get-relay-worker", err, lager.Data{
			"worker": workerName,
		})
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "failed to get relay worker"})
		return
	}

	if !found {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "worker not found or not a relay"})
		return
	}

	bridges, err := h.networkTopologyFactory.GetRelayNetworkBridges(workerName)
	if err != nil {
		h.logger.Error("failed-to-get-network-bridges", err, lager.Data{
			"worker": workerName,
		})
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "failed to get network bridges"})
		return
	}

	response := RelayWorkerInfo{
		WorkerName:         relay.WorkerName,
		Enabled:            relay.Enabled,
		MaxConnections:     relay.MaxConnections,
		ActiveConnections:  relay.ActiveConnections,
		BandwidthLimitMbps: relay.BandwidthLimitMbps,
		TotalBytesRelayed:  relay.TotalBytesRelayed,
		NetworkBridges:     bridges,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// RelayWorkerInfo contains relay worker information
type RelayWorkerInfo struct {
	WorkerName         string                    `json:"worker_name"`
	Enabled            bool                      `json:"enabled"`
	MaxConnections     int                       `json:"max_connections"`
	ActiveConnections  int                       `json:"active_connections"`
	BandwidthLimitMbps int                       `json:"bandwidth_limit_mbps"`
	TotalBytesRelayed  int64                     `json:"total_bytes_relayed"`
	NetworkBridges     []db.RelayNetworkBridge   `json:"network_bridges"`
}

// RelayWorkerStats contains relay worker statistics
type RelayWorkerStats struct {
	ActiveConnections int   `json:"active_connections"`
	TotalBytesRelayed int64 `json:"total_bytes_relayed"`
}