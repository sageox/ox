package uxfriction

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/sageox/ox/internal/useragent"
)

// ClientConfig configures the friction client.
type ClientConfig struct {
	// Endpoint is the base URL for the friction API (e.g., "https://api.sageox.ai").
	Endpoint string

	// Version is the CLI version included in the "v" field of requests.
	Version string

	// AuthFunc returns a bearer token for authentication.
	// Called on each request. Return empty string for unauthenticated requests.
	AuthFunc func() string

	// HTTPClient is the HTTP client to use. If nil, http.DefaultClient is used.
	HTTPClient *http.Client

	// Timeout is the request timeout. Default is 5 seconds.
	Timeout time.Duration
}

// Client is the reference implementation for the friction API.
// It handles rate limiting via Retry-After and X-SageOx-Sample-Rate headers.
// Thread-safe for concurrent use.
type Client struct {
	config     ClientConfig
	sampleRate float64   // from X-SageOx-Sample-Rate (default 1.0)
	retryAfter time.Time // from Retry-After header
	mu         sync.RWMutex
}

// SubmitRequest is the request body for friction event submission.
type SubmitRequest struct {
	// Version is the CLI version.
	Version string `json:"v"`

	// Events is the list of friction events to submit.
	Events []FrictionEvent `json:"events"`
}

// SubmitResponse contains the response from the friction API.
type SubmitResponse struct {
	// StatusCode is the HTTP status code.
	StatusCode int

	// SampleRate is the updated sample rate from X-SageOx-Sample-Rate header.
	// -1 if not present in response.
	SampleRate float64

	// RetryAfter is the duration to wait before retrying, from Retry-After header.
	// Zero if not present in response.
	RetryAfter time.Duration

	// Catalog contains catalog data if updated or client version is stale.
	Catalog *CatalogData
}

// SubmitOptions contains optional parameters for Submit.
type SubmitOptions struct {
	// CatalogVersion is the current catalog version to send in X-Catalog-Version header.
	CatalogVersion string
}

// NewClient creates a new friction client with the given configuration.
func NewClient(cfg ClientConfig) *Client {
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &Client{
		config:     cfg,
		sampleRate: 1.0, // default: send all events
	}
}

// Submit sends friction events to the API.
// Returns the response and any error. Network errors are returned as-is.
// The caller should check ShouldSend() before calling Submit to respect rate limiting.
//
// Events are truncated to MaxEventsPerRequest (100) if more are provided.
// Pass nil for opts if no catalog version header is needed.
func (c *Client) Submit(ctx context.Context, events []FrictionEvent, opts *SubmitOptions) (*SubmitResponse, error) {
	if len(events) == 0 {
		// empty submission is a heartbeat - still valid
		events = []FrictionEvent{}
	}

	// truncate to max events per request
	if len(events) > MaxEventsPerRequest {
		events = events[:MaxEventsPerRequest]
	}

	// truncate each event's fields
	for i := range events {
		events[i].Truncate()
	}

	reqBody := SubmitRequest{
		Version: c.config.Version,
		Events:  events,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := c.config.Endpoint + "/api/v1/cli/friction"

	// create request with timeout
	reqCtx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()

	req, err := useragent.NewRequest(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-Version", c.config.Version)

	// add catalog version header if provided
	if opts != nil && opts.CatalogVersion != "" {
		req.Header.Set("X-Catalog-Version", opts.CatalogVersion)
	}

	// add auth header if available
	if c.config.AuthFunc != nil {
		if token := c.config.AuthFunc(); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	resp, err := c.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	result := &SubmitResponse{
		StatusCode: resp.StatusCode,
		SampleRate: -1, // indicates not present
	}

	// parse response headers and update internal state
	c.updateFromHeaders(resp.Header, result)

	// parse response body for catalog data
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		respBody, err := io.ReadAll(resp.Body)
		if err == nil && len(respBody) > 0 {
			var frictionResp FrictionResponse
			if json.Unmarshal(respBody, &frictionResp) == nil && frictionResp.Catalog != nil {
				result.Catalog = frictionResp.Catalog
			}
		}
	}

	return result, nil
}

// ShouldSend returns true if events should be sent based on current rate limiting.
// Checks both Retry-After and sample rate.
//
// Usage pattern:
//
//	if client.ShouldSend() {
//	    client.Submit(ctx, events, nil)
//	}
func (c *Client) ShouldSend() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// check retry-after
	if time.Now().Before(c.retryAfter) {
		return false
	}

	// check sample rate (probabilistic)
	if c.sampleRate <= 0 {
		return false
	}
	if c.sampleRate >= 1.0 {
		return true
	}

	return rand.Float64() < c.sampleRate
}

// SampleRate returns the current sample rate (0.0-1.0).
func (c *Client) SampleRate() float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sampleRate
}

// RetryAfter returns the time until which requests should be delayed.
// Returns zero time if no retry-after is active.
func (c *Client) RetryAfter() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.retryAfter
}

// updateFromHeaders parses response headers and updates internal state.
func (c *Client) updateFromHeaders(h http.Header, result *SubmitResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// parse X-SageOx-Sample-Rate
	if rateStr := h.Get("X-SageOx-Sample-Rate"); rateStr != "" {
		if rate, err := strconv.ParseFloat(rateStr, 64); err == nil {
			if rate >= 0 && rate <= 1.0 {
				c.sampleRate = rate
				result.SampleRate = rate
			}
		}
	}

	// parse Retry-After (seconds or HTTP-date)
	if retryStr := h.Get("Retry-After"); retryStr != "" {
		// try parsing as seconds first
		if seconds, err := strconv.Atoi(retryStr); err == nil {
			c.retryAfter = time.Now().Add(time.Duration(seconds) * time.Second)
			result.RetryAfter = time.Duration(seconds) * time.Second
		} else if t, err := http.ParseTime(retryStr); err == nil {
			// try HTTP-date format
			c.retryAfter = t
			result.RetryAfter = time.Until(t)
		}
	}
}

// Reset clears rate limiting state (sample rate back to 1.0, retry-after cleared).
// Use this when the daemon restarts.
func (c *Client) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sampleRate = 1.0
	c.retryAfter = time.Time{}
}

// MaxEventsPerRequest is the maximum number of events per submission.
// Events beyond this limit are silently dropped.
const MaxEventsPerRequest = 100
