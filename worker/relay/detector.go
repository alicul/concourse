package relay

import (
	"context"
	"fmt"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/worker/network"
)

// Detector detects relay capabilities for a worker
type Detector struct {
	logger             lager.Logger
	workerName         string
	networkDetector    *network.Detector
	enabled            bool
	maxConnections     int
	bandwidthLimitMbps int
}

// Config holds relay worker configuration
type Config struct {
	Enabled            bool
	MaxConnections     int
	BandwidthLimitMbps int
}

// NewDetector creates a new relay detector
func NewDetector(
	logger lager.Logger,
	workerName string,
	networkDetector *network.Detector,
	config Config,
) *Detector {
	return &Detector{
		logger:             logger.Session("relay-detector"),
		workerName:         workerName,
		networkDetector:    networkDetector,
		enabled:            config.Enabled,
		maxConnections:     config.MaxConnections,
		bandwidthLimitMbps: config.BandwidthLimitMbps,
	}
}

// DetectRelayCapability detects if this worker can act as a relay
func (d *Detector) DetectRelayCapability(ctx context.Context) (*db.RelayWorker, []db.RelayNetworkBridge, error) {
	if !d.enabled {
		d.logger.Debug("relay-disabled")
		return nil, nil, nil
	}

	d.logger.Info("detecting-relay-capability")

	// Detect network segments this worker is connected to
	segments, err := d.networkDetector.DetectNetworkSegments(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to detect network segments: %w", err)
	}

	if len(segments) < 2 {
		d.logger.Info("insufficient-networks-for-relay", lager.Data{
			"segments": len(segments),
		})
		return nil, nil, nil
	}

	// Create relay worker configuration
	relayWorker := &db.RelayWorker{
		WorkerName:         d.workerName,
		Enabled:            true,
		MaxConnections:     d.maxConnections,
		BandwidthLimitMbps: d.bandwidthLimitMbps,
	}

	// Create network bridges for all segment pairs
	var bridges []db.RelayNetworkBridge
	for i, fromSegment := range segments {
		for j, toSegment := range segments {
			if i == j {
				continue // Skip same segment
			}

			bridge := db.RelayNetworkBridge{
				WorkerName:  d.workerName,
				FromSegment: fromSegment.ID,
				ToSegment:   toSegment.ID,
				Enabled:     true,
				Priority:    d.calculateBridgePriority(fromSegment.Type, toSegment.Type),
			}
			bridges = append(bridges, bridge)

			d.logger.Debug("relay-bridge-detected", lager.Data{
				"from": fromSegment.ID,
				"to":   toSegment.ID,
			})
		}
	}

	d.logger.Info("relay-capability-detected", lager.Data{
		"bridges": len(bridges),
	})

	return relayWorker, bridges, nil
}

// calculateBridgePriority calculates priority for a network bridge
func (d *Detector) calculateBridgePriority(fromType, toType string) int {
	// Lower priority value = higher priority
	// Prefer private-to-private bridges
	if fromType == "private" && toType == "private" {
		return 1
	}
	// Then private-to-public
	if (fromType == "private" && toType == "public") ||
		(fromType == "public" && toType == "private") {
		return 2
	}
	// Overlay networks have medium priority
	if fromType == "overlay" || toType == "overlay" {
		return 3
	}
	// Public-to-public has lowest priority
	return 4
}

// IsRelayCapable returns whether this worker can act as a relay
func (d *Detector) IsRelayCapable() bool {
	return d.enabled
}

// GetConfig returns the relay configuration
func (d *Detector) GetConfig() Config {
	return Config{
		Enabled:            d.enabled,
		MaxConnections:     d.maxConnections,
		BandwidthLimitMbps: d.bandwidthLimitMbps,
	}
}