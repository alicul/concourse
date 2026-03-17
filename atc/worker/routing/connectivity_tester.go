package routing

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
)

// DefaultConnectivityTester is the default implementation of ConnectivityTester
type DefaultConnectivityTester struct {
	logger                 lager.Logger
	httpClient             *http.Client
	networkTopologyFactory db.NetworkTopologyFactory
	timeout                time.Duration
}

// NewConnectivityTester creates a new connectivity tester
func NewConnectivityTester(
	logger lager.Logger,
	networkTopologyFactory db.NetworkTopologyFactory,
	timeout time.Duration,
) ConnectivityTester {
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	return &DefaultConnectivityTester{
		logger:                 logger.Session("connectivity-tester"),
		networkTopologyFactory: networkTopologyFactory,
		timeout:                timeout,
		httpClient: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				DisableKeepAlives: true,
				DialContext: (&net.Dialer{
					Timeout:   timeout,
					KeepAlive: 0,
				}).DialContext,
			},
		},
	}
}

// TestConnectivity tests connectivity between two workers
func (t *DefaultConnectivityTester) TestConnectivity(ctx context.Context, sourceWorker, destWorker string) (bool, time.Duration, error) {
	t.logger.Debug("testing-connectivity", lager.Data{
		"source": sourceWorker,
		"dest":   destWorker,
	})

	// Get destination worker's P2P endpoints
	destNetworks, err := t.networkTopologyFactory.GetWorkerNetworks(destWorker)
	if err != nil {
		return false, 0, fmt.Errorf("failed to get destination worker networks: %w", err)
	}

	if len(destNetworks) == 0 {
		return false, 0, fmt.Errorf("destination worker has no networks")
	}

	// Test connectivity to each endpoint
	var lastErr error
	bestLatency := time.Duration(0)
	canConnect := false

	for _, network := range destNetworks {
		ok, latency, err := t.TestEndpoint(ctx, network.P2PEndpoint)
		if err != nil {
			lastErr = err
			t.logger.Debug("endpoint-test-failed", lager.Data{
				"endpoint": network.P2PEndpoint,
				"error":    err.Error(),
			})
			continue
		}

		if ok {
			canConnect = true
			if bestLatency == 0 || latency < bestLatency {
				bestLatency = latency
			}
			t.logger.Debug("endpoint-reachable", lager.Data{
				"endpoint": network.P2PEndpoint,
				"latency":  latency,
			})
		}
	}

	if !canConnect && lastErr != nil {
		return false, 0, lastErr
	}

	return canConnect, bestLatency, nil
}

// TestEndpoint tests connectivity to a specific P2P endpoint
func (t *DefaultConnectivityTester) TestEndpoint(ctx context.Context, endpoint string) (bool, time.Duration, error) {
	t.logger.Debug("testing-endpoint", lager.Data{"endpoint": endpoint})

	// Parse the endpoint URL
	u, err := url.Parse(endpoint)
	if err != nil {
		return false, 0, fmt.Errorf("invalid endpoint URL: %w", err)
	}

	// Create a context with timeout
	testCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	startTime := time.Now()

	// Try TCP connection first (faster than HTTP)
	dialer := &net.Dialer{
		Timeout: t.timeout,
	}

	conn, err := dialer.DialContext(testCtx, "tcp", u.Host)
	if err != nil {
		return false, 0, fmt.Errorf("tcp connection failed: %w", err)
	}
	defer conn.Close()

	latency := time.Since(startTime)

	// Optionally, try an HTTP HEAD request to verify P2P endpoint
	// This is more thorough but slower
	if t.shouldVerifyHTTP() {
		healthURL := fmt.Sprintf("%s/healthz", endpoint)
		req, err := http.NewRequestWithContext(testCtx, "HEAD", healthURL, nil)
		if err != nil {
			// TCP worked, so consider it successful even if HTTP setup fails
			return true, latency, nil
		}

		resp, err := t.httpClient.Do(req)
		if err != nil {
			// TCP worked, so consider it successful even if HTTP fails
			t.logger.Debug("http-health-check-failed", lager.Data{
				"endpoint": endpoint,
				"error":    err.Error(),
			})
			return true, latency, nil
		}
		defer resp.Body.Close()

		// Update latency to include HTTP round-trip
		latency = time.Since(startTime)
	}

	t.logger.Debug("endpoint-test-success", lager.Data{
		"endpoint": endpoint,
		"latency":  latency,
	})

	return true, latency, nil
}

func (t *DefaultConnectivityTester) shouldVerifyHTTP() bool {
	// Could be made configurable
	// For now, skip HTTP verification for speed
	return false
}

// MockConnectivityTester is a mock implementation for testing
type MockConnectivityTester struct {
	Results map[string]mockResult
}

type mockResult struct {
	CanConnect bool
	Latency    time.Duration
	Error      error
}

func NewMockConnectivityTester() *MockConnectivityTester {
	return &MockConnectivityTester{
		Results: make(map[string]mockResult),
	}
}

func (m *MockConnectivityTester) SetResult(sourceWorker, destWorker string, canConnect bool, latency time.Duration, err error) {
	key := fmt.Sprintf("%s->%s", sourceWorker, destWorker)
	m.Results[key] = mockResult{
		CanConnect: canConnect,
		Latency:    latency,
		Error:      err,
	}
}

func (m *MockConnectivityTester) TestConnectivity(ctx context.Context, sourceWorker, destWorker string) (bool, time.Duration, error) {
	key := fmt.Sprintf("%s->%s", sourceWorker, destWorker)
	if result, ok := m.Results[key]; ok {
		return result.CanConnect, result.Latency, result.Error
	}
	return false, 0, fmt.Errorf("no mock result configured for %s", key)
}

func (m *MockConnectivityTester) TestEndpoint(ctx context.Context, endpoint string) (bool, time.Duration, error) {
	if result, ok := m.Results[endpoint]; ok {
		return result.CanConnect, result.Latency, result.Error
	}
	// Default to successful for endpoints
	return true, 100 * time.Millisecond, nil
}