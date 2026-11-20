package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
)

// sessionAgentViewPath is GET /api/v1/sessions/{id}/agent-view: the server
// renders a live (v3) coding session as markdown for agents. Rendering lives
// only on the server; the CLI prints what it returns.
//
// A 404 means "not found or you cannot read it" and deliberately never says
// which.
const sessionAgentViewPath = "/api/v1/sessions/%s/agent-view"

// maxAgentViewBodyBytes caps the response the client buffers. The server
// budget tops out at 60000 tokens (~240 KB of text); JSON escaping and the
// layer list fit well inside this.
const maxAgentViewBodyBytes = 4 << 20

// ErrSessionNotFound: 404 from the agent-view endpoint (missing or unreadable).
var ErrSessionNotFound = errors.New("session not found or not accessible")

// SessionAgentViewOptions are the optional query parameters.
type SessionAgentViewOptions struct {
	Since  string // opaque cursor from a previous next_since
	Budget int    // approximate tokens; 0 = server default
	Layer  string // clyr_ id
	// ContextRepo is the repo_id of the project the reader runs in; the server
	// uses it to decide whether the content may be shown here. Empty = none.
	ContextRepo string
}

// RateLimitedError is a 429; RetryAfter is the parsed Retry-After (0 if absent).
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string { return "session view rate limited" }

// SessionLayer is one entry of AgentView.Layers.
type SessionLayer struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Bytes int64  `json:"bytes"`
}

// AgentView is the agent-view response. Unknown fields are ignored by
// decoding; Raw keeps the exact body for `--json`.
type AgentView struct {
	SessionID      string `json:"session_id"`
	ConversationID string `json:"conversation_id"`
	TeamID         string `json:"team_id"`
	// TeamMatch is false when the server withheld the content because the
	// session is outside the reader's repo team; nil when the field is absent.
	TeamMatch      *bool          `json:"team_match"`
	Notice         string         `json:"notice"`
	Title          string         `json:"title"`
	Status         string         `json:"status"`
	Live           bool           `json:"live"`
	AsOf           string         `json:"as_of"`
	LastActivityAt string         `json:"last_activity_at"`
	TurnCount      int            `json:"turn_count"`
	NextSince      string         `json:"next_since"`
	Truncated      bool           `json:"truncated"`
	Layers         []SessionLayer `json:"layers"`
	Markdown       string         `json:"markdown"`

	Raw json.RawMessage `json:"-"`
}

// GetSessionAgentView fetches the rendered view of a live session.
//
// Errors: ErrSessionNotFound (404), ErrUnauthorized (401),
// ErrVersionUnsupported (426); transport and 5xx failures and other statuses
// are plain wrapped errors. Redirects are never followed so the credential
// goes only to this client's endpoint.
func (c *RepoClient) GetSessionAgentView(ctx context.Context, id string, opts SessionAgentViewOptions) (*AgentView, error) {
	// The id lands in the request path; callers validate its grammar, this is
	// the backstop against separators a proxy could rewrite into another route.
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\?#%") {
		return nil, fmt.Errorf("invalid session id")
	}

	q := url.Values{}
	if opts.Since != "" {
		q.Set("since", opts.Since)
	}
	if opts.Budget > 0 {
		q.Set("budget", strconv.Itoa(opts.Budget))
	}
	if opts.Layer != "" {
		q.Set("layer", opts.Layer)
	}
	if opts.ContextRepo != "" {
		q.Set("context_repo", opts.ContextRepo)
	}
	base := strings.TrimSuffix(c.baseURL, "/")
	reqURL := base + fmt.Sprintf(sessionAgentViewPath, url.PathEscape(id))
	if len(q) > 0 {
		reqURL += "?" + q.Encode()
	}
	// the cursor is opaque but not a secret; still log only the route shape
	logURL := base + fmt.Sprintf(sessionAgentViewPath, "{id}")

	logger.LogHTTPRequest("GET", logURL)
	start := time.Now()

	req, err := useragent.NewRequest(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}

	hc := *c.httpClient
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := hc.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("GET", logURL, err, duration)
		return nil, fmt.Errorf("session view request: %w", err)
	}
	defer resp.Body.Close()
	logger.LogHTTPResponse("GET", logURL, resp.StatusCode, duration)

	if CheckVersionResponse(resp) {
		return nil, ErrVersionUnsupported
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxAgentViewBodyBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read session view response: %w", err)
		}
		if int64(len(body)) > maxAgentViewBodyBytes {
			return nil, fmt.Errorf("session view response too large (exceeds %d bytes)", maxAgentViewBodyBytes)
		}
		var out AgentView
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("decode session view response: %w", err)
		}
		out.Raw = body
		return &out, nil
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusForbidden:
		return nil, ErrSessionNotFound
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, &RateLimitedError{RetryAfter: parseRetryAfter(resp.Header)}
	case resp.StatusCode >= 500:
		return nil, fmt.Errorf("session view unavailable: server returned %d", resp.StatusCode)
	default:
		return nil, fmt.Errorf("session view failed: HTTP %d", resp.StatusCode)
	}
}
