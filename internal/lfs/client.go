// Package lfs provides a pure-HTTP client for the Git LFS Batch API.
// No git-lfs binary required. Uses the batch API for blob upload/download.
//
// The client implements the Git LFS Batch API spec:
// https://github.com/git-lfs/git-lfs/blob/main/docs/api/batch.md
package lfs

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sageox/ox/internal/useragent"
)

// Client communicates with a Git LFS Batch API server (e.g., GitLab).
type Client struct {
	batchURL   string // e.g., https://git.sageox.io/sageox/ledger.git/info/lfs/objects/batch
	httpClient *http.Client
	authHeader string // "Basic <base64(username:token)>"
}

// NewClient creates an LFS client for the given git repo URL.
// repoURL should be the git clone URL (e.g., https://git.sageox.io/sageox/ledger.git).
// Auth uses HTTP Basic per the Git LFS spec: username:token base64-encoded.
func NewClient(repoURL, username, token string) *Client {
	// Derive LFS batch endpoint from repo URL
	batchURL := strings.TrimSuffix(repoURL, "/") + "/info/lfs/objects/batch"

	// HTTP Basic auth header
	creds := username + ":" + token
	authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(creds))

	return &Client{
		batchURL: batchURL,
		httpClient: &http.Client{
			Timeout: 2 * time.Minute,
		},
		authHeader: authHeader,
	}
}

// BatchObject identifies a single LFS object by its SHA256 OID and size.
type BatchObject struct {
	OID  string `json:"oid"`  // SHA256 hex digest
	Size int64  `json:"size"` // bytes
}

// batchRequest is the JSON body for POST /info/lfs/objects/batch.
type batchRequest struct {
	Operation string        `json:"operation"` // "upload" or "download"
	Transfers []string      `json:"transfers"` // ["basic"]
	Objects   []BatchObject `json:"objects"`
}

// BatchResponse is the server response from the batch API.
type BatchResponse struct {
	Transfer string                `json:"transfer"` // "basic"
	Objects  []BatchResponseObject `json:"objects"`
}

// BatchResponseObject is a single object in the batch response.
type BatchResponseObject struct {
	OID           string       `json:"oid"`
	Size          int64        `json:"size"`
	Authenticated bool         `json:"authenticated,omitempty"`
	Actions       *Actions     `json:"actions,omitempty"`
	Error         *ObjectError `json:"error,omitempty"`
}

// Actions contains the upload/download actions returned by the batch API.
type Actions struct {
	Upload   *Action `json:"upload,omitempty"`
	Download *Action `json:"download,omitempty"`
	Verify   *Action `json:"verify,omitempty"`
}

// Action is a single LFS action with an href and optional headers.
type Action struct {
	Href      string            `json:"href"`
	Header    map[string]string `json:"header,omitempty"`
	ExpiresIn int               `json:"expires_in,omitempty"` // seconds
	ExpiresAt string            `json:"expires_at,omitempty"` // RFC3339
}

// ObjectError is returned when the server cannot process an object.
type ObjectError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// BatchUpload requests upload URLs for the given objects.
func (c *Client) BatchUpload(objects []BatchObject) (*BatchResponse, error) {
	return c.doBatch("upload", objects)
}

// BatchDownload requests download URLs for the given objects.
func (c *Client) BatchDownload(objects []BatchObject) (*BatchResponse, error) {
	return c.doBatch("download", objects)
}

// doBatch sends a batch request and returns the response.
func (c *Client) doBatch(operation string, objects []BatchObject) (*BatchResponse, error) {
	reqBody := batchRequest{
		Operation: operation,
		Transfers: []string{"basic"},
		Objects:   objects,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal batch request: %w", err)
	}

	req, err := http.NewRequest("POST", c.batchURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create batch request: %w", err)
	}

	req.Header.Set("Content-Type", "application/vnd.git-lfs+json")
	req.Header.Set("Accept", "application/vnd.git-lfs+json")
	useragent.SetHeaders(req.Header)
	req.Header.Set("Authorization", c.authHeader)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("batch request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read batch response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("LFS batch API returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var batchResp BatchResponse
	if err := json.Unmarshal(respBody, &batchResp); err != nil {
		return nil, fmt.Errorf("decode batch response: %w", err)
	}

	return &batchResp, nil
}
