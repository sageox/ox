package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
	"github.com/sageox/ox/internal/version"
)

// KB API endpoint paths.
const (
	kbListPath   = "/api/v1/kb"    // GET — list all knowledge bubbles the caller can access
	kbDetailPath = "/api/v1/kb/%s" // GET — fetch a single bubble by kb_id (or slug, server resolves)
)

// ErrKBAPIUnavailable is the sentinel returned when the KB endpoint is not
// available to the caller (HTTP 403/404). This is non-fatal by design: the kb
// API is one of three sources merged in internal/kb/merge.go, and a missing
// or flag-gated endpoint must NOT fail the whole listing. Callers should
// inspect with errors.Is and treat it as "kb-API source returned 0 rows".
var ErrKBAPIUnavailable = errors.New("kb API unavailable for this caller")

// KBType matches the sageox-mono KBType enum (ADR-028 / ADR-036). Five known
// kinds plus a client-side "unknown" fallback bucket for forward compatibility
// when the server rolls out a sixth type before the CLI knows about it.
type KBType string

const (
	KBTypePersonal KBType = "personal"
	KBTypeProfile  KBType = "profile"
	KBTypeTeam     KBType = "team"
	KBTypeRepo     KBType = "repo"
	KBTypeCustom   KBType = "custom"
	// KBTypeUnknown is the client-side fallback bucket when the server
	// returns a kb_type the CLI doesn't recognize. Never sent by the server.
	KBTypeUnknown KBType = "unknown"
)

// KB represents a knowledge bubble row from GET /api/v1/kb (list) or
// GET /api/v1/kb/{id} (detail). Field set tracks the sageox-mono response
// schema; new server fields can be added without breaking older clients
// because the JSON decoder ignores unknown keys by default.
type KB struct {
	KBID           string `json:"kb_id"`
	KBType         KBType `json:"kb_type"`
	Slug           string `json:"slug"`
	Name           string `json:"name"`
	OwnerUserID    string `json:"owner_user_id,omitempty"`
	LifecycleState string `json:"lifecycle_state,omitempty"` // e.g. "active", "provision-failed"
	ViewerRole     string `json:"viewer_role,omitempty"`     // e.g. "owner", "member", "viewer"
	RepoURL        string `json:"repo_url,omitempty"`        // git clone URL when provisioned
	CreatedAt      string `json:"created_at,omitempty"`      // RFC3339 timestamp
	UpdatedAt      string `json:"updated_at,omitempty"`      // RFC3339 timestamp

	// RepoID scopes a kb_type=repo bubble to a specific project. When set,
	// the per-project symlink reconciler only links this bubble into the
	// project whose ProjectConfig.RepoID matches. Empty for non-repo
	// bubbles. May also be empty on legacy/un-migrated repo bubbles —
	// those are skipped by the symlink reconciler since there's no way
	// to know which project they belong to.
	RepoID string `json:"repo_id,omitempty"`
}

// kbListResponse is the envelope shape for GET /api/v1/kb. The server may
// either return a bare JSON array or an object with a "bubbles" key; this
// struct supports the object form, and ListBubbles falls back to bare-array
// decoding when the object form fails to populate.
type kbListResponse struct {
	Bubbles []KB `json:"bubbles"`
}

// KBClient handles API communication with the SageOx kb endpoints.
// Mirrors the construction style of RepoClient (see repo.go).
type KBClient struct {
	baseURL    string
	httpClient *http.Client
	authToken  string
	version    string
}

// NewKBClient creates a kb API client using the global default endpoint.
// Prefer NewKBClientForProject when called from a repo context so the
// project-configured endpoint is honored.
func NewKBClient() *KBClient {
	return &KBClient{
		baseURL:    endpoint.Get(),
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    version.Version,
	}
}

// NewKBClientForProject creates a kb API client using the endpoint resolved
// from project config (env var > project config > default), matching the
// canonical helper used elsewhere in the codebase.
func NewKBClientForProject(gitRoot string) *KBClient {
	return &KBClient{
		baseURL:    endpoint.GetForProject(gitRoot),
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    version.Version,
	}
}

// NewKBClientWithEndpoint creates a kb API client with an explicit base URL.
// Used by tests and code paths that already have the endpoint resolved.
func NewKBClientWithEndpoint(baseURL string) *KBClient {
	return &KBClient{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    version.Version,
	}
}

// WithAuthToken sets the bearer token for authenticated requests and returns
// the client for fluent chaining.
func (c *KBClient) WithAuthToken(token string) *KBClient {
	c.authToken = token
	return c
}

// Endpoint returns the base URL this client is configured for.
func (c *KBClient) Endpoint() string {
	return c.baseURL
}

// ListBubbles calls GET /api/v1/kb to list knowledge bubbles the caller can
// access. A 403 or 404 maps to ErrKBAPIUnavailable (non-fatal — see the
// sentinel doc). Other non-2xx statuses return a wrapped error.
func (c *KBClient) ListBubbles(ctx context.Context) ([]KB, error) {
	reqURL := strings.TrimSuffix(c.baseURL, "/") + kbListPath

	bodyBytes, err := c.do(ctx, "GET", reqURL)
	if err != nil {
		return nil, err
	}

	// prefer the envelope shape; if it decodes empty, retry as a bare array
	var envelope kbListResponse
	if err := json.Unmarshal(bodyBytes, &envelope); err == nil && envelope.Bubbles != nil {
		return c.normalizeTypes(envelope.Bubbles), nil
	}

	var bare []KB
	if err := json.Unmarshal(bodyBytes, &bare); err != nil {
		return nil, fmt.Errorf("failed to decode kb list response: %w", err)
	}
	return c.normalizeTypes(bare), nil
}

// GetBubble calls GET /api/v1/kb/{id} for a single bubble. The id may be a
// kb_id or a slug — server is responsible for resolving. 403/404 → sentinel.
func (c *KBClient) GetBubble(ctx context.Context, kbID string) (*KB, error) {
	if strings.TrimSpace(kbID) == "" {
		return nil, fmt.Errorf("kb id is required")
	}

	reqURL := strings.TrimSuffix(c.baseURL, "/") + fmt.Sprintf(kbDetailPath, kbID)

	bodyBytes, err := c.do(ctx, "GET", reqURL)
	if err != nil {
		return nil, err
	}

	var bubble KB
	if err := json.Unmarshal(bodyBytes, &bubble); err != nil {
		return nil, fmt.Errorf("failed to decode kb response: %w", err)
	}
	bubble.KBType = normalizeKBType(bubble.KBType)
	return &bubble, nil
}

// do issues the HTTP request, applies standard auth+UA headers, and returns
// the response body bytes on 2xx. Non-2xx and network/version errors are
// translated into the package's typed errors.
func (c *KBClient) do(ctx context.Context, method, reqURL string) ([]byte, error) {
	logger.LogHTTPRequest(method, reqURL)
	start := time.Now()

	httpReq, err := useragent.NewRequest(ctx, method, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.authToken))
	}

	resp, err := c.httpClient.Do(httpReq)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError(method, reqURL, err, duration)
		return nil, fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse(method, reqURL, resp.StatusCode, duration)

	if CheckVersionResponse(resp) {
		return nil, ErrVersionUnsupported
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 403 and 404 are the "kb feature flag is off / endpoint missing"
		// signal — both surface as the same non-fatal sentinel so callers
		// can treat them uniformly via errors.Is.
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: HTTP %d from %s", ErrKBAPIUnavailable, resp.StatusCode, reqURL)
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, ErrUnauthorized
		}
		errMsg := strings.TrimSpace(string(bodyBytes))
		if errMsg == "" {
			return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, reqURL)
		}
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, reqURL, errMsg)
	}

	logger.LogHTTPResponseBody(string(bodyBytes))
	return bodyBytes, nil
}

// normalizeTypes maps any unrecognized kb_type values to KBTypeUnknown so
// callers never observe a server-only future value.
func (c *KBClient) normalizeTypes(in []KB) []KB {
	for i := range in {
		in[i].KBType = normalizeKBType(in[i].KBType)
	}
	return in
}

// normalizeKBType collapses unknown-to-this-client KBType strings into
// KBTypeUnknown without losing the row. Empty strings stay empty so callers
// can distinguish "server omitted the field" from "server sent a value we
// don't understand".
func normalizeKBType(t KBType) KBType {
	switch t {
	case "":
		return ""
	case KBTypePersonal, KBTypeProfile, KBTypeTeam, KBTypeRepo, KBTypeCustom, KBTypeUnknown:
		return t
	default:
		return KBTypeUnknown
	}
}
