package baggageclaimcmd

import (
	"fmt"
	"regexp"
	"strings"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/worker/baggageclaim/api"
	"github.com/concourse/concourse/worker/baggageclaim/network"
	"github.com/concourse/concourse/worker/baggageclaim/volume"
	"github.com/tedsuo/rata"
	"gopkg.in/yaml.v2"
)

// P2PMultiNetworkConfig represents P2P multi-network configuration
type P2PMultiNetworkConfig struct {
	Interfaces              string `long:"p2p-interfaces" description:"YAML configuration for P2P network interfaces"`
	NetworkDetection        string `long:"p2p-network-detection" default:"auto" choice:"auto" choice:"manual" description:"Network detection mode"`
	RelayEnabled            bool   `long:"p2p-relay-enabled" description:"Enable this worker as a P2P relay"`
	ConnectivityTestPort    uint16 `long:"p2p-connectivity-test-port" default:"7789" description:"Port for P2P connectivity testing"`
	MaxRelaySize            int64  `long:"p2p-max-relay-size" default:"10737418240" description:"Maximum size for relay streaming (10GB default)"`
}

// ParseP2PInterfaces parses the P2P interfaces configuration
func (c *P2PMultiNetworkConfig) ParseP2PInterfaces() ([]network.InterfaceConfig, error) {
	if c.Interfaces == "" {
		// Default configuration
		return []network.InterfaceConfig{
			{
				Pattern:        "eth0",
				NetworkSegment: "default",
				Priority:       1,
			},
		}, nil
	}

	// Parse YAML configuration
	var configs []network.InterfaceConfig
	err := yaml.Unmarshal([]byte(c.Interfaces), &configs)
	if err != nil {
		return nil, fmt.Errorf("failed to parse P2P interfaces configuration: %w", err)
	}

	return configs, nil
}

// CreateMultiNetworkHandler creates the HTTP handler with multi-network support
func CreateMultiNetworkHandler(
	logger lager.Logger,
	strategizer volume.Strategizer,
	volumeRepo volume.Repository,
	multiNetworkConfig P2PMultiNetworkConfig,
	p2pInterfaceFamily int,
	p2pStreamPort uint16,
) (rata.Router, error) {
	// Parse interface configurations
	interfaceConfigs, err := multiNetworkConfig.ParseP2PInterfaces()
	if err != nil {
		return nil, err
	}

	// Create network detector
	networkDetector := network.NewNetworkDetector(
		logger.Session("network-detector"),
		interfaceConfigs,
		p2pInterfaceFamily,
		multiNetworkConfig.NetworkDetection == "auto",
		multiNetworkConfig.RelayEnabled,
	)

	// Create volume server
	volumeServer := api.NewVolumeServer(
		logger.Session("volume-server"),
		strategizer,
		volumeRepo,
	)

	// Create multi-network P2P server
	p2pMultiNetworkServer := api.NewP2PMultiNetworkServer(
		logger.Session("p2p-multinetwork-server"),
		networkDetector,
		p2pStreamPort,
	)

	// Create relay streamer if enabled
	var relayEndpoints *volume.RelayEndpoints
	if multiNetworkConfig.RelayEnabled {
		relayStreamer := volume.NewRelayStreamer(
			logger.Session("relay-streamer"),
			multiNetworkConfig.MaxRelaySize,
		)
		relayEndpoints = volume.NewRelayEndpoints(relayStreamer, logger)
	}

	// Build handlers map
	handlers := rata.Handlers{
		// Original baggageclaim routes
		baggageclaim.ListVolumes:               http.HandlerFunc(volumeServer.ListVolumes),
		baggageclaim.GetVolume:                 http.HandlerFunc(volumeServer.GetVolume),
		baggageclaim.CreateVolume:              http.HandlerFunc(volumeServer.CreateVolume),
		baggageclaim.CreateVolumeAsync:         http.HandlerFunc(volumeServer.CreateVolumeAsync),
		baggageclaim.CreateVolumeAsyncCheck:    http.HandlerFunc(volumeServer.CreateVolumeAsyncCheck),
		baggageclaim.CreateVolumeAsyncCancel:   http.HandlerFunc(volumeServer.CreateVolumeAsyncCancel),
		baggageclaim.SetProperty:               http.HandlerFunc(volumeServer.SetProperty),
		baggageclaim.GetPrivileged:             http.HandlerFunc(volumeServer.GetPrivileged),
		baggageclaim.SetPrivileged:             http.HandlerFunc(volumeServer.SetPrivileged),
		baggageclaim.StreamIn:                  http.HandlerFunc(volumeServer.StreamIn),
		baggageclaim.StreamOut:                 http.HandlerFunc(volumeServer.StreamOut),
		baggageclaim.StreamP2pOut:              http.HandlerFunc(volumeServer.StreamP2pOut),
		baggageclaim.DestroyVolume:             http.HandlerFunc(volumeServer.DestroyVolume),
		baggageclaim.DestroyVolumes:            http.HandlerFunc(volumeServer.DestroyVolumes),
		baggageclaim.CleanupOrphanedVolumes:    http.HandlerFunc(volumeServer.CleanupOrphanedVolumes),

		// Legacy P2P endpoint (for backward compatibility)
		baggageclaim.GetP2pUrl:                 http.HandlerFunc(p2pMultiNetworkServer.GetP2PUrl),

		// New multi-network P2P endpoints
		baggageclaim.GetP2PURLs:                http.HandlerFunc(p2pMultiNetworkServer.GetP2PURLs),
		baggageclaim.TestConnectivity:          http.HandlerFunc(p2pMultiNetworkServer.TestConnectivity),
		baggageclaim.GetNetworkInfo:            http.HandlerFunc(p2pMultiNetworkServer.GetNetworkInfo),
	}

	// Add relay endpoints if enabled
	if relayEndpoints != nil {
		handlers[baggageclaim.StreamP2PRelay] = http.HandlerFunc(relayEndpoints.HandleRelayStream)
		handlers[baggageclaim.ProxyStream] = http.HandlerFunc(relayEndpoints.HandleProxyStream)
	}

	// Create router with combined routes
	routes := baggageclaim.CombineRoutes()
	return rata.NewRouter(routes, handlers)
}

// ValidateP2PConfiguration validates P2P configuration
func ValidateP2PConfiguration(config P2PMultiNetworkConfig) error {
	// Parse and validate interface configurations
	interfaces, err := config.ParseP2PInterfaces()
	if err != nil {
		return err
	}

	// Validate interface patterns are valid regexes
	for _, iface := range interfaces {
		if _, err := regexp.Compile(iface.Pattern); err != nil {
			return fmt.Errorf("invalid interface pattern '%s': %w", iface.Pattern, err)
		}

		// Validate network segment is not empty
		if strings.TrimSpace(iface.NetworkSegment) == "" {
			return fmt.Errorf("network segment cannot be empty for pattern '%s'", iface.Pattern)
		}

		// Validate priority is positive
		if iface.Priority < 0 {
			return fmt.Errorf("priority must be non-negative for pattern '%s'", iface.Pattern)
		}
	}

	// Validate ports
	if config.ConnectivityTestPort == 0 {
		return fmt.Errorf("connectivity test port cannot be 0")
	}

	// Validate relay configuration
	if config.RelayEnabled && len(interfaces) < 2 {
		return fmt.Errorf("relay worker must have at least 2 network interfaces configured")
	}

	return nil
}