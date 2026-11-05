package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
)

// Nudges (the server's ADR-206 engine) pick at most one card per placement for
// the signed-in person. The CLI asks for the "cli.hint" placement and reports
// back when it has shown the card.
//
// Contract:
//
//	GET  /api/v1/users/me/nudges?placement=cli.hint          -> NudgeResponse
//	POST /api/v1/users/me/nudges/{token}/seen?placement=...  -> 2xx, body ignored
//
// Both calls are advisory. Neither runs CheckVersionResponse: a hint must never
// print an upgrade banner or a deprecation warning of its own, and the real
// commands already surface those.
const (
	nudgesPath     = "/api/v1/users/me/nudges"
	nudgeSeenPath  = "/api/v1/users/me/nudges/%s/seen" // %s = card token
	maxNudgeBytes  = 64 << 10                          // a card is a few hundred bytes
	nudgeEventBody = 1 << 10                           // drained, never parsed
)

// NudgePlacementCLIHint is the placement for the one-line hint ox prints on
// stderr after a command a person typed.
const NudgePlacementCLIHint = "cli.hint"

// ErrNudgesUnsupported means the server does not serve this placement: a 404
// from an older server, or a 200 that flags the placement as unknown. Callers
// stay silent and ask again much later.
var ErrNudgesUnsupported = errors.New("nudges placement not available (older server)")

// NudgeCTA is a card's call to action. Href is a same-origin path; the caller
// owns joining it to the endpoint.
type NudgeCTA struct {
	Label string `json:"label"`
	Href  string `json:"href"`
}

// NudgeCard is the part of a server nudge card the CLI reads. Unknown fields
// (art, kind, eyebrow, ...) decode away, so the server can grow the card
// without breaking this release (.claude/rules/tolerant-readers.md). The
// struct is also what the CLI caches, so keep it to what the hint needs.
type NudgeCard struct {
	NudgeID string   `json:"nudge_id"`
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	CTA     NudgeCTA `json:"cta"`
	// Token names the card on the event POSTs. Opaque to the client.
	Token string `json:"token"`
}

// NudgeResponse is the GET body. Card is nil when nothing should show.
type NudgeResponse struct {
	Placement        string     `json:"placement"`
	Card             *NudgeCard `json:"card"`
	UnknownPlacement bool       `json:"unknown_placement,omitempty"`
}

// GetNudge fetches the card the server would show for placement. A nil Card
// with a nil error is a real answer: nothing to show. The caller's ctx carries
// the time budget.
func (c *RepoClient) GetNudge(ctx context.Context, placement string) (*NudgeResponse, error) {
	reqURL := strings.TrimSuffix(c.baseURL, "/") + nudgesPath + "?placement=" + url.QueryEscape(placement)

	logger.LogHTTPRequest("GET", reqURL)
	start := time.Now()

	httpReq, err := useragent.NewRequest(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Accept", "application/json")
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.authToken)
	}

	resp, err := c.httpClient.Do(httpReq)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("GET", reqURL, err, duration)
		return nil, fmt.Errorf("fetch nudge: %w", err)
	}
	defer resp.Body.Close()
	logger.LogHTTPResponse("GET", reqURL, resp.StatusCode, duration)

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNudgesUnsupported
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("fetch nudge: status=%d", resp.StatusCode)
	}

	// Read one byte past the cap so an oversized body is detected, not
	// silently truncated into a confusing decode error.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxNudgeBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read nudge response: %w", err)
	}
	if len(body) > maxNudgeBytes {
		return nil, fmt.Errorf("nudge response too large (exceeds %d bytes)", maxNudgeBytes)
	}

	var out NudgeResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode nudge response: %w", err)
	}
	if out.UnknownPlacement {
		return nil, ErrNudgesUnsupported
	}
	return &out, nil
}

// ReportNudgeSeen tells the server the person was shown the card named by
// token. placement is analytics only: the token does not carry it. Best-effort
// by contract; the caller logs a failure and moves on.
func (c *RepoClient) ReportNudgeSeen(ctx context.Context, token, placement string) error {
	if token == "" {
		return errors.New("nudge token is required")
	}
	reqURL := strings.TrimSuffix(c.baseURL, "/") +
		fmt.Sprintf(nudgeSeenPath, url.PathEscape(token)) + "?placement=" + url.QueryEscape(placement)

	logger.LogHTTPRequest("POST", reqURL)
	start := time.Now()

	httpReq, err := useragent.NewRequest(ctx, http.MethodPost, reqURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.authToken)
	}

	resp, err := c.httpClient.Do(httpReq)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("POST", reqURL, err, duration)
		return fmt.Errorf("report nudge seen: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, nudgeEventBody)) // drain for connection reuse
	logger.LogHTTPResponse("POST", reqURL, resp.StatusCode, duration)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("report nudge seen: status=%d", resp.StatusCode)
	}
	return nil
}
