package atc

// P2PEndpoint represents a P2P endpoint with network metadata
type P2PEndpoint struct {
	URL            string `json:"url"`
	NetworkSegment string `json:"network_segment"`
	Priority       int    `json:"priority"`
	Bandwidth      string `json:"bandwidth,omitempty"`
}

// P2PURLsResponse represents the response for multi-network P2P URLs
type P2PURLsResponse struct {
	Endpoints            []P2PEndpoint `json:"endpoints"`
	ConnectivityTestPort int           `json:"connectivity_test_port,omitempty"`
}