package api

import (
	"encoding/json"
	"net/http"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/worker/baggageclaim/network"
)

// P2PMultiNetworkServer handles P2P requests in multi-network environments
type P2PMultiNetworkServer struct {
	logger          lager.Logger
	networkDetector network.Detector
	p2pStreamPort   uint16
}

// NewP2PMultiNetworkServer creates a new P2P multi-network server
func NewP2PMultiNetworkServer(
	logger lager.Logger,
	detector network.Detector,
	p2pStreamPort uint16,
) *P2PMultiNetworkServer {
	return &P2PMultiNetworkServer{
		logger:          logger,
		networkDetector: detector,
		p2pStreamPort:   p2pStreamPort,
	}
}

// P2PURLsResponse represents the response for GetP2PURLs
type P2PURLsResponse struct {
	Endpoints            []P2PEndpoint `json:"endpoints"`
	ConnectivityTestPort int           `json:"connectivity_test_port"`
	IsRelayCapable       bool          `json:"is_relay_capable"`
}

// P2PEndpoint represents a P2P endpoint with metadata
type P2PEndpoint struct {
	URL            string `json:"url"`
	NetworkSegment string `json:"network_segment"`
	Priority       int    `json:"priority"`
	Bandwidth      string `json:"bandwidth,omitempty"`
}

// GetP2PURLs returns all P2P URLs for multi-network support
func (server *P2PMultiNetworkServer) GetP2PURLs(w http.ResponseWriter, req *http.Request) {
	hLog := server.logger.Session("get-p2p-urls")
	hLog.Debug("start")
	defer hLog.Debug("done")

	// Get all P2P URLs from network detector
	p2pURLs := server.networkDetector.GetP2PURLs(server.p2pStreamPort)

	if len(p2pURLs) == 0 {
		hLog.Error("no-p2p-urls-found", nil)
		RespondWithError(w, ErrGetP2pUrlFailed, http.StatusInternalServerError)
		return
	}

	// Convert to response format
	endpoints := make([]P2PEndpoint, len(p2pURLs))
	for i, url := range p2pURLs {
		endpoints[i] = P2PEndpoint{
			URL:            url.URL,
			NetworkSegment: url.NetworkSegment,
			Priority:       url.Priority,
			Bandwidth:      url.Bandwidth,
		}
	}

	response := P2PURLsResponse{
		Endpoints:            endpoints,
		ConnectivityTestPort: int(server.p2pStreamPort + 1), // Use next port for connectivity tests
		IsRelayCapable:       server.networkDetector.IsRelayCapable(),
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		hLog.Error("failed-to-encode-response", err)
	}
}

// TestConnectivity tests connectivity to another worker
func (server *P2PMultiNetworkServer) TestConnectivity(w http.ResponseWriter, req *http.Request) {
	hLog := server.logger.Session("test-connectivity")
	hLog.Debug("start")
	defer hLog.Debug("done")

	var request struct {
		TargetURL string `json:"target_url"`
	}

	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		hLog.Error("failed-to-decode-request", err)
		RespondWithError(w, err, http.StatusBadRequest)
		return
	}

	result, err := server.networkDetector.TestConnectivity(request.TargetURL)
	if err != nil {
		hLog.Error("connectivity-test-failed", err)
		RespondWithError(w, err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		hLog.Error("failed-to-encode-response", err)
	}
}

// GetNetworkInfo returns detailed network information for this worker
func (server *P2PMultiNetworkServer) GetNetworkInfo(w http.ResponseWriter, req *http.Request) {
	hLog := server.logger.Session("get-network-info")
	hLog.Debug("start")
	defer hLog.Debug("done")

	networks, err := server.networkDetector.DetectNetworks()
	if err != nil {
		hLog.Error("failed-to-detect-networks", err)
		RespondWithError(w, err, http.StatusInternalServerError)
		return
	}

	response := struct {
		Networks       []network.NetworkInfo `json:"networks"`
		IsRelayCapable bool                  `json:"is_relay_capable"`
	}{
		Networks:       networks,
		IsRelayCapable: server.networkDetector.IsRelayCapable(),
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		hLog.Error("failed-to-encode-response", err)
	}
}

// GetP2PUrl provides backward compatibility with single network P2P
func (server *P2PMultiNetworkServer) GetP2PUrl(w http.ResponseWriter, req *http.Request) {
	hLog := server.logger.Session("get-p2p-url-legacy")
	hLog.Debug("start")
	defer hLog.Debug("done")

	// Get all P2P URLs
	p2pURLs := server.networkDetector.GetP2PURLs(server.p2pStreamPort)

	if len(p2pURLs) == 0 {
		hLog.Error("no-p2p-urls-found", nil)
		RespondWithError(w, ErrGetP2pUrlFailed, http.StatusInternalServerError)
		return
	}

	// Return the highest priority URL for backward compatibility
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(p2pURLs[0].URL))
}