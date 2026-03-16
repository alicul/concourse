package api

import (
	"encoding/json"
	"net/http"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type NetworkTopologyHandler struct {
	networkTopologyFactory db.NetworkTopologyFactory
}

func NewNetworkTopologyHandler(networkTopologyFactory db.NetworkTopologyFactory) *NetworkTopologyHandler {
	return &NetworkTopologyHandler{
		networkTopologyFactory: networkTopologyFactory,
	}
}

// GetNetworkTopology returns the complete network topology
func (h *NetworkTopologyHandler) GetNetworkTopology(w http.ResponseWriter, r *http.Request) {
	logger := Logger(r.Context())

	topology, err := h.networkTopologyFactory.GetNetworkTopology()
	if err != nil {
		logger.Error("failed-to-get-network-topology", err)
		hLog.Error("failed-to-get-network-topology", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(topology); err != nil {
		logger.Error("failed-to-encode-network-topology", err)
		hLog.Error("failed-to-encode-network-topology", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

// UpdateWorkerNetworks updates network information for a worker
func (h *NetworkTopologyHandler) UpdateWorkerNetworks(w http.ResponseWriter, r *http.Request) {
	logger := Logger(r.Context())
	workerName := r.FormValue(":worker_name")

	var request struct {
		WorkerName string              `json:"worker_name"`
		Networks   []db.WorkerNetwork  `json:"networks"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		logger.Error("malformed-request-body", err)
		hLog.Error("malformed-request-body", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if request.WorkerName != workerName {
		logger.Error("worker-name-mismatch", nil)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if err := h.networkTopologyFactory.UpdateWorkerNetworks(workerName, request.Networks); err != nil {
		logger.Error("failed-to-update-worker-networks", err)
		hLog.Error("failed-to-update-worker-networks", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetWorkerNetworks returns network information for a specific worker
func (h *NetworkTopologyHandler) GetWorkerNetworks(w http.ResponseWriter, r *http.Request) {
	logger := Logger(r.Context())
	workerName := r.FormValue(":worker_name")

	networks, err := h.networkTopologyFactory.GetWorkerNetworks(workerName)
	if err != nil {
		logger.Error("failed-to-get-worker-networks", err, lager.Data{"worker": workerName})
		hLog.Error("failed-to-get-worker-networks", err, lager.Data{"worker": workerName})
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(networks); err != nil {
		logger.Error("failed-to-encode-worker-networks", err)
		hLog.Error("failed-to-encode-worker-networks", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

// GetWorkerP2PURLs returns P2P URLs for a specific worker
func (h *NetworkTopologyHandler) GetWorkerP2PURLs(w http.ResponseWriter, r *http.Request) {
	logger := Logger(r.Context())
	workerName := r.FormValue(":worker_name")

	networks, err := h.networkTopologyFactory.GetWorkerNetworks(workerName)
	if err != nil {
		logger.Error("failed-to-get-worker-networks", err, lager.Data{"worker": workerName})
		hLog.Error("failed-to-get-worker-networks", err, lager.Data{"worker": workerName})
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Convert to P2P URL response format
	var endpoints []atc.P2PEndpoint
	for _, network := range networks {
		endpoints = append(endpoints, atc.P2PEndpoint{
			URL:            network.P2PEndpoint,
			NetworkSegment: network.SegmentID,
			Priority:       1, // Could be enhanced with actual priority
		})
	}

	response := atc.P2PURLsResponse{
		Endpoints: endpoints,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		logger.Error("failed-to-encode-p2p-urls", err)
		hLog.Error("failed-to-encode-p2p-urls", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

// UpdateWorkerConnectivity updates connectivity test results for a worker
func (h *NetworkTopologyHandler) UpdateWorkerConnectivity(w http.ResponseWriter, r *http.Request) {
	logger := Logger(r.Context())
	workerName := r.FormValue(":worker_name")

	var request struct {
		SourceWorker string                    `json:"source_worker"`
		Results      []db.WorkerConnectivity   `json:"results"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		logger.Error("malformed-request-body", err)
		hLog.Error("malformed-request-body", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if request.SourceWorker != workerName {
		logger.Error("worker-name-mismatch", nil)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	for _, result := range request.Results {
		if err := h.networkTopologyFactory.UpdateWorkerConnectivity(result); err != nil {
			logger.Error("failed-to-update-connectivity", err, lager.Data{
				"source": result.SourceWorker,
				"dest":   result.DestWorker,
			})
			// Continue with other results even if one fails
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetConnectivityMatrix returns the full connectivity matrix
func (h *NetworkTopologyHandler) GetConnectivityMatrix(w http.ResponseWriter, r *http.Request) {
	logger := Logger(r.Context())

	matrix, err := h.networkTopologyFactory.GetConnectivityMatrix()
	if err != nil {
		logger.Error("failed-to-get-connectivity-matrix", err)
		hLog.Error("failed-to-get-connectivity-matrix", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(matrix); err != nil {
		logger.Error("failed-to-encode-connectivity-matrix", err)
		hLog.Error("failed-to-encode-connectivity-matrix", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}