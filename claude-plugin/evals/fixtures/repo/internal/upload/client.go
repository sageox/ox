// Package upload pushes artifacts to the Acme artifact store.
package upload

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
)

// Client uploads artifacts to the store at BaseURL.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewClient returns a client for the given artifact store.
func NewClient(baseURL string) *Client {
	return &Client{BaseURL: baseURL, HTTP: http.DefaultClient}
}

// Upload sends the file at path to the store. It makes exactly one attempt;
// transient store errors surface directly to the caller.
func (c *Client) Upload(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	resp, err := c.HTTP.Post(c.BaseURL+"/artifacts", "application/octet-stream", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("post artifact: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("artifact store returned %d", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("artifact rejected: %d", resp.StatusCode)
	}
	return nil
}
