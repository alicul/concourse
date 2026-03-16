package network

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
)

// NetworkInterface represents a network interface with its details
type NetworkInterface struct {
	Name      string   `json:"name"`
	IPAddress string   `json:"ip_address"`
	CIDR      string   `json:"cidr"`
	Gateway   string   `json:"gateway,omitempty"`
	IsPrivate bool     `json:"is_private"`
	IsPublic  bool     `json:"is_public"`
	Flags     []string `json:"flags"`
}

// NetworkSegmentInfo represents information about a network segment
type NetworkSegmentInfo struct {
	ID            string `json:"id"`
	CIDR          string `json:"cidr"`
	Gateway       string `json:"gateway,omitempty"`
	Type          string `json:"type"` // private, public, overlay
	InterfaceName string `json:"interface_name"`
	IPAddress     string `json:"ip_address"`
	P2PEndpoint   string `json:"p2p_endpoint"`
}

// Detector detects network topology for a worker
type Detector struct {
	logger              lager.Logger
	interfacePattern    *regexp.Regexp
	p2pPort             int
	ipFamily            int // 4 or 6
	excludeLoopback     bool
	excludeDockerBridge bool
}

// NewDetector creates a new network detector
func NewDetector(
	logger lager.Logger,
	interfacePattern string,
	p2pPort int,
	ipFamily int,
) (*Detector, error) {
	var pattern *regexp.Regexp
	var err error

	if interfacePattern != "" {
		pattern, err = regexp.Compile(interfacePattern)
		if err != nil {
			return nil, fmt.Errorf("invalid interface pattern: %w", err)
		}
	}

	return &Detector{
		logger:              logger,
		interfacePattern:    pattern,
		p2pPort:             p2pPort,
		ipFamily:            ipFamily,
		excludeLoopback:     true,
		excludeDockerBridge: true,
	}, nil
}

// DetectNetworkSegments detects all network segments this worker is connected to
func (d *Detector) DetectNetworkSegments(ctx context.Context) ([]NetworkSegmentInfo, error) {
	d.logger.Debug("detecting-network-segments")

	interfaces, err := d.getNetworkInterfaces()
	if err != nil {
		return nil, fmt.Errorf("failed to get network interfaces: %w", err)
	}

	var segments []NetworkSegmentInfo

	for _, iface := range interfaces {
		if d.shouldSkipInterface(iface) {
			d.logger.Debug("skipping-interface", lager.Data{"interface": iface.Name})
			continue
		}

		segment := d.interfaceToSegment(iface)
		if segment != nil {
			segments = append(segments, *segment)
			d.logger.Info("detected-network-segment", lager.Data{
				"segment_id": segment.ID,
				"interface":  segment.InterfaceName,
				"cidr":       segment.CIDR,
				"type":       segment.Type,
			})
		}
	}

	return segments, nil
}

// getNetworkInterfaces returns all network interfaces on the system
func (d *Detector) getNetworkInterfaces() ([]NetworkInterface, error) {
	var interfaces []NetworkInterface

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			d.logger.Error("failed-to-get-addresses", err, lager.Data{"interface": iface.Name})
			continue
		}

		for _, addr := range addrs {
			var ip net.IP
			var ipNet *net.IPNet

			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
				ipNet = v
			case *net.IPAddr:
				ip = v.IP
				// Create a default mask for IP addresses without CIDR
				if ip.To4() != nil {
					ipNet = &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)}
				} else {
					ipNet = &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}
				}
			}

			// Skip if wrong IP family
			if d.ipFamily == 4 && ip.To4() == nil {
				continue
			}
			if d.ipFamily == 6 && ip.To4() != nil {
				continue
			}

			ni := NetworkInterface{
				Name:      iface.Name,
				IPAddress: ip.String(),
				CIDR:      ipNet.String(),
				IsPrivate: isPrivateIP(ip),
				IsPublic:  !isPrivateIP(ip) && !ip.IsLoopback(),
				Flags:     interfaceFlags(iface),
			}

			// Try to detect gateway
			ni.Gateway = d.detectGateway(iface.Name)

			interfaces = append(interfaces, ni)
		}
	}

	return interfaces, nil
}

// shouldSkipInterface determines if an interface should be skipped
func (d *Detector) shouldSkipInterface(iface NetworkInterface) bool {
	// Skip loopback if configured
	if d.excludeLoopback && strings.HasPrefix(iface.Name, "lo") {
		return true
	}

	// Skip docker bridge if configured
	if d.excludeDockerBridge && (strings.HasPrefix(iface.Name, "docker") || strings.HasPrefix(iface.Name, "br-")) {
		return true
	}

	// Skip if doesn't match pattern
	if d.interfacePattern != nil && !d.interfacePattern.MatchString(iface.Name) {
		return true
	}

	// Skip link-local addresses
	ip := net.ParseIP(iface.IPAddress)
	if ip != nil && ip.IsLinkLocalUnicast() {
		return true
	}

	return false
}

// interfaceToSegment converts a network interface to a network segment
func (d *Detector) interfaceToSegment(iface NetworkInterface) *NetworkSegmentInfo {
	segmentType := "private"
	if iface.IsPublic {
		segmentType = "public"
	}

	// Check for overlay networks (common patterns)
	if strings.Contains(iface.Name, "flannel") || strings.Contains(iface.Name, "weave") || strings.Contains(iface.Name, "calico") {
		segmentType = "overlay"
	}

	// Generate segment ID based on CIDR
	segmentID := generateSegmentID(iface.CIDR, segmentType)

	// Build P2P endpoint
	p2pEndpoint := fmt.Sprintf("http://%s:%d", iface.IPAddress, d.p2pPort)

	return &NetworkSegmentInfo{
		ID:            segmentID,
		CIDR:          iface.CIDR,
		Gateway:       iface.Gateway,
		Type:          segmentType,
		InterfaceName: iface.Name,
		IPAddress:     iface.IPAddress,
		P2PEndpoint:   p2pEndpoint,
	}
}

// detectGateway attempts to detect the default gateway for an interface
func (d *Detector) detectGateway(interfaceName string) string {
	// This is a simplified implementation
	// In production, you'd want to parse routing tables properly
	// For now, return empty string
	return ""
}

// generateSegmentID generates a unique ID for a network segment
func generateSegmentID(cidr string, segmentType string) string {
	// Replace special characters to make it URL-safe
	safe := strings.ReplaceAll(cidr, "/", "-")
	safe = strings.ReplaceAll(safe, ".", "_")
	safe = strings.ReplaceAll(safe, ":", "_")
	return fmt.Sprintf("%s-%s", segmentType, safe)
}

// isPrivateIP checks if an IP is in a private range
func isPrivateIP(ip net.IP) bool {
	privateRanges := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"fc00::/7", // IPv6 private
	}

	for _, cidr := range privateRanges {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

// interfaceFlags returns the flags for an interface as strings
func interfaceFlags(iface net.Interface) []string {
	var flags []string
	if iface.Flags&net.FlagUp != 0 {
		flags = append(flags, "up")
	}
	if iface.Flags&net.FlagBroadcast != 0 {
		flags = append(flags, "broadcast")
	}
	if iface.Flags&net.FlagLoopback != 0 {
		flags = append(flags, "loopback")
	}
	if iface.Flags&net.FlagMulticast != 0 {
		flags = append(flags, "multicast")
	}
	return flags
}

// TestConnectivity tests connectivity to another worker's P2P endpoint
func (d *Detector) TestConnectivity(ctx context.Context, endpoint string) (bool, int, error) {
	startTime := time.Now()

	// Set a timeout for the connectivity test
	testCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Try to establish a TCP connection to the endpoint
	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(testCtx, "tcp", endpoint)
	if err != nil {
		return false, 0, err
	}
	defer conn.Close()

	// Calculate latency
	latencyMs := int(time.Since(startTime).Milliseconds())

	return true, latencyMs, nil
}