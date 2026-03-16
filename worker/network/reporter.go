package network

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
)

// Reporter reports network topology information to the ATC
type Reporter struct {
	logger       lager.Logger
	detector     *Detector
	atcURL       string
	workerName   string
	httpClient   *http.Client
	interval     time.Duration
}

// NewReporter creates a new network topology reporter
func NewReporter(
	logger lager.Logger,
	detector *Detector,
	atcURL string,
	workerName string,
	interval time.Duration,
) *Reporter {
	return &Reporter{
		logger:     logger.Session("network-reporter"),
		detector:   detector,
		atcURL:     atcURL,
		workerName: workerName,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		interval: interval,
	}
}

// Run starts the network topology reporter
func (r *Reporter) Run(ctx context.Context) error {
	r.logger.Info("starting-network-topology-reporter", lager.Data{
		"worker":   r.workerName,
		"interval": r.interval,
	})

	// Report immediately on startup
	if err := r.reportNetworkTopology(ctx); err != nil {
		r.logger.Error("initial-report-failed", err)
	}

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("stopping-network-topology-reporter")
			return ctx.Err()
		case <-ticker.C:
			if err := r.reportNetworkTopology(ctx); err != nil {
				r.logger.Error("report-network-topology-failed", err)
				// Continue reporting even if one attempt fails
			}
		}
	}
}

// reportNetworkTopology detects and reports the current network topology
func (r *Reporter) reportNetworkTopology(ctx context.Context) error {
	r.logger.Debug("reporting-network-topology")

	// Detect current network segments
	segments, err := r.detector.DetectNetworkSegments(ctx)
	if err != nil {
		return fmt.Errorf("failed to detect network segments: %w", err)
	}

	if len(segments) == 0 {
		r.logger.Info("no-network-segments-detected")
		return nil
	}

	// Convert to database types
	var workerNetworks []db.WorkerNetwork
	for _, segment := range segments {
		wn := db.WorkerNetwork{
			WorkerName:    r.workerName,
			SegmentID:     segment.ID,
			P2PEndpoint:   segment.P2PEndpoint,
			InterfaceName: segment.InterfaceName,
			IPAddress:     segment.IPAddress,
			LastUpdated:   time.Now(),
		}
		workerNetworks = append(workerNetworks, wn)
	}

	// Send to ATC
	if err := r.sendNetworkUpdate(ctx, workerNetworks); err != nil {
		return fmt.Errorf("failed to send network update: %w", err)
	}

	r.logger.Info("reported-network-topology", lager.Data{
		"worker":   r.workerName,
		"segments": len(segments),
	})

	return nil
}

// sendNetworkUpdate sends the network topology update to the ATC
func (r *Reporter) sendNetworkUpdate(ctx context.Context, networks []db.WorkerNetwork) error {
	payload, err := json.Marshal(map[string]interface{}{
		"worker_name": r.workerName,
		"networks":    networks,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal network update: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/workers/%s/networks", r.atcURL, r.workerName)
	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

// TestAndReportConnectivity tests connectivity to other workers and reports results
func (r *Reporter) TestAndReportConnectivity(ctx context.Context, targetWorkers []string) error {
	r.logger.Debug("testing-connectivity", lager.Data{"targets": targetWorkers})

	var connectivityResults []db.WorkerConnectivity

	for _, target := range targetWorkers {
		if target == r.workerName {
			continue // Skip self
		}

		// Get target worker's P2P endpoints
		endpoints, err := r.getWorkerP2PEndpoints(ctx, target)
		if err != nil {
			r.logger.Error("failed-to-get-p2p-endpoints", err, lager.Data{"target": target})
			continue
		}

		// Test connectivity to each endpoint
		for _, endpoint := range endpoints {
			canConnect, latencyMs, err := r.detector.TestConnectivity(ctx, endpoint)

			result := db.WorkerConnectivity{
				SourceWorker: r.workerName,
				DestWorker:   target,
				CanConnect:   canConnect,
				LatencyMs:    latencyMs,
				LastTested:   time.Now(),
			}

			if err != nil {
				result.TestError = err.Error()
			}

			connectivityResults = append(connectivityResults, result)

			r.logger.Info("connectivity-test-result", lager.Data{
				"target":      target,
				"endpoint":    endpoint,
				"can_connect": canConnect,
				"latency_ms":  latencyMs,
				"error":       err,
			})
		}
	}

	// Report results to ATC
	if len(connectivityResults) > 0 {
		return r.sendConnectivityUpdate(ctx, connectivityResults)
	}

	return nil
}

// getWorkerP2PEndpoints gets P2P endpoints for a worker from ATC
func (r *Reporter) getWorkerP2PEndpoints(ctx context.Context, workerName string) ([]string, error) {
	url := fmt.Sprintf("%s/api/v1/workers/%s/p2p-urls", r.atcURL, workerName)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var result struct {
		Endpoints []struct {
			URL string `json:"url"`
		} `json:"endpoints"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var urls []string
	for _, ep := range result.Endpoints {
		urls = append(urls, ep.URL)
	}

	return urls, nil
}

// sendConnectivityUpdate sends connectivity test results to ATC
func (r *Reporter) sendConnectivityUpdate(ctx context.Context, results []db.WorkerConnectivity) error {
	payload, err := json.Marshal(map[string]interface{}{
		"source_worker": r.workerName,
		"results":       results,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal connectivity update: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/workers/%s/connectivity", r.atcURL, r.workerName)
	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}