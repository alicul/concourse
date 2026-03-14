package volume

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/worker/baggageclaim"
)

// RelayStreamer handles P2P streaming through relay workers
type RelayStreamer interface {
	// StreamThroughRelay streams a volume from source to destination through this relay
	StreamThroughRelay(ctx context.Context, sourceURL, destURL string, encoding baggageclaim.Encoding) error
	// ProxyStream proxies a stream between two workers
	ProxyStream(ctx context.Context, req *http.Request, w http.ResponseWriter) error
}

// RelayStreamerImpl implements RelayStreamer
type RelayStreamerImpl struct {
	logger     lager.Logger
	httpClient *http.Client
	maxSize    int64
}

// NewRelayStreamer creates a new relay streamer
func NewRelayStreamer(logger lager.Logger, maxSize int64) RelayStreamer {
	return &RelayStreamerImpl{
		logger: logger,
		httpClient: &http.Client{
			Timeout: 30 * time.Minute, // Long timeout for large volumes
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		maxSize: maxSize,
	}
}

// StreamThroughRelay streams a volume through this relay worker
func (r *RelayStreamerImpl) StreamThroughRelay(
	ctx context.Context,
	sourceURL, destURL string,
	encoding baggageclaim.Encoding,
) error {
	ctx, span := tracing.StartSpan(ctx, "relay.StreamThrough", tracing.Attrs{
		"source_url": sourceURL,
		"dest_url":   destURL,
		"encoding":   string(encoding),
	})
	defer span.End()

	logger := r.logger.Session("stream-through-relay", lager.Data{
		"source": sourceURL,
		"dest":   destURL,
	})
	logger.Debug("start")
	defer logger.Debug("done")

	// Step 1: Initiate stream-out from source
	sourceReq, err := http.NewRequestWithContext(ctx, "GET", sourceURL, nil)
	if err != nil {
		logger.Error("failed-to-create-source-request", err)
		return fmt.Errorf("failed to create source request: %w", err)
	}

	// Add encoding header
	sourceReq.Header.Set("Accept-Encoding", string(encoding))

	// Get stream from source
	sourceResp, err := r.httpClient.Do(sourceReq)
	if err != nil {
		logger.Error("failed-to-get-source-stream", err)
		return fmt.Errorf("failed to get source stream: %w", err)
	}
	defer sourceResp.Body.Close()

	if sourceResp.StatusCode != http.StatusOK {
		logger.Error("source-returned-error", nil, lager.Data{
			"status": sourceResp.StatusCode,
		})
		return fmt.Errorf("source returned status %d", sourceResp.StatusCode)
	}

	// Step 2: Stream to destination
	destReq, err := http.NewRequestWithContext(ctx, "PUT", destURL, sourceResp.Body)
	if err != nil {
		logger.Error("failed-to-create-dest-request", err)
		return fmt.Errorf("failed to create dest request: %w", err)
	}

	// Set content type and encoding
	destReq.Header.Set("Content-Type", "application/octet-stream")
	destReq.Header.Set("Content-Encoding", string(encoding))

	// Copy content length if known
	if sourceResp.ContentLength > 0 {
		destReq.ContentLength = sourceResp.ContentLength
	}

	// Stream to destination
	destResp, err := r.httpClient.Do(destReq)
	if err != nil {
		logger.Error("failed-to-stream-to-dest", err)
		return fmt.Errorf("failed to stream to destination: %w", err)
	}
	defer destResp.Body.Close()

	if destResp.StatusCode != http.StatusOK && destResp.StatusCode != http.StatusNoContent {
		logger.Error("dest-returned-error", nil, lager.Data{
			"status": destResp.StatusCode,
		})
		return fmt.Errorf("destination returned status %d", destResp.StatusCode)
	}

	logger.Info("relay-stream-completed")
	return nil
}

// ProxyStream proxies a stream request between two workers
func (r *RelayStreamerImpl) ProxyStream(ctx context.Context, req *http.Request, w http.ResponseWriter) error {
	logger := r.logger.Session("proxy-stream", lager.Data{
		"method": req.Method,
		"url":    req.URL.String(),
	})
	logger.Debug("start")
	defer logger.Debug("done")

	// Extract target URL from request
	targetURL := req.URL.Query().Get("target")
	if targetURL == "" {
		logger.Error("missing-target-url", nil)
		http.Error(w, "missing target URL", http.StatusBadRequest)
		return fmt.Errorf("missing target URL")
	}

	// Parse and validate target URL
	parsedURL, err := url.Parse(targetURL)
	if err != nil {
		logger.Error("invalid-target-url", err)
		http.Error(w, "invalid target URL", http.StatusBadRequest)
		return fmt.Errorf("invalid target URL: %w", err)
	}

	// Create proxy request
	proxyReq, err := http.NewRequestWithContext(ctx, req.Method, parsedURL.String(), req.Body)
	if err != nil {
		logger.Error("failed-to-create-proxy-request", err)
		http.Error(w, "failed to create proxy request", http.StatusInternalServerError)
		return fmt.Errorf("failed to create proxy request: %w", err)
	}

	// Copy headers
	for key, values := range req.Header {
		// Skip hop-by-hop headers
		if isHopByHopHeader(key) {
			continue
		}
		for _, value := range values {
			proxyReq.Header.Add(key, value)
		}
	}

	// Make the proxy request
	proxyResp, err := r.httpClient.Do(proxyReq)
	if err != nil {
		logger.Error("proxy-request-failed", err)
		http.Error(w, "proxy request failed", http.StatusBadGateway)
		return fmt.Errorf("proxy request failed: %w", err)
	}
	defer proxyResp.Body.Close()

	// Copy response headers
	for key, values := range proxyResp.Header {
		if isHopByHopHeader(key) {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// Set status code
	w.WriteHeader(proxyResp.StatusCode)

	// Copy response body with size limit
	limitedReader := &limitedReader{
		Reader:  proxyResp.Body,
		Limit:   r.maxSize,
		read:    0,
	}

	written, err := io.Copy(w, limitedReader)
	if err != nil {
		logger.Error("failed-to-copy-response", err)
		return fmt.Errorf("failed to copy response: %w", err)
	}

	logger.Info("proxy-completed", lager.Data{
		"bytes_written": written,
		"status":        proxyResp.StatusCode,
	})

	return nil
}

// isHopByHopHeader checks if a header is hop-by-hop
func isHopByHopHeader(header string) bool {
	hopByHopHeaders := []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"TE",
		"Trailers",
		"Transfer-Encoding",
		"Upgrade",
	}

	for _, h := range hopByHopHeaders {
		if header == h {
			return true
		}
	}
	return false
}

// limitedReader limits the amount of data read
type limitedReader struct {
	io.Reader
	Limit int64
	read  int64
}

func (r *limitedReader) Read(p []byte) (n int, err error) {
	if r.Limit > 0 && r.read >= r.Limit {
		return 0, fmt.Errorf("read limit exceeded")
	}

	remaining := r.Limit - r.read
	if r.Limit > 0 && int64(len(p)) > remaining {
		p = p[:remaining]
	}

	n, err = r.Reader.Read(p)
	r.read += int64(n)
	return n, err
}

// RelayEndpoints provides endpoints for relay operations
type RelayEndpoints struct {
	streamer RelayStreamer
	logger   lager.Logger
}

// NewRelayEndpoints creates new relay endpoints
func NewRelayEndpoints(streamer RelayStreamer, logger lager.Logger) *RelayEndpoints {
	return &RelayEndpoints{
		streamer: streamer,
		logger:   logger,
	}
}

// HandleRelayStream handles HTTP requests for relay streaming
func (e *RelayEndpoints) HandleRelayStream(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	logger := e.logger.Session("handle-relay-stream")

	// Extract source and destination URLs from request
	sourceURL := req.URL.Query().Get("source")
	destURL := req.URL.Query().Get("dest")
	encoding := req.URL.Query().Get("encoding")

	if sourceURL == "" || destURL == "" {
		logger.Error("missing-parameters", nil)
		http.Error(w, "missing source or dest URL", http.StatusBadRequest)
		return
	}

	if encoding == "" {
		encoding = string(baggageclaim.GzipEncoding)
	}

	// Perform relay streaming
	err := e.streamer.StreamThroughRelay(ctx, sourceURL, destURL, baggageclaim.Encoding(encoding))
	if err != nil {
		logger.Error("relay-stream-failed", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("relay stream completed"))
}

// HandleProxyStream handles HTTP proxy requests
func (e *RelayEndpoints) HandleProxyStream(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	err := e.streamer.ProxyStream(ctx, req, w)
	if err != nil {
		e.logger.Error("proxy-stream-failed", err)
		// Error already written to response
	}
}