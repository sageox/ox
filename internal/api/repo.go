package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
	"github.com/sageox/ox/internal/version"
)

const (
	repoInitPath = "/api/v1/repo/init"

	// repoDoctorPath: per ox-alh, the cloud doctor endpoint is being moved
	// from /api/v1/public/repos/{id}/doctor (unauthenticated) to
	// /api/v1/repos/{id}/doctor (authenticated Bearer). The CLI already
	// sends the Authorization header on this call, so flipping the path
	// here doesn't break anything once the server-side route lands. We
	// hit the authed path FIRST; on 404 we fall back to the legacy path
	// so a CLI on a newer ox version keeps working against an older
	// server.
	repoDoctorPathAuthed = "/api/v1/repos/%s/doctor"
	repoDoctorPathLegacy = "/api/v1/public/repos/%s/doctor"

	repoUninstallPath   = "/api/v1/repo/%s/uninstall"       // %s = repo_id
	repoMergePath       = "/api/v1/repo/%s/merge"           // %s = repo_id
	gitImportPath       = "/api/v1/teams/%s/context/import" // %s = team_id
	sessionUploadedPath = "/api/v1/sessions/%s/uploaded"    // %s = session_id
	sessionStartedPath  = "/api/v1/sessions/%s/started"     // %s = session_id
	sessionAbortedPath  = "/api/v1/sessions/%s/aborted"     // %s = session_id

	// planActivityPath is the client half of the caller-driven plan_id -> plan
	// index (bead sageox-gqgkg): the server endpoint currently accepts and
	// drops the body (a stub) until a future change actually indexes it.
	// %s, %s = team_id, plan_id.
	planActivityPath = "/api/v1/teams/%s/plans/%s/activity"
)

// RepoInitRequest represents the POST /api/v1/repo/init request
type RepoInitRequest struct {
	RepoID           string           `json:"repo_id"`                      // Required: prefixed UUIDv7
	Type             string           `json:"type"`                         // Required: "git" or "svn"
	InitAt           string           `json:"init_at"`                      // Required: RFC3339 timestamp
	Name             string           `json:"name,omitempty"`               // Optional: display name (e.g. "sageox/ox")
	Teams            []string         `json:"teams,omitempty"`              // Optional: team IDs to associate repo with
	RepoSalt         string           `json:"repo_salt,omitempty"`          // Optional: initial commit hash
	RepoRemoteHashes []string         `json:"repo_remote_hashes,omitempty"` // Optional: salted hashes
	Fingerprint      *RepoFingerprint `json:"fingerprint,omitempty"`        // Optional: repo identity fingerprint
	Identities       any              `json:"identities,omitempty"`         // Optional: resolved user identities (identity.ResolvedIdentities)
	IsPublic         bool             `json:"is_public,omitempty"`          // Optional: prevents fork merging
	CreatedByEmail   string           `json:"created_by_email,omitempty"`   // Optional: git user email (backward compat)
	CreatedByName    string           `json:"created_by_name,omitempty"`    // Optional: git user name (backward compat)
}

// RepoFingerprint holds repository identity fingerprint data for detecting
// identical or related repositories across teams. Enables the API to suggest
// team merges when multiple teams are working on the same codebase.
type RepoFingerprint struct {
	// FirstCommit is the hash of the initial commit (same as repo_salt).
	FirstCommit string `json:"first_commit"`

	// MonthlyCheckpoints maps "YYYY-MM" to the first commit hash of that month.
	MonthlyCheckpoints map[string]string `json:"monthly_checkpoints"`

	// AncestrySamples contains commit hashes at power-of-2 intervals.
	AncestrySamples []string `json:"ancestry_samples"`

	// RemoteHashes contains salted SHA256 hashes of normalized remote URLs.
	RemoteHashes []string `json:"remote_hashes,omitempty"`
}

// RepoInitResponse represents the POST /api/v1/repo/init response
type RepoInitResponse struct {
	RepoID           string `json:"repo_id"`
	TeamID           string `json:"team_id"`
	WebBaseURL       string `json:"web_base_url,omitempty"`      // web dashboard base URL (for enterprise endpoints)
	ExistingRepoID   string `json:"existing_repo_id,omitempty"`  // set when dedup matched a different repo_id
	DuplicateWarning string `json:"duplicate_warning,omitempty"` // human-readable warning for CLI display
}

// RepoUninstallRequest represents the POST /api/v1/repo/{repo_id}/uninstall request
type RepoUninstallRequest struct {
	RepoSalt string `json:"repo_salt"` // first commit hash for authentication
}

// MergeRepoRequest represents POST /api/v1/repo/{repo_id}/merge
type MergeRepoRequest struct {
	RepoMarkers map[string]json.RawMessage `json:"repo_markers"` // filename -> marker JSON
}

// MergeRepoResponse represents the merge API response
type MergeRepoResponse struct {
	Canonical string        `json:"canonical_repo_id"`  // the winning repo_id
	Merged    []string      `json:"merged_repo_ids"`    // repo_ids that were marked as merged
	Redirect  *RedirectInfo `json:"redirect,omitempty"` // redirect info (also in header)
}

// ImportNotification is the POST /api/v1/teams/{team_id}/context/import request body.
// Imports target a team context (not a project repo), so team_id is the
// primary identifier. The Metadata field is passed as-is (json.RawMessage)
// to avoid coupling the API package to the docMeta struct in cmd/ox.
type ImportNotification struct {
	TeamID   string          `json:"team_id"`
	Metadata json.RawMessage `json:"metadata"`
}

// SessionUploadedNotification is the POST /api/v1/sessions/{session_id}/uploaded
// request body. Tells the server a session's content has landed in the
// ledger and is viewable, so the (v2) GitHub App reconciler can refresh any
// PR sticky comment. See docs/specs/session-pr-issue-linkage.md (v1.5).
type SessionUploadedNotification struct {
	SessionID       string   `json:"session_id"`
	RepoID          string   `json:"repo_id"`
	SessionName     string   `json:"session_name,omitempty"` // ledger dir name — lets the server bind name↔id without waiting for ledger ingest
	SessionURL      string   `json:"session_url,omitempty"`
	LinkedPRs       []string `json:"linked_prs,omitempty"`
	LinkedIssues    []string `json:"linked_issues,omitempty"`
	ProducedCommits []string `json:"produced_commits,omitempty"`
}

// SessionStartedNotification is the POST /api/v1/sessions/{session_id}/started
// request body. Register-at-start: lets the universal conversation link
// (/c/<session_id>) resolve to a live "in progress" page from the moment a
// recording begins instead of only after stop+upload. Strictly fire-and-forget
// — the uploaded notification remains the authoritative signal.
type SessionStartedNotification struct {
	SessionID   string `json:"session_id"`
	RepoID      string `json:"repo_id"`
	SessionName string `json:"session_name,omitempty"`
	AgentID     string `json:"agent_id,omitempty"`
	Branch      string `json:"branch,omitempty"`
	StartedAt   string `json:"started_at,omitempty"` // RFC3339
}

// SessionAbortedNotification is the POST /api/v1/sessions/{session_id}/aborted
// request body. Flips a registered "in progress" page to "discarded" and lets
// the server drop any pending PR-link repair tasks for the session. Best-effort:
// the server also ages out stale registered-never-uploaded sessions on its own.
type SessionAbortedNotification struct {
	SessionID string `json:"session_id"`
	RepoID    string `json:"repo_id,omitempty"`
}

// PRLinkMiss is one linked PR whose body is missing the SageOx-Session
// trailer, as detected server-side (GitHub App) in response to the uploaded
// notification. The CLI surfaces each as a repair task in session-stop
// guidance — the agent fixes the PR with its own tooling; ox never mutates
// PR bodies and never requires gh on the machine.
type PRLinkMiss struct {
	PRURL        string `json:"pr_url"`
	ExpectedLine string `json:"expected_line"`
}

// DoctorIssue represents a single diagnostic issue from the cloud
// Cloud doctor detects things the local CLI cannot:
// - Pending merge conflicts (same repo registered twice) - requires cross-repo knowledge
// - Team invites pending acceptance - lives in cloud DB
// - Guidance updates available - version comparison server-side
// - Billing/quota warnings - enterprise only
// - Team-wide health (X repos need updates) - aggregate view
type DoctorIssue struct {
	Type        string `json:"type"`                   // e.g., "merge_pending", "team_invite_pending"
	Severity    string `json:"severity"`               // "error", "warning", "info"
	Title       string `json:"title"`                  // short display title
	Description string `json:"description"`            // detailed explanation, supports Markdown
	ActionURL   string `json:"action_url,omitempty"`   // URL to resolve the issue
	ActionLabel string `json:"action_label,omitempty"` // button text, e.g., "Resolve merge"
}

// DoctorResponse represents the GET /api/v1/repo/{repo_id}/doctor response
type DoctorResponse struct {
	Issues    []DoctorIssue `json:"issues"`
	CheckedAt string        `json:"checked_at"` // RFC3339 timestamp
}

// RepoClient handles API communication with the SageOx repo endpoints
type RepoClient struct {
	baseURL    string
	httpClient *http.Client
	authToken  string
	version    string
}

// NewRepoClient creates a new repo API client using the global default endpoint.
//
// CAUTION: This should RARELY be used. It uses endpoint.Get() which ignores
// project config, so it will use the wrong endpoint for repos configured with
// non-default endpoints (e.g., enterprise or test environments).
//
// Use NewRepoClientForProject(gitRoot) instead for operations within a repo context.
// Use NewRepoClientWithEndpoint(endpoint) when you have the endpoint explicitly.
func NewRepoClient() *RepoClient {
	return &RepoClient{
		baseURL:    endpoint.Get(),
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    version.Version,
	}
}

// NewRepoClientForProject creates a new repo API client using the endpoint from project config.
// This is the recommended way to create a client for repo-bound operations.
// It checks: SAGEOX_ENDPOINT env var > project config > default endpoint.
func NewRepoClientForProject(gitRoot string) *RepoClient {
	return &RepoClient{
		baseURL:    endpoint.GetForProject(gitRoot),
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    version.Version,
	}
}

// NewRepoClientWithEndpoint creates a new repo API client with a specific endpoint.
// Use this when you already have the endpoint URL (e.g., from auth flow or config).
func NewRepoClientWithEndpoint(baseURL string) *RepoClient {
	return &RepoClient{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    version.Version,
	}
}

// WithAuthToken sets the auth token for authenticated requests
func (c *RepoClient) WithAuthToken(token string) *RepoClient {
	c.authToken = token
	return c
}

// WithTimeout overrides the default 10s HTTP timeout. Use it for advisory,
// fire-and-forget notifications that sit on a path a human or agent is waiting
// on — 10s of stall to deliver a best-effort hint is a worse outcome than not
// delivering it.
func (c *RepoClient) WithTimeout(d time.Duration) *RepoClient {
	c.httpClient = &http.Client{Timeout: d}
	return c
}

// Endpoint returns the base URL this client is configured for
func (c *RepoClient) Endpoint() string {
	return c.baseURL
}

// RegisterRepo calls POST /api/v1/repo/init
// Returns (response, error) - error is nil if call succeeds (even for 4xx/5xx)
// Gracefully handles 404 (endpoint not yet deployed) by returning nil, nil
func (c *RepoClient) RegisterRepo(req *RepoInitRequest) (*RepoInitResponse, error) {
	reqURL := strings.TrimSuffix(c.baseURL, "/") + repoInitPath

	// marshal request body
	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	logger.LogHTTPRequest("POST", reqURL)
	// intentionally skip LogHTTPRequestBody — RepoInitRequest carries repo_salt
	// and repo_remote_hashes (auth material); matches the RegisterMarkers sibling.
	start := time.Now()

	// create HTTP request
	httpReq, err := useragent.NewRequest(context.Background(), "POST", reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// set headers
	httpReq.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.authToken))
	}

	// execute request
	resp, err := c.httpClient.Do(httpReq)
	duration := time.Since(start)

	if err != nil {
		logger.LogHTTPError("POST", reqURL, err, duration)
		return nil, fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("POST", reqURL, resp.StatusCode, duration)

	// check for version deprecation signals
	if CheckVersionResponse(resp) {
		return nil, ErrVersionUnsupported
	}

	// handle X-SageOx-Merge header for repo/team merges
	// this is best-effort - don't fail the request if redirect handling fails
	if redirectInfo := ParseRedirectHeader(resp.Header); redirectInfo != nil {
		if projectRoot := config.FindProjectRoot(); projectRoot != "" {
			_ = HandleRedirect(projectRoot, redirectInfo)
		}
	}

	// handle 404 gracefully - endpoint not yet deployed
	if resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, resp.Body) // drain body for connection reuse
		return nil, nil
	}

	// read response body for error handling
	bodyBytes, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// handle non-2xx responses
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errMsg := strings.TrimSpace(string(bodyBytes))
		if errMsg == "" {
			return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, reqURL)
		}
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, reqURL, errMsg)
	}

	// log the raw response for debugging
	logger.LogHTTPResponseBody(string(bodyBytes))

	// decode successful response
	var initResp RepoInitResponse
	if err := json.Unmarshal(bodyBytes, &initResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &initResp, nil
}

// GetDoctorIssues calls GET /api/v1/repo/{repo_id}/doctor for cloud diagnostics
// Returns nil, nil if API unavailable (graceful degradation for offline mode)
//
// Path probe order (ox-alh): try the authenticated path first; fall back to
// the legacy unauthenticated path only on 404 so the CLI keeps working
// against older servers that haven't shipped the auth move yet. Once every
// production endpoint has the authed path, the legacy fallback can be
// removed.
func (c *RepoClient) GetDoctorIssues(repoID string) (*DoctorResponse, error) {
	status, bodyBytes := c.tryDoctorPathFollow(repoID, repoDoctorPathAuthed)
	if status == http.StatusNotFound {
		status, bodyBytes = c.tryDoctorPathFollow(repoID, repoDoctorPathLegacy)
	}
	if status < 200 || status >= 300 {
		return nil, nil // graceful degradation
	}

	var doctorResp DoctorResponse
	if err := json.Unmarshal(bodyBytes, &doctorResp); err != nil {
		return nil, nil // graceful degradation on malformed response
	}
	return &doctorResp, nil
}

// tryDoctorPathFollow issues a single GET against the formatted path and
// returns the status code + body bytes. Sealed: network errors become
// status=0 so the caller's fallback logic stays simple. Logs at the same
// level the original code did.
func (c *RepoClient) tryDoctorPathFollow(repoID, pathFmt string) (int, []byte) {
	reqURL := strings.TrimSuffix(c.baseURL, "/") + fmt.Sprintf(pathFmt, repoID)
	logger.LogHTTPRequest("GET", reqURL)
	start := time.Now()
	httpReq, err := useragent.NewRequest(context.Background(), "GET", reqURL, nil)
	if err != nil {
		return 0, nil
	}
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.authToken))
	}
	resp, err := c.httpClient.Do(httpReq)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("GET", reqURL, err, duration)
		return 0, nil
	}
	defer resp.Body.Close()
	logger.LogHTTPResponse("GET", reqURL, resp.StatusCode, duration)
	if resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, resp.Body)
		return http.StatusNotFound, nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil
	}
	return resp.StatusCode, body
}

// NotifyUninstall calls POST /api/v1/repo/{repo_id}/uninstall to notify server of local uninstall.
// Requires authentication - the server validates the user is a team member with permission
// to trigger uninstallation workflows. Returns errors so callers can provide user feedback.
// The repo_salt (first commit hash) provides additional verification.
func (c *RepoClient) NotifyUninstall(repoID, repoSalt string) error {
	if repoID == "" {
		return fmt.Errorf("repo_id is required")
	}

	reqURL := strings.TrimSuffix(c.baseURL, "/") + fmt.Sprintf(repoUninstallPath, repoID)

	req := &RepoUninstallRequest{
		RepoSalt: repoSalt,
	}

	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	logger.LogHTTPRequest("POST", reqURL)
	start := time.Now()

	httpReq, err := useragent.NewRequest(context.Background(), "POST", reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.authToken))
	}

	resp, err := c.httpClient.Do(httpReq)
	duration := time.Since(start)

	if err != nil {
		logger.LogHTTPError("POST", reqURL, err, duration)
		return fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("POST", reqURL, resp.StatusCode, duration)

	// drain body for connection reuse (no response parsing needed)
	io.Copy(io.Discard, resp.Body)

	// check for auth/permission errors
	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted, http.StatusNoContent:
		return nil
	case http.StatusUnauthorized:
		return fmt.Errorf("authentication required (401)")
	case http.StatusForbidden:
		return fmt.Errorf("not authorized to uninstall this repo (403)")
	case http.StatusNotFound:
		// repo not found in cloud - that's ok, might not have been registered
		return nil
	default:
		if resp.StatusCode >= 400 {
			return fmt.Errorf("server error (%d)", resp.StatusCode)
		}
		return nil
	}
}

// MergeRepo calls POST /api/v1/repo/{repo_id}/merge to notify the server about
// duplicate registration resolution. This is best-effort for server-side visibility
// and bookkeeping — the local cleanup is authoritative.
// Gracefully handles 404 (endpoint not yet deployed) by returning nil, nil, nil.
func (c *RepoClient) MergeRepo(repoID string, markers map[string]json.RawMessage) (*MergeRepoResponse, *RedirectInfo, error) {
	reqURL := strings.TrimSuffix(c.baseURL, "/") + fmt.Sprintf(repoMergePath, repoID)

	mergeReq := &MergeRepoRequest{
		RepoMarkers: markers,
	}
	bodyBytes, err := json.Marshal(mergeReq)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	logger.LogHTTPRequest("POST", reqURL)
	// intentionally skip LogHTTPRequestBody — markers contain repo_salt (auth material)
	start := time.Now()

	httpReq, err := useragent.NewRequest(context.Background(), "POST", reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.authToken))
	}

	resp, err := c.httpClient.Do(httpReq)
	duration := time.Since(start)

	if err != nil {
		logger.LogHTTPError("POST", reqURL, err, duration)
		return nil, nil, fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("POST", reqURL, resp.StatusCode, duration)

	if CheckVersionResponse(resp) {
		return nil, nil, ErrVersionUnsupported
	}

	redirectInfo := ParseRedirectHeader(resp.Header)

	if resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, resp.Body)
		return nil, nil, nil
	}

	bodyBytes, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errMsg := strings.TrimSpace(string(bodyBytes))
		if errMsg == "" {
			return nil, nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, reqURL)
		}
		return nil, nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, reqURL, errMsg)
	}

	logger.LogHTTPResponseBody(string(bodyBytes))

	var mergeResp MergeRepoResponse
	if err := json.Unmarshal(bodyBytes, &mergeResp); err != nil {
		return nil, nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &mergeResp, redirectInfo, nil
}

// NotifyImport sends a fire-and-forget notification about a new document import.
// Imports target a team context, so teamID identifies where the document lives.
// The metadata argument should be JSON-marshalable (typically the docMeta struct).
//
// On 2xx the server may return a recording ID for media files it routes to
// transcription; that ID is returned so callers can poll `ox import --status`.
// The endpoint is fire-and-forget — a missing/empty body and network errors are
// not failures: recordingID is "" and err is nil. Only a non-404 4xx/5xx is an
// error, and even then it never invalidates the already-committed import.
func (c *RepoClient) NotifyImport(teamID string, metadata any) (recordingID string, err error) {
	metaBytes, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("marshal metadata: %w", err)
	}

	reqBody := ImportNotification{
		TeamID:   teamID,
		Metadata: metaBytes,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	reqURL := strings.TrimSuffix(c.baseURL, "/") + fmt.Sprintf(gitImportPath, teamID)

	logger.LogHTTPRequest("POST", reqURL)
	start := time.Now()

	httpReq, err := useragent.NewRequest(context.Background(), "POST", reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.authToken))
	}

	resp, err := c.httpClient.Do(httpReq)
	duration := time.Since(start)

	if err != nil {
		logger.LogHTTPError("POST", reqURL, err, duration)
		return "", nil // graceful degradation
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("POST", reqURL, resp.StatusCode, duration)

	if resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, resp.Body)
		return "", nil // endpoint not yet deployed
	}

	if resp.StatusCode >= 400 {
		io.Copy(io.Discard, resp.Body)
		return "", fmt.Errorf("import notification failed (%d)", resp.StatusCode)
	}

	// 2xx: opportunistically read a recording ID if the server returns one for
	// media routed to transcription. An empty/non-JSON body is normal (the
	// endpoint predates this field) and must not be treated as an error.
	var out struct {
		RecordingID string `json:"recording_id"`
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if len(respBody) > 0 {
		_ = json.Unmarshal(respBody, &out)
	}
	return out.RecordingID, nil
}

// postSessionSignal POSTs a session lifecycle notification and returns the
// (size-limited) response body on 2xx. Graceful degradation mirrors
// NotifyImport: 404 (endpoint not yet deployed) and 409 (already recorded —
// idempotent) return (nil, nil) so callers proceed without retry thrash;
// network errors, 429, and 5xx return an error for the caller's retry policy.
func (c *RepoClient) postSessionSignal(pathTmpl, sessionID, label string, body any) ([]byte, error) {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	reqURL := strings.TrimSuffix(c.baseURL, "/") + fmt.Sprintf(pathTmpl, sessionID)

	logger.LogHTTPRequest("POST", reqURL)
	start := time.Now()

	httpReq, err := useragent.NewRequest(context.Background(), "POST", reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.authToken))
	}

	resp, err := c.httpClient.Do(httpReq)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("POST", reqURL, err, duration)
		return nil, fmt.Errorf("%s notification: %w", label, err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("POST", reqURL, resp.StatusCode, duration)

	switch {
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusConflict:
		io.Copy(io.Discard, resp.Body)
		return nil, nil
	case resp.StatusCode >= 400:
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("%s notification failed (%d)", label, resp.StatusCode)
	default:
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return respBody, nil
	}
}

// NotifySessionUploaded tells the SageOx server that a session's content
// has been uploaded and is viewable, so the (v2) GitHub App reconciler can
// refresh any PR sticky comment. The response may carry pr_link_misses —
// linked PRs whose bodies lack the SageOx-Session trailer — which the
// caller surfaces as agent repair tasks. An empty or non-JSON body is
// normal (older servers) and yields no misses.
func (c *RepoClient) NotifySessionUploaded(n SessionUploadedNotification) ([]PRLinkMiss, error) {
	respBody, err := c.postSessionSignal(sessionUploadedPath, n.SessionID, "session uploaded", n)
	if err != nil || len(respBody) == 0 {
		return nil, err
	}
	var out struct {
		PRLinkMisses []PRLinkMiss `json:"pr_link_misses"`
	}
	_ = json.Unmarshal(respBody, &out)
	return out.PRLinkMisses, nil
}

// NotifySessionStarted registers a just-started recording so /c/<session_id>
// resolves immediately. Callers treat this as strictly fire-and-forget.
func (c *RepoClient) NotifySessionStarted(n SessionStartedNotification) error {
	_, err := c.postSessionSignal(sessionStartedPath, n.SessionID, "session started", n)
	return err
}

// NotifySessionAborted marks a registered recording as discarded. Callers
// treat this as strictly fire-and-forget.
func (c *RepoClient) NotifySessionAborted(n SessionAbortedNotification) error {
	_, err := c.postSessionSignal(sessionAbortedPath, n.SessionID, "session aborted", n)
	return err
}

// PlanActivityNotification is the POST
// /api/v1/teams/{team_id}/plans/{plan_id}/activity request body — the
// client half of the caller-driven plan_id -> plan index (bead sageox-gqgkg).
// Deliberately minimal (a stub): the server currently accepts and drops it
// until a future change actually builds the index from it.
type PlanActivityNotification struct {
	Event  string `json:"event"`            // plan.EventKind value (approved/worked/realized/abandoned/superseded)
	Status string `json:"status,omitempty"` // plan.PlanStatus, when the event carries one
}

// NotifyPlanActivity best-effort-reports one appended plan lifecycle event to
// the server's caller-driven plan index (bead sageox-gqgkg). Callers treat
// this as strictly fire-and-forget, matching NotifySessionStarted/Aborted:
// any error here is advisory only and must never affect the local
// events.jsonl append that already succeeded. Not built on postSessionSignal
// because this path substitutes two ids (team_id, plan_id), not one.
func (c *RepoClient) NotifyPlanActivity(teamID, planID string, n PlanActivityNotification) error {
	bodyBytes, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	reqURL := strings.TrimSuffix(c.baseURL, "/") + fmt.Sprintf(planActivityPath, teamID, planID)

	logger.LogHTTPRequest("POST", reqURL)
	start := time.Now()

	httpReq, err := useragent.NewRequest(context.Background(), "POST", reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.authToken))
	}

	resp, err := c.httpClient.Do(httpReq)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("POST", reqURL, err, duration)
		return fmt.Errorf("plan activity notification: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) // drain for connection reuse; the stub response body carries nothing useful

	logger.LogHTTPResponse("POST", reqURL, resp.StatusCode, duration)

	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("plan activity notification failed (%d)", resp.StatusCode)
	}
	return nil
}

// RepoMarkerData holds parsed data from a .repo_* marker file
type RepoMarkerData struct {
	RepoID   string `json:"repo_id"`
	RepoSalt string `json:"repo_salt"`
	Endpoint string `json:"endpoint"`
}

// ReadFirstRepoMarker reads the first .repo_* marker file found in the sageox directory.
// Returns the parsed marker data or nil if no marker found.
// This is useful for getting repo_id and repo_salt before uninstall.
func ReadFirstRepoMarker(sageoxDir string) (*RepoMarkerData, error) {
	entries, err := os.ReadDir(sageoxDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), ".repo_") {
			continue
		}

		markerPath := filepath.Join(sageoxDir, entry.Name())
		data, err := os.ReadFile(markerPath)
		if err != nil {
			continue
		}

		var marker RepoMarkerData
		if err := json.Unmarshal(data, &marker); err != nil {
			continue
		}

		if marker.RepoID != "" {
			return &marker, nil
		}
	}

	return nil, nil
}
