package baggageclaim

import "github.com/tedsuo/rata"

// P2P Multi-Network Routes
const (
	// New multi-network endpoints
	GetP2PURLs         = "GetP2PURLs"
	TestConnectivity   = "TestConnectivity"
	GetNetworkInfo     = "GetNetworkInfo"
	StreamP2PRelay     = "StreamP2PRelay"
	ProxyStream        = "ProxyStream"
)

// MultiNetworkRoutes defines the new P2P multi-network API routes
var MultiNetworkRoutes = rata.Routes{
	// Multi-network P2P endpoints
	{Path: "/p2p-urls", Method: "GET", Name: GetP2PURLs},
	{Path: "/test-connectivity", Method: "POST", Name: TestConnectivity},
	{Path: "/network-info", Method: "GET", Name: GetNetworkInfo},

	// Relay endpoints
	{Path: "/volumes/:handle/stream-p2p-relay", Method: "PUT", Name: StreamP2PRelay},
	{Path: "/proxy-stream", Method: "POST", Name: ProxyStream},
}

// CombineRoutes combines the original routes with multi-network routes
func CombineRoutes() rata.Routes {
	combined := make(rata.Routes, 0, len(Routes)+len(MultiNetworkRoutes))
	combined = append(combined, Routes...)
	combined = append(combined, MultiNetworkRoutes...)
	return combined
}