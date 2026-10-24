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

	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
)

// teamContextPath is the cloud endpoint that returns a team's full context
// (docs + AGENTS.md + CLAUDE.md + MEMORY.md) for HTTP-only ephemeral
// consumers that cannot perform a git clone of the team-context repo.
//
// Contract: GET /api/v1/teams/{team_id}/context returns a JSON
// TeamContextContentResponse. 404 means the endpoint is not deployed
// yet (or the team has no context) — callers fall through gracefully.
const teamContextPath = "/api/v1/teams/%s/context" // %s = team_id

// ErrTeamContextEndpointUnavailable is returned when the cloud team-context
// endpoint responds with 404. This is a sentinel — callers in ephemeral
// mode treat it as "fall through to whatever was already there" rather
// than a hard error, because the endpoint may not be deployed yet.
var ErrTeamContextEndpointUnavailable = errors.New("team-context endpoint not available")

// TeamContextContentResponse is the JSON payload returned by
// GET /api/v1/teams/{team_id}/context.
type TeamContextContentResponse struct {
	TeamID   string           `json:"team_id"`
	TeamName string           `json:"team_name"`
	Docs     []TeamContextDoc `json:"docs"`
	AgentsMD string           `json:"agents_md"`
	ClaudeMD string           `json:"claude_md"`
	Memory   string           `json:"memory"`

	// Skills and Rules are additive: a server that predates them omits the
	// keys, which decodes as nil, and ox then leaves skills and rules on disk
	// exactly as an older ox would. A non-nil slice — even an empty one — is
	// the server's complete set, unless SkillsTruncated says it is not: the
	// server dropped skills to stay under its payload cap, or could not read
	// the Team Context at all. Rules carry no flag of their own.
	//
	// No omitempty: re-encoding must keep "the team has none" ([]) distinct
	// from "not reported" (null), or a fixture or proxy turns one into the other.
	Skills          []TeamContextSkill `json:"skills"`
	SkillsTruncated bool               `json:"skills_truncated"`
	Rules           []TeamContextRule  `json:"rules"`
}

// TeamContextSkill is one servable team skill, carried as the files a Team
// Context checkout would hold at Root/Dir. Refused skills are never sent; a
// binary or oversize file is omitted while the skill is still listed.
type TeamContextSkill struct {
	Name   string                 `json:"name"`
	Root   string                 `json:"root"`
	Dir    string                 `json:"dir"`
	Digest string                 `json:"digest"`
	Files  []TeamContextSkillFile `json:"files"`
}

// TeamContextSkillFile is one text file of a served skill. Path is relative to
// the skill directory. Executable is the server's verdict for the file — the
// same predicate ox applies to it.
type TeamContextSkillFile struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	Executable bool   `json:"executable"`
}

// TeamContextRule is one team rule file. Path is Team Context-relative, e.g.
// "agents/rules/team-rules.md".
type TeamContextRule struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

// TeamContextDoc is a single team-context document fetched over HTTP.
// Name is a relative path under the team-context root (may include
// directory separators, e.g. "guides/architecture.md").
type TeamContextDoc struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

// GetTeamContextContent fetches the full team context for teamID over HTTP.
//
// This is the ephemeral-mode fallback for environments where the daemon
// cannot keep a local git clone of the team-context repo in sync. The
// caller is responsible for writing the returned content to disk under
// paths.TeamContextDir(teamID, endpoint).
//
// Returns ErrTeamContextEndpointUnavailable on 404 (endpoint not deployed
// or no team context). Returns ErrUnauthorized on 401, ErrForbidden on 403.
func (c *RepoClient) GetTeamContextContent(ctx context.Context, teamID string) (*TeamContextContentResponse, error) {
	if teamID == "" {
		return nil, fmt.Errorf("team_id is required")
	}

	reqURL := strings.TrimSuffix(c.baseURL, "/") + fmt.Sprintf(teamContextPath, teamID)

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
		logger.LogHTTPError("GET", reqURL, err, duration)
		return nil, fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("GET", reqURL, resp.StatusCode, duration)

	if CheckVersionResponse(resp) {
		return nil, ErrVersionUnsupported
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		switch resp.StatusCode {
		case http.StatusNotFound:
			return nil, ErrTeamContextEndpointUnavailable
		case http.StatusUnauthorized:
			return nil, ErrUnauthorized
		case http.StatusForbidden:
			return nil, ErrForbidden
		}
		errMsg := strings.TrimSpace(string(bodyBytes))
		if errMsg == "" {
			errMsg = resp.Status
		}
		return nil, fmt.Errorf("team-context fetch failed: status=%d body=%s", resp.StatusCode, errMsg)
	}

	var out TeamContextContentResponse
	if err := json.Unmarshal(bodyBytes, &out); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &out, nil
}
