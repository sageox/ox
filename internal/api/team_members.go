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

// teamRosterPath is the cloud endpoint that returns a team's coworker roster —
// the identity fields the server exposes for resolving who a teammate is (human
// or AI coworker).
//
// Contract: GET /api/v1/teams/{team_ref}/roster returns a JSON
// TeamRosterResponse. team_ref may be a team id or slug; the server resolves
// both. The server owns which identity fields the roster carries; the client
// renders whatever it is given.
//
// The endpoint is feature-flag gated on the server. A 404 means the flag is off
// or the route is not deployed yet — callers degrade gracefully rather than
// erroring, mirroring GetTeamContextContent.
const teamRosterPath = "/api/v1/teams/%s/roster" // %s = team id or slug

// maxRosterBodyBytes caps the roster response read. A roster is a few KB; 4 MiB
// is generous headroom while still refusing an unbounded body.
const maxRosterBodyBytes = 4 << 20

// ErrTeamRosterUnsupported is the sentinel returned on 404: the roster endpoint
// is not available (feature flag off, or an older server). Callers report the
// capability as unavailable and exit 0 rather than treating it as a failure.
var ErrTeamRosterUnsupported = errors.New("team roster endpoint not available (feature not enabled, or server too old)")

// ErrTeamRosterUnavailable is the sentinel for a server that is down or
// unreachable: a transport failure (DNS/refused/timeout) or a 5xx. Distinct from
// ErrTeamRosterUnsupported (the route doesn't exist) — this means the route may
// exist but the server can't answer right now. Callers degrade gracefully: the
// roster is informational, so a blip shouldn't be a hard failure.
var ErrTeamRosterUnavailable = errors.New("team roster temporarily unavailable (could not reach the server)")

// TeamMember is one coworker in a team roster — a human (usr_) or an AI coworker
// (agt_). Its fields are the identity attributes the server exposes for a
// roster: display name, type, role, and the handles the coworker is known by.
// The set of fields is the server's to decide; unknown fields decode away.
type TeamMember struct {
	// PrincipalID is the canonical coworker id: usr_ (human) or agt_ (AI).
	PrincipalID string `json:"principal_id"`
	// Name is the coworker's display name.
	Name string `json:"name"`
	// Type is "human" or "ai".
	Type string `json:"type"`
	// Role is the team role (e.g. owner, member), when present.
	Role string `json:"role,omitempty"`
	// Aliases are handles the coworker is known by (a GitHub login, short
	// names), so a surface form like "port8080" resolves to a name.
	Aliases []string `json:"aliases,omitempty"`
}

// TeamRosterResponse is the JSON payload from GET /api/v1/teams/{ref}/roster.
type TeamRosterResponse struct {
	TeamID  string       `json:"team_id"`
	Members []TeamMember `json:"members"`
	Total   int          `json:"total"`
}

// ListTeamRoster fetches the minimal-tier coworker roster for a team.
//
// teamRef is a team id or slug (the server resolves both). Returns
// ErrTeamRosterUnsupported on 404 (flag off / not deployed — caller degrades),
// ErrTeamRosterUnavailable on a transport failure or 5xx (server down/unreachable
// — caller degrades), ErrUnauthorized on 401, ErrForbidden on 403, and
// ErrVersionUnsupported when the server rejects the CLI version. An oversized
// body (> maxRosterBodyBytes) is rejected with a plain error.
func (c *RepoClient) ListTeamRoster(ctx context.Context, teamRef string) (*TeamRosterResponse, error) {
	if teamRef == "" {
		return nil, fmt.Errorf("team is required")
	}
	// Reject dot-segments and separators up front. url.PathEscape leaves "." and
	// ".." unchanged, so a normalizing proxy could rewrite /teams/./roster or
	// /teams/../roster before routing and the resulting 404 would masquerade as
	// "roster unavailable". A real team id/slug never contains these.
	if teamRef == "." || teamRef == ".." || strings.ContainsAny(teamRef, "/\\") {
		return nil, fmt.Errorf("invalid team reference %q", teamRef)
	}

	reqURL := strings.TrimSuffix(c.baseURL, "/") + fmt.Sprintf(teamRosterPath, url.PathEscape(teamRef))

	logger.LogHTTPRequest("GET", reqURL)
	start := time.Now()

	httpReq, err := useragent.NewRequest(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.authToken))
	}

	resp, err := c.httpClient.Do(httpReq)
	duration := time.Since(start)
	if err != nil {
		// Transport failure — server down, DNS/host doesn't resolve, connection
		// refused, or timeout. Map to the graceful "unavailable" sentinel so the
		// command degrades instead of erroring hard on a reachability blip.
		logger.LogHTTPError("GET", reqURL, err, duration)
		return nil, fmt.Errorf("%w: %w", ErrTeamRosterUnavailable, err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("GET", reqURL, resp.StatusCode, duration)

	if CheckVersionResponse(resp) {
		return nil, ErrVersionUnsupported
	}

	// Classify status BEFORE reading the body, so a hostile or huge error body
	// can't turn a degradable failure (e.g. a 5xx) into an "oversize" error.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		switch {
		case resp.StatusCode == http.StatusNotFound:
			return nil, ErrTeamRosterUnsupported
		case resp.StatusCode == http.StatusUnauthorized:
			return nil, ErrUnauthorized
		case resp.StatusCode == http.StatusForbidden:
			return nil, ErrForbidden
		case resp.StatusCode >= 500:
			// Server is up but failing — a reachability/availability problem;
			// degrade gracefully rather than erroring hard.
			return nil, fmt.Errorf("%w: server returned %d", ErrTeamRosterUnavailable, resp.StatusCode)
		}
		// Other 4xx: read a capped body for a diagnostic message.
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxRosterBodyBytes))
		errMsg := strings.TrimSpace(string(errBody))
		if errMsg == "" {
			errMsg = resp.Status
		}
		return nil, fmt.Errorf("team roster fetch failed: status=%d body=%s", resp.StatusCode, errMsg)
	}

	// Success: read one byte past the cap so an oversized body is DETECTED, not
	// silently truncated — io.LimitReader returns EOF at the limit,
	// indistinguishable from a body that just fit, so truncation would surface as
	// a confusing JSON decode error. This keeps a malicious or MITM'd server from
	// OOMing the CLI (the 30s timeout bounds time, not memory).
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxRosterBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	if int64(len(bodyBytes)) > maxRosterBodyBytes {
		return nil, fmt.Errorf("roster response too large (exceeds %d bytes)", maxRosterBodyBytes)
	}

	var out TeamRosterResponse
	if err := json.Unmarshal(bodyBytes, &out); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &out, nil
}
