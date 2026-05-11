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
	"net/url"
	"strings"
	"time"

	"github.com/sageox/ox/internal/gitserver"
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
//
// TrustedHost and TrustedScheme are populated by doBatch from the batch
// URL the action came from, so consumers (Upload/Download/Verify) can
// reject hrefs whose host doesn't match the LFS server we just talked
// to. Per ox-90gh: without this, a compromised LFS server can hand us
// action.Href = https://attacker.example/leak with action.Header carrying
// our bearer token, and the default http.Client follows redirects
// blindly. The fields are zero-valued when actions are constructed by
// callers outside doBatch (tests); the validation helpers below default
// to "no constraint" in that case.
type Action struct {
	Href      string            `json:"href"`
	Header    map[string]string `json:"header,omitempty"`
	ExpiresIn int               `json:"expires_in,omitempty"` // seconds
	ExpiresAt string            `json:"expires_at,omitempty"` // RFC3339

	// TrustedScheme is "https" for actions returned by an https batch URL.
	// Non-https schemes are rejected outside of loopback addresses.
	TrustedScheme string `json:"-"`
	// TrustedHost is the host:port of the batch URL the action came from.
	// Lowercased so comparisons are case-insensitive.
	TrustedHost string `json:"-"`
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
	// Fail closed before sending Authorization. The batch URL is built
	// from the git remote URL in NewClient; if someone misconfigures or
	// MITM-rewrites the remote to plaintext http://example.com, this
	// guard prevents shipping Basic auth (the user's PAT) over the
	// wire. http is only allowed for loopback hosts so httptest servers
	// keep working without TLS.
	batchU, parseErr := url.Parse(c.batchURL)
	if parseErr != nil {
		return nil, fmt.Errorf("batch url parse: %w", parseErr)
	}
	if strings.EqualFold(batchU.Scheme, "http") && !isLoopbackHost(batchU.Hostname()) {
		return nil, fmt.Errorf("batch url %q uses plaintext http on a non-loopback host; refusing to send Authorization", c.batchURL)
	}

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
	// only User-Agent for external Git host; no X-Orchestrator
	req.Header.Set("User-Agent", useragent.String())
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

	// Stamp every action in the response with the trusted host derived
	// from the batch URL. Per ox-90gh, consumers use this to reject
	// hrefs that point at attacker-controlled hosts. batchU was already
	// parsed at the top of this function for the plaintext-auth guard.
	scheme := strings.ToLower(batchU.Scheme)
	host := strings.ToLower(batchU.Host)
	stamp := func(a *Action) {
		if a == nil {
			return
		}
		a.TrustedScheme = scheme
		a.TrustedHost = host
	}
	for i := range batchResp.Objects {
		if batchResp.Objects[i].Actions == nil {
			continue
		}
		stamp(batchResp.Objects[i].Actions.Upload)
		stamp(batchResp.Objects[i].Actions.Download)
		stamp(batchResp.Objects[i].Actions.Verify)
	}

	return &batchResp, nil
}

// NewClientFromLedger creates an LFS client using the ledger's git remote URL
// and credentials loaded for the given endpoint. This is a convenience constructor
// for callers that already have the ledger path (e.g., daemon session finalization).
func NewClientFromLedger(ledgerPath, endpointURL string) (*Client, error) {
	creds, err := gitserver.LoadCredentialsForEndpoint(endpointURL)
	if err != nil {
		return nil, fmt.Errorf("load credentials: %w", err)
	}
	if creds == nil {
		return nil, fmt.Errorf("no git credentials found (run 'ox login' first)")
	}
	if creds.Token == "" {
		return nil, fmt.Errorf("git credentials have empty token")
	}

	repoURL, err := gitserver.GetBareRemoteURL(ledgerPath)
	if err != nil {
		return nil, fmt.Errorf("get ledger remote URL: %w", err)
	}
	if repoURL == "" {
		return nil, fmt.Errorf("ledger has no remote URL configured")
	}

	return NewClient(repoURL, creds.Username, creds.Token), nil
}
