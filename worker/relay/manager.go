package relay

import (
	"context"
	"fmt"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
)

// Manager manages relay worker operations
type Manager struct {
	logger          lager.Logger
	workerName      string
	detector        *Detector
	proxy           *Proxy
	reporter        *Reporter
	enabled         bool
	mu              sync.RWMutex
	relayCapability *db.RelayWorker
	networkBridges  []db.RelayNetworkBridge
}

// Reporter reports relay status to ATC
type Reporter struct {
	logger     lager.Logger
	workerName string
	atcURL     string
	interval   time.Duration
}

// NewManager creates a new relay manager
func NewManager(
	logger lager.Logger,
	workerName string,
	detector *Detector,
	proxy *Proxy,
	atcURL string,
	reportInterval time.Duration,
) *Manager {
	reporter := &Reporter{
		logger:     logger.Session("relay-reporter"),
		workerName: workerName,
		atcURL:     atcURL,
		interval:   reportInterval,
	}

	return &Manager{
		logger:     logger.Session("relay-manager"),
		workerName: workerName,
		detector:   detector,
		proxy:      proxy,
		reporter:   reporter,
		enabled:    detector.IsRelayCapable(),
	}
}

// Start starts the relay manager
func (m *Manager) Start(ctx context.Context) error {
	if !m.enabled {
		m.logger.Info("relay-disabled")
		return nil
	}

	m.logger.Info("starting-relay-manager")

	// Detect initial relay capability
	if err := m.detectCapability(ctx); err != nil {
		return fmt.Errorf("detecting relay capability: %w", err)
	}

	// Start periodic reporting
	go m.runReporter(ctx)

	// Start periodic capability refresh
	go m.runCapabilityRefresh(ctx)

	return nil
}

// detectCapability detects and stores relay capability
func (m *Manager) detectCapability(ctx context.Context) error {
	relayWorker, bridges, err := m.detector.DetectRelayCapability(ctx)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.relayCapability = relayWorker
	m.networkBridges = bridges
	m.mu.Unlock()

	if relayWorker != nil {
		m.logger.Info("relay-capability-stored", lager.Data{
			"bridges": len(bridges),
		})
	}

	return nil
}

// runReporter periodically reports relay status to ATC
func (m *Manager) runReporter(ctx context.Context) {
	ticker := time.NewTicker(m.reporter.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.reportStatus(ctx); err != nil {
				m.logger.Error("report-status-failed", err)
			}
		}
	}
}

// runCapabilityRefresh periodically refreshes relay capability
func (m *Manager) runCapabilityRefresh(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.detectCapability(ctx); err != nil {
				m.logger.Error("capability-refresh-failed", err)
			}
		}
	}
}

// reportStatus reports current relay status to ATC
func (m *Manager) reportStatus(ctx context.Context) error {
	m.mu.RLock()
	capability := m.relayCapability
	bridges := m.networkBridges
	m.mu.RUnlock()

	if capability == nil {
		return nil // Not a relay worker
	}

	// Get proxy statistics
	stats := m.proxy.GetStats()

	// Update capability with current stats
	capability.ActiveConnections = stats.ActiveConnections
	capability.TotalBytesRelayed = stats.TotalBytesRelayed

	m.logger.Debug("reporting-relay-status", lager.Data{
		"active_connections": stats.ActiveConnections,
		"total_bytes":        stats.TotalBytesRelayed,
		"bridges":            len(bridges),
	})

	// Report to ATC (would make actual API call here)
	return m.reporter.reportToATC(ctx, capability, bridges, stats)
}

// HandleRelayRequest handles an incoming relay request
func (m *Manager) HandleRelayRequest(
	ctx context.Context,
	sourceURL string,
	destURL string,
	volumeID string,
	fromSegment string,
	toSegment string,
) error {
	if !m.enabled {
		return fmt.Errorf("relay not enabled on this worker")
	}

	// Check if we support this bridge
	if !m.supportsBridge(fromSegment, toSegment) {
		return fmt.Errorf("worker does not support bridge from %s to %s", fromSegment, toSegment)
	}

	m.logger.Info("handling-relay-request", lager.Data{
		"volume_id":    volumeID,
		"from_segment": fromSegment,
		"to_segment":   toSegment,
	})

	// Perform the relay
	metadata := map[string]string{
		"from_segment": fromSegment,
		"to_segment":   toSegment,
		"relay_worker": m.workerName,
	}

	return m.proxy.RelayStream(ctx, sourceURL, destURL, volumeID, metadata)
}

// supportsBridge checks if this worker supports a network bridge
func (m *Manager) supportsBridge(fromSegment, toSegment string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, bridge := range m.networkBridges {
		if bridge.FromSegment == fromSegment && bridge.ToSegment == toSegment {
			return bridge.Enabled
		}
	}
	return false
}

// GetCapability returns the current relay capability
func (m *Manager) GetCapability() (*db.RelayWorker, []db.RelayNetworkBridge) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.relayCapability, m.networkBridges
}

// IsEnabled returns whether relay is enabled
func (m *Manager) IsEnabled() bool {
	return m.enabled
}

// reportToATC reports relay status to ATC
func (r *Reporter) reportToATC(
	ctx context.Context,
	capability *db.RelayWorker,
	bridges []db.RelayNetworkBridge,
	stats ProxyStats,
) error {
	// In a real implementation, this would make an HTTP PUT request to:
	// PUT /api/v1/workers/:name/relay
	// with the relay capability and bridge information

	r.logger.Debug("reporting-to-atc", lager.Data{
		"worker":            r.workerName,
		"bridges":           len(bridges),
		"active_connections": stats.ActiveConnections,
	})

	// Placeholder for actual HTTP call
	// client := &http.Client{}
	// req, _ := http.NewRequestWithContext(ctx, "PUT", r.atcURL+"/api/v1/workers/"+r.workerName+"/relay", body)
	// resp, err := client.Do(req)

	return nil
}