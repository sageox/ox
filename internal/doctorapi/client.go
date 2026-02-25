package doctorapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sageox/ox/internal/useragent"
)

// ClientConfig configures the doctor API client.
type ClientConfig struct {
	Endpoint   string
	AuthFunc   func() string
	HTTPClient *http.Client
	Timeout    time.Duration
}

// Client is the doctor API client.
type Client struct {
	config ClientConfig
}

// NewClient creates a new doctor API client.
func NewClient(cfg ClientConfig) *Client {
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &Client{config: cfg}
}

// GetContext fetches server-side context for troubleshooting.
func (c *Client) GetContext(ctx context.Context) (*DoctorContextResponse, error) {
	url := c.config.Endpoint + "/api/v1/cli/doctor/context"

	reqCtx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()

	req, err := useragent.NewRequest(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

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

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("unauthorized - login required")
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var result DoctorContextResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}
