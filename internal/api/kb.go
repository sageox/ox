package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
	"github.com/sageox/ox/internal/version"
)

// KB API endpoint paths. The list endpoint REQUIRES an explicit scope
// (scope_type + scope_id) — the contextless "all my bubbles" union no longer
// exists server-side (sageox-mono ADR-073: listing is per-context,
// members-only; a bare GET is a 400).
const (
	kbListPath    = "/api/v1/kb"         // GET ?scope_type=&scope_id= — list one scope's bubbles
	kbDetailPath  = "/api/v1/kb/%s"      // GET — fetch a single bubble by kb_id
	kbResolvePath = "/api/v1/kb/resolve" // GET ?scope_type=&scope_id=&slug= — scoped slug → kb_id
)

// KB scope types. Every bubble is owned by exactly one scope: a single user
// or a single team (ADR-073). scope_id carries the matching usr_/team_ id.
const (
	KBScopeTypeUser = "user"
	KBScopeTypeTeam = "team"
)

// KBScope identifies one listable context.
type KBScope struct {
	Type string // KBScopeTypeUser | KBScopeTypeTeam
	ID   string // usr_… | team_…
}

// ErrKBAPIUnavailable is the sentinel returned when the KB endpoint is not
// available to the caller (HTTP 403/404 — feature flag off, non-member scope,
// or endpoint missing). Non-fatal by design: callers should inspect with
// errors.Is and treat it as "no bubbles for this caller/scope".
var ErrKBAPIUnavailable = errors.New("kb API unavailable for this caller")

// KBType matches the sageox-mono KBType enum. Six known kinds plus a
// client-side "unknown" fallback bucket for forward compatibility when the
// server rolls out a new type before the CLI knows about it.
type KBType string

const (
	KBTypePersonal KBType = "personal"
	KBTypeProfile  KBType = "profile"
	KBTypeTeam     KBType = "team"
	KBTypeRepo     KBType = "repo"
	KBTypeCustom   KBType = "custom"
	KBTypeChannel  KBType = "channel"
	// KBTypeUnknown is the client-side fallback bucket when the server
	// returns a kb_type the CLI doesn't recognize. Never sent by the server.
	KBTypeUnknown KBType = "unknown"
)

// KB represents a knowledge bubble row from GET /api/v1/kb (list) or
// GET /api/v1/kb/{id} (detail). Field set tracks the sageox-mono KBResponse
// schema; new server fields can be added without breaking older clients
// because the JSON decoder ignores unknown keys by default.
//
// Note ViewerRole: the server omits it from LIST responses (role is per-KB
// but the list is scope-wide); it is populated on single-bubble reads.
type KB struct {
	KBID           string `json:"id"`
	KBType         KBType `json:"kb_type"`
	Slug           string `json:"slug"`
	Name           string `json:"name"`
	OwnerUserID    string `json:"owner_user_id,omitempty"`
	LifecycleState string `json:"lifecycle_state,omitempty"` // "provisioning", "active", "provision-failed"
	ViewerRole     string `json:"viewer_role,omitempty"`     // "admin", "member", "viewer" (single reads only)
	CreatedAt      string `json:"created_at,omitempty"`      // RFC3339 timestamp

	// ScopeType/ScopeID locate the bubble: "user"|"team" + the usr_/team_
	// owner id (ADR-073). Immutable after creation.
	ScopeType string `json:"scope_type,omitempty"`
	ScopeID   string `json:"scope_id,omitempty"`

	// Manager is the bubble admin — the single human in charge (a label,
	// not an ACL; ADR-073 §2). Same value as owner_user_id today.
	Manager string `json:"manager,omitempty"`

	// Description is the bubble's free-text description.
	Description string `json:"description,omitempty"`

	// Steering is the free-text curator-steering value (ADR-097 C9).
	// Unset ⇒ the bubble opted out of curator routing.
	Steering string `json:"steering,omitempty"`

	// Topics is the declared topic list (server sends [] when none).
	Topics []string `json:"topics,omitempty"`

	// GitPath is the host-relative git project path (e.g. "kb/kb_xxx").
	// Combine with the endpoint's git host to build a clone URL; empty
	// until the bubble's repo is provisioned.
	GitPath string `json:"git_path,omitempty"`

	// DefaultBranch is the bubble repo's default branch (almost always "main").
	DefaultBranch string `json:"default_branch,omitempty"`

	// LastActivityAt is GitLab's project last_activity_at — the only
	// activity signal that reflects out-of-band pushes. RFC3339.
	LastActivityAt string `json:"last_activity_at,omitempty"`

	// RepoURL is a full git clone URL when the server supplies one
	// (older response shapes). Prefer GitPath + endpoint-derived host.
	RepoURL string `json:"repo_url,omitempty"`

	// RepoID scopes a kb_type=repo bubble to a specific project. When set,
	// the per-project symlink reconciler only links this bubble into the
	// project whose ProjectConfig.RepoID matches.
	RepoID string `json:"repo_id,omitempty"`
}

// UnmarshalJSON accepts both the current server key ("id") and the older
// "kb_id" alias so fixtures and any transitional response shape decode to
// the same struct. All other fields use the standard decoder.
func (k *KB) UnmarshalJSON(data []byte) error {
	type kbAlias KB // no methods — avoids recursion
	aux := struct {
		*kbAlias
		LegacyKBID string `json:"kb_id"`
	}{kbAlias: (*kbAlias)(k)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if k.KBID == "" && aux.LegacyKBID != "" {
		k.KBID = aux.LegacyKBID
	}
	return nil
}

// kbListResponse is the envelope for GET /api/v1/kb. The current server
// returns {"kbs": [...]}; the older shape used {"bubbles": [...]} and some
// fixtures use a bare array — ListBubbles tolerates all three.
type kbListResponse struct {
	KBs     []KB `json:"kbs"`
	Bubbles []KB `json:"bubbles"`
}

// kbResolveResponse is the envelope for GET /api/v1/kb/resolve.
type kbResolveResponse struct {
	KBID string `json:"kb_id"`
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

// WithHTTPTimeout overrides the transport-level timeout and returns the
// client for fluent chaining. The default 10s suits the fast reads
// (list/detail/resolve); a caller whose request does real server-side work —
// search embeds the query and fans out per bubble — must raise this to at
// least its per-call context budget, or the transport cap silently becomes
// the effective limit.
func (c *KBClient) WithHTTPTimeout(d time.Duration) *KBClient {
	c.httpClient = &http.Client{Timeout: d}
	return c
}

// Endpoint returns the base URL this client is configured for.
func (c *KBClient) Endpoint() string {
	return c.baseURL
}

// ListBubbles calls GET /api/v1/kb?scope_type=&scope_id= to list one scope's
// bubbles. The scope is REQUIRED — the server 400s a bare request and 403s a
// non-member scope (which maps to ErrKBAPIUnavailable like a flag-off 404,
// since both mean "no bubbles visible here for this caller").
func (c *KBClient) ListBubbles(ctx context.Context, scope KBScope) ([]KB, error) {
	if scope.Type == "" || scope.ID == "" {
		return nil, fmt.Errorf("kb list requires a scope (scope_type + scope_id)")
	}

	q := url.Values{}
	q.Set("scope_type", scope.Type)
	q.Set("scope_id", scope.ID)
	reqURL := strings.TrimSuffix(c.baseURL, "/") + kbListPath + "?" + q.Encode()

	bodyBytes, err := c.do(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}

	// prefer the envelope shapes; fall back to a bare array
	var envelope kbListResponse
	if err := json.Unmarshal(bodyBytes, &envelope); err == nil {
		if envelope.KBs != nil {
			return c.normalizeTypes(envelope.KBs), nil
		}
		if envelope.Bubbles != nil {
			return c.normalizeTypes(envelope.Bubbles), nil
		}
	}

	var bare []KB
	if err := json.Unmarshal(bodyBytes, &bare); err != nil {
		return nil, fmt.Errorf("failed to decode kb list response: %w", err)
	}
	return c.normalizeTypes(bare), nil
}

// GetBubble calls GET /api/v1/kb/{id} for a single bubble by kb_id.
// 403/404 → sentinel. Slug resolution goes through ResolveSlug.
func (c *KBClient) GetBubble(ctx context.Context, kbID string) (*KB, error) {
	if strings.TrimSpace(kbID) == "" {
		return nil, fmt.Errorf("kb id is required")
	}

	reqURL := strings.TrimSuffix(c.baseURL, "/") + fmt.Sprintf(kbDetailPath, url.PathEscape(kbID))

	bodyBytes, err := c.do(ctx, "GET", reqURL, nil)
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

// ResolveSlug calls GET /api/v1/kb/resolve to map a scoped slug to its
// kb_id, following server-side rename aliases. Members-only: not-found and
// no-access both surface as ErrKBAPIUnavailable (the server deliberately
// returns an identical 404 for both so slugs cannot be enumerated).
func (c *KBClient) ResolveSlug(ctx context.Context, scope KBScope, slug string) (string, error) {
	if scope.Type == "" || scope.ID == "" || strings.TrimSpace(slug) == "" {
		return "", fmt.Errorf("kb resolve requires scope_type, scope_id, and slug")
	}

	q := url.Values{}
	q.Set("scope_type", scope.Type)
	q.Set("scope_id", scope.ID)
	q.Set("slug", slug)
	reqURL := strings.TrimSuffix(c.baseURL, "/") + kbResolvePath + "?" + q.Encode()

	bodyBytes, err := c.do(ctx, "GET", reqURL, nil)
	if err != nil {
		return "", err
	}

	var resp kbResolveResponse
	if err := json.Unmarshal(bodyBytes, &resp); err != nil {
		return "", fmt.Errorf("failed to decode kb resolve response: %w", err)
	}
	if resp.KBID == "" {
		return "", fmt.Errorf("kb resolve returned an empty kb_id")
	}
	return resp.KBID, nil
}

// do issues the HTTP request, applies standard auth+UA headers, and returns
// the response body bytes on 2xx. Non-2xx and network/version errors are
// translated into the package's typed errors. A non-nil body is sent as a
// JSON request body (Content-Type: application/json).
func (c *KBClient) do(ctx context.Context, method, reqURL string, body []byte) ([]byte, error) {
	logger.LogHTTPRequest(method, reqURL)
	start := time.Now()

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	httpReq, err := useragent.NewRequest(ctx, method, reqURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
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
		// 403 and 404 are the "kb feature flag is off / endpoint missing /
		// non-member scope" signal — all surface as the same non-fatal
		// sentinel so callers can treat them uniformly via errors.Is.
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
	case KBTypePersonal, KBTypeProfile, KBTypeTeam, KBTypeRepo, KBTypeCustom, KBTypeChannel, KBTypeUnknown:
		return t
	default:
		return KBTypeUnknown
	}
}
