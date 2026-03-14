package network

import (
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
)

// InterfaceConfig represents configuration for a network interface
type InterfaceConfig struct {
	Pattern        string `yaml:"pattern" json:"pattern"`
	NetworkSegment string `yaml:"network_segment" json:"network_segment"`
	Priority       int    `yaml:"priority" json:"priority"`
}

// NetworkInfo contains information about a network interface
type NetworkInfo struct {
	InterfaceName  string    `json:"interface_name"`
	IPAddress      string    `json:"ip_address"`
	CIDR           string    `json:"cidr"`
	NetworkSegment string    `json:"network_segment"`
	Priority       int       `json:"priority"`
	IsIPv6         bool      `json:"is_ipv6"`
	Gateway        string    `json:"gateway,omitempty"`
	Bandwidth      string    `json:"bandwidth,omitempty"`
	LastSeen       time.Time `json:"last_seen"`
}

// Detector detects network interfaces and their properties
type Detector interface {
	// DetectNetworks detects all configured network interfaces
	DetectNetworks() ([]NetworkInfo, error)
	// TestConnectivity tests connectivity to another worker
	TestConnectivity(targetURL string) (*ConnectivityResult, error)
	// GetP2PURLs returns P2P URLs for all network interfaces
	GetP2PURLs(port uint16) []P2PURL
	// IsRelayCapable checks if this worker can act as a relay
	IsRelayCapable() bool
}

// P2PURL represents a P2P endpoint URL with metadata
type P2PURL struct {
	URL            string `json:"url"`
	NetworkSegment string `json:"network_segment"`
	Priority       int    `json:"priority"`
	Bandwidth      string `json:"bandwidth,omitempty"`
}

// ConnectivityResult represents the result of a connectivity test
type ConnectivityResult struct {
	Success       bool          `json:"success"`
	Latency       time.Duration `json:"latency"`
	Bandwidth     string        `json:"bandwidth,omitempty"`
	NetworkPath   string        `json:"network_path,omitempty"`
	ErrorMessage  string        `json:"error_message,omitempty"`
}

// NetworkDetector implements the Detector interface
type NetworkDetector struct {
	logger             lager.Logger
	interfaceConfigs   []InterfaceConfig
	interfaceFamily    int // 4 for IPv4, 6 for IPv6
	autoDetect         bool
	relayEnabled       bool
}

// NewNetworkDetector creates a new network detector
func NewNetworkDetector(
	logger lager.Logger,
	configs []InterfaceConfig,
	family int,
	autoDetect bool,
	relayEnabled bool,
) Detector {
	return &NetworkDetector{
		logger:           logger,
		interfaceConfigs: configs,
		interfaceFamily:  family,
		autoDetect:       autoDetect,
		relayEnabled:     relayEnabled,
	}
}

// DetectNetworks detects all configured network interfaces
func (d *NetworkDetector) DetectNetworks() ([]NetworkInfo, error) {
	d.logger.Debug("detecting-networks")

	ifaces, err := net.Interfaces()
	if err != nil {
		d.logger.Error("failed-to-list-interfaces", err)
		return nil, err
	}

	var networks []NetworkInfo

	for _, iface := range ifaces {
		// Skip loopback and down interfaces
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		// Check if interface matches any configured pattern
		matchedConfig := d.findMatchingConfig(iface.Name)
		if matchedConfig == nil && !d.autoDetect {
			continue
		}

		// Get addresses for this interface
		addrs, err := iface.Addrs()
		if err != nil {
			d.logger.Error("failed-to-get-addresses", err, lager.Data{"interface": iface.Name})
			continue
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}

			ip := ipNet.IP

			// Filter by IP family
			isIPv6 := ip.To4() == nil
			if d.interfaceFamily == 4 && isIPv6 {
				continue
			}
			if d.interfaceFamily == 6 && !isIPv6 {
				continue
			}

			// Skip link-local addresses
			if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
				continue
			}

			networkInfo := NetworkInfo{
				InterfaceName: iface.Name,
				IPAddress:     ip.String(),
				CIDR:          ipNet.String(),
				IsIPv6:        isIPv6,
				LastSeen:      time.Now(),
			}

			// Set network segment and priority from config
			if matchedConfig != nil {
				networkInfo.NetworkSegment = matchedConfig.NetworkSegment
				networkInfo.Priority = matchedConfig.Priority
			} else if d.autoDetect {
				// Auto-detect network segment based on CIDR
				networkInfo.NetworkSegment = d.generateNetworkSegmentID(ipNet)
				networkInfo.Priority = 100 // Default priority for auto-detected
			}

			// Try to detect gateway
			networkInfo.Gateway = d.detectGateway(iface.Name)

			// Try to detect bandwidth
			networkInfo.Bandwidth = d.detectBandwidth(iface.Name)

			networks = append(networks, networkInfo)

			d.logger.Info("detected-network", lager.Data{
				"interface": networkInfo.InterfaceName,
				"ip":        networkInfo.IPAddress,
				"segment":   networkInfo.NetworkSegment,
				"priority":  networkInfo.Priority,
			})
		}
	}

	return networks, nil
}

// findMatchingConfig finds a matching interface configuration
func (d *NetworkDetector) findMatchingConfig(ifaceName string) *InterfaceConfig {
	for _, config := range d.interfaceConfigs {
		pattern, err := regexp.Compile(config.Pattern)
		if err != nil {
			d.logger.Error("invalid-interface-pattern", err, lager.Data{"pattern": config.Pattern})
			continue
		}

		if pattern.MatchString(ifaceName) {
			return &config
		}
	}
	return nil
}

// generateNetworkSegmentID generates a network segment ID from CIDR
func (d *NetworkDetector) generateNetworkSegmentID(ipNet *net.IPNet) string {
	// Use CIDR as the segment ID with slashes replaced
	segment := strings.ReplaceAll(ipNet.String(), "/", "-")
	segment = strings.ReplaceAll(segment, ".", "_")
	segment = strings.ReplaceAll(segment, ":", "_")
	return fmt.Sprintf("auto-%s", segment)
}

// detectGateway attempts to detect the default gateway for an interface
func (d *NetworkDetector) detectGateway(ifaceName string) string {
	// This is a simplified implementation
	// In production, you would parse routing tables
	// For now, return empty string
	return ""
}

// detectBandwidth attempts to detect the bandwidth of an interface
func (d *NetworkDetector) detectBandwidth(ifaceName string) string {
	// This would typically read from /sys/class/net/{ifaceName}/speed
	// For now, return a default value
	return "1000Mbps"
}

// TestConnectivity tests connectivity to another worker
func (d *NetworkDetector) TestConnectivity(targetURL string) (*ConnectivityResult, error) {
	d.logger.Debug("testing-connectivity", lager.Data{"target": targetURL})

	start := time.Now()

	// Parse the URL to get the host
	var host string
	if strings.HasPrefix(targetURL, "http://") {
		host = strings.TrimPrefix(targetURL, "http://")
	} else if strings.HasPrefix(targetURL, "https://") {
		host = strings.TrimPrefix(targetURL, "https://")
	} else {
		host = targetURL
	}

	// Remove path if present
	if idx := strings.Index(host, "/"); idx > 0 {
		host = host[:idx]
	}

	// Try to establish a TCP connection
	conn, err := net.DialTimeout("tcp", host, 5*time.Second)
	if err != nil {
		return &ConnectivityResult{
			Success:      false,
			ErrorMessage: err.Error(),
		}, nil
	}
	defer conn.Close()

	latency := time.Since(start)

	// Get local and remote addresses
	localAddr := conn.LocalAddr().String()
	remoteAddr := conn.RemoteAddr().String()

	return &ConnectivityResult{
		Success:     true,
		Latency:     latency,
		NetworkPath: fmt.Sprintf("%s -> %s", localAddr, remoteAddr),
	}, nil
}

// GetP2PURLs returns P2P URLs for all network interfaces
func (d *NetworkDetector) GetP2PURLs(port uint16) []P2PURL {
	networks, err := d.DetectNetworks()
	if err != nil {
		d.logger.Error("failed-to-detect-networks", err)
		return nil
	}

	var urls []P2PURL
	for _, network := range networks {
		url := P2PURL{
			URL:            fmt.Sprintf("http://%s:%d", network.IPAddress, port),
			NetworkSegment: network.NetworkSegment,
			Priority:       network.Priority,
			Bandwidth:      network.Bandwidth,
		}
		urls = append(urls, url)
	}

	// Sort by priority
	for i := 0; i < len(urls)-1; i++ {
		for j := i + 1; j < len(urls); j++ {
			if urls[j].Priority < urls[i].Priority {
				urls[i], urls[j] = urls[j], urls[i]
			}
		}
	}

	return urls
}

// IsRelayCapable checks if this worker can act as a relay
func (d *NetworkDetector) IsRelayCapable() bool {
	if !d.relayEnabled {
		return false
	}

	// A worker is relay capable if it has interfaces in multiple network segments
	networks, err := d.DetectNetworks()
	if err != nil {
		return false
	}

	segments := make(map[string]bool)
	for _, network := range networks {
		segments[network.NetworkSegment] = true
	}

	return len(segments) > 1
}