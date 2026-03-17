package relay

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/metric"
)

// Proxy handles P2P stream relaying between workers
type Proxy struct {
	logger             lager.Logger
	workerName         string
	maxConnections     int
	bandwidthLimitMbps int
	activeConnections  int64
	totalBytesRelayed  int64
	mu                 sync.RWMutex
	connections        map[string]*RelayConnection
}

// RelayConnection represents an active relay connection
type RelayConnection struct {
	ID          string
	SourceWorker string
	DestWorker   string
	StartTime    time.Time
	BytesRelayed int64
	Status       string
}

// NewProxy creates a new relay proxy
func NewProxy(
	logger lager.Logger,
	workerName string,
	maxConnections int,
	bandwidthLimitMbps int,
) *Proxy {
	return &Proxy{
		logger:             logger.Session("relay-proxy"),
		workerName:         workerName,
		maxConnections:     maxConnections,
		bandwidthLimitMbps: bandwidthLimitMbps,
		connections:        make(map[string]*RelayConnection),
	}
}

// RelayStream proxies a P2P stream from source to destination
func (p *Proxy) RelayStream(
	ctx context.Context,
	sourceURL string,
	destURL string,
	volumeID string,
	metadata map[string]string,
) error {
	// Check if we've reached max connections
	if !p.canAcceptConnection() {
		p.logger.Info("max-connections-reached", lager.Data{
			"current": atomic.LoadInt64(&p.activeConnections),
			"max":     p.maxConnections,
		})
		return fmt.Errorf("relay worker at maximum capacity")
	}

	// Create relay connection record
	connectionID := fmt.Sprintf("%s-%s-%d", volumeID, p.workerName, time.Now().UnixNano())
	conn := &RelayConnection{
		ID:           connectionID,
		SourceWorker: metadata["source_worker"],
		DestWorker:   metadata["dest_worker"],
		StartTime:    time.Now(),
		Status:       "active",
	}

	// Register connection
	p.registerConnection(conn)
	defer p.unregisterConnection(connectionID)

	// Increment active connections counter
	atomic.AddInt64(&p.activeConnections, 1)
	defer atomic.AddInt64(&p.activeConnections, -1)

	// Emit relay start metric
	metric.RelayStreamingStarted.Inc()
	metric.RelayStreamingInProgress.Inc()
	defer metric.RelayStreamingInProgress.Dec()

	startTime := time.Now()
	p.logger.Info("starting-relay", lager.Data{
		"connection_id": connectionID,
		"source":        sourceURL,
		"destination":   destURL,
		"volume_id":     volumeID,
	})

	// Perform the relay
	bytesRelayed, err := p.performRelay(ctx, sourceURL, destURL, connectionID)

	duration := time.Since(startTime)

	// Update connection stats
	conn.BytesRelayed = bytesRelayed
	atomic.AddInt64(&p.totalBytesRelayed, bytesRelayed)

	// Emit metrics
	if err != nil {
		conn.Status = "failed"
		metric.RelayStreamingFailure.Inc()
		metric.RelayStreamingDuration.WithLabelValues("failed").Observe(duration.Seconds())
		p.logger.Error("relay-failed", err, lager.Data{
			"connection_id": connectionID,
			"bytes_relayed": bytesRelayed,
			"duration_ms":   duration.Milliseconds(),
		})
		return fmt.Errorf("relay streaming failed: %w", err)
	}

	conn.Status = "completed"
	metric.RelayStreamingSuccess.Inc()
	metric.RelayStreamingDuration.WithLabelValues("success").Observe(duration.Seconds())
	metric.RelayStreamingBytes.Observe(float64(bytesRelayed))

	p.logger.Info("relay-completed", lager.Data{
		"connection_id": connectionID,
		"bytes_relayed": bytesRelayed,
		"duration_ms":   duration.Milliseconds(),
		"throughput_mbps": float64(bytesRelayed*8) / (duration.Seconds() * 1e6),
	})

	return nil
}

// performRelay does the actual streaming from source to destination
func (p *Proxy) performRelay(ctx context.Context, sourceURL, destURL, connectionID string) (int64, error) {
	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: 30 * time.Minute, // Long timeout for large volumes
	}

	// Start streaming from source
	sourceReq, err := http.NewRequestWithContext(ctx, "GET", sourceURL, nil)
	if err != nil {
		return 0, fmt.Errorf("creating source request: %w", err)
	}

	sourceResp, err := client.Do(sourceReq)
	if err != nil {
		return 0, fmt.Errorf("connecting to source: %w", err)
	}
	defer sourceResp.Body.Close()

	if sourceResp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("source returned status %d", sourceResp.StatusCode)
	}

	// Create pipe for streaming with bandwidth limiting if configured
	var reader io.Reader = sourceResp.Body
	if p.bandwidthLimitMbps > 0 {
		reader = NewBandwidthLimiter(reader, p.bandwidthLimitMbps)
	}

	// Create destination request
	destReq, err := http.NewRequestWithContext(ctx, "PUT", destURL, reader)
	if err != nil {
		return 0, fmt.Errorf("creating destination request: %w", err)
	}

	// Set content length if known
	if sourceResp.ContentLength > 0 {
		destReq.ContentLength = sourceResp.ContentLength
	}

	// Stream to destination
	destResp, err := client.Do(destReq)
	if err != nil {
		return 0, fmt.Errorf("streaming to destination: %w", err)
	}
	defer destResp.Body.Close()

	if destResp.StatusCode != http.StatusOK && destResp.StatusCode != http.StatusCreated {
		return 0, fmt.Errorf("destination returned status %d", destResp.StatusCode)
	}

	// Track bytes relayed (assuming successful completion)
	// In a real implementation, we'd track this during the streaming
	bytesRelayed := sourceResp.ContentLength
	if bytesRelayed < 0 {
		// Content-Length not provided, estimate from connection tracking
		bytesRelayed = p.getConnectionBytes(connectionID)
	}

	return bytesRelayed, nil
}

// canAcceptConnection checks if proxy can accept a new connection
func (p *Proxy) canAcceptConnection() bool {
	current := atomic.LoadInt64(&p.activeConnections)
	return current < int64(p.maxConnections)
}

// registerConnection registers a new relay connection
func (p *Proxy) registerConnection(conn *RelayConnection) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.connections[conn.ID] = conn
}

// unregisterConnection removes a relay connection
func (p *Proxy) unregisterConnection(connectionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.connections, connectionID)
}

// getConnectionBytes gets bytes relayed for a connection
func (p *Proxy) getConnectionBytes(connectionID string) int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if conn, ok := p.connections[connectionID]; ok {
		return conn.BytesRelayed
	}
	return 0
}

// GetStats returns current proxy statistics
func (p *Proxy) GetStats() ProxyStats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	activeConns := make([]RelayConnection, 0, len(p.connections))
	for _, conn := range p.connections {
		activeConns = append(activeConns, *conn)
	}

	return ProxyStats{
		ActiveConnections: int(atomic.LoadInt64(&p.activeConnections)),
		TotalBytesRelayed: atomic.LoadInt64(&p.totalBytesRelayed),
		Connections:       activeConns,
		MaxConnections:    p.maxConnections,
		BandwidthLimitMbps: p.bandwidthLimitMbps,
	}
}

// ProxyStats contains proxy statistics
type ProxyStats struct {
	ActiveConnections  int
	TotalBytesRelayed  int64
	Connections        []RelayConnection
	MaxConnections     int
	BandwidthLimitMbps int
}

// BandwidthLimiter implements bandwidth limiting for relay streams
type BandwidthLimiter struct {
	reader     io.Reader
	rateLimit  int64 // bytes per second
	lastRead   time.Time
	bucket     int64 // token bucket for rate limiting
}

// NewBandwidthLimiter creates a new bandwidth limiter
func NewBandwidthLimiter(reader io.Reader, limitMbps int) io.Reader {
	return &BandwidthLimiter{
		reader:    reader,
		rateLimit: int64(limitMbps * 1024 * 1024 / 8), // Convert Mbps to bytes/sec
		lastRead:  time.Now(),
		bucket:    0,
	}
}

// Read implements io.Reader with bandwidth limiting
func (bl *BandwidthLimiter) Read(p []byte) (n int, err error) {
	// Calculate tokens to add to bucket based on time elapsed
	now := time.Now()
	elapsed := now.Sub(bl.lastRead)
	bl.lastRead = now

	tokensToAdd := int64(elapsed.Seconds() * float64(bl.rateLimit))
	bl.bucket += tokensToAdd

	// Cap bucket at rate limit to prevent burst accumulation
	if bl.bucket > bl.rateLimit {
		bl.bucket = bl.rateLimit
	}

	// Determine how many bytes we can read
	canRead := len(p)
	if int64(canRead) > bl.bucket {
		canRead = int(bl.bucket)
	}

	if canRead == 0 {
		// Need to wait for tokens
		time.Sleep(10 * time.Millisecond)
		return 0, nil
	}

	// Read up to allowed amount
	n, err = bl.reader.Read(p[:canRead])
	bl.bucket -= int64(n)

	return n, err
}