package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/search"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
)

// queryArgs holds parsed arguments for the query command.
type queryArgs struct {
	query  string
	mode   string
	limit  int
	teamID string
	repoID string
	source string // "team" (default), "code", "all"
}

// parseQueryArgs extracts flags and the positional query from raw args.
// --limit not --k: self-describing flag names over ML jargon;
// agents and humans guess --limit first
func parseQueryArgs(args []string) (*queryArgs, error) {
	qa := &queryArgs{
		mode:   "hybrid",
		limit:  5,
		source: "team",
	}

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--mode" && i+1 < len(args):
			qa.mode = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--mode="):
			qa.mode = strings.TrimPrefix(args[i], "--mode=")
		// TODO(ox-54a): move --k alias to friction catalog once hand-crafted catalog merging lands
		case (args[i] == "--limit" || args[i] == "--k") && i+1 < len(args):
			v, err := strconv.Atoi(args[i+1])
			if err != nil {
				return nil, fmt.Errorf("invalid --limit value %q: must be an integer", args[i+1])
			}
			qa.limit = v
			i++
		case strings.HasPrefix(args[i], "--limit=") || strings.HasPrefix(args[i], "--k="):
			raw := strings.TrimPrefix(strings.TrimPrefix(args[i], "--limit="), "--k=")
			v, err := strconv.Atoi(raw)
			if err != nil {
				return nil, fmt.Errorf("invalid --limit value %q: must be an integer", raw)
			}
			qa.limit = v
		case args[i] == "--team" && i+1 < len(args):
			qa.teamID = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--team="):
			qa.teamID = strings.TrimPrefix(args[i], "--team=")
		case args[i] == "--repo" && i+1 < len(args):
			qa.repoID = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--repo="):
			qa.repoID = strings.TrimPrefix(args[i], "--repo=")
		case args[i] == "--source" && i+1 < len(args):
			qa.source = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--source="):
			qa.source = strings.TrimPrefix(args[i], "--source=")
		case !strings.HasPrefix(args[i], "--"):
			qa.query = args[i]
		}
	}

	if qa.query == "" {
		return nil, fmt.Errorf("query text is required")
	}

	switch qa.mode {
	case "hybrid", "knn", "bm25":
		// ok
	default:
		return nil, fmt.Errorf("invalid mode %q: must be hybrid, knn, or bm25", qa.mode)
	}

	// normalize alias: teamctx is shorthand agents may use
	if qa.source == "teamctx" {
		qa.source = "team"
	}

	switch qa.source {
	case "all", "team", "code":
		// ok
	default:
		return nil, fmt.Errorf("invalid source %q: must be all, team, or code", qa.source)
	}

	return qa, nil
}

const queryUsage = `Usage: ox query "search text" [flags]

Flags:
  --limit N      Max results to return (default: 5)
  --mode MODE    Search mode: hybrid, knn, or bm25 (default: hybrid)
  --team ID      Team ID to search (default: from project config)
  --repo ID      Repo ID to search (default: from project config)
  --source SRC   Search source: team (default), code, all

Sources:
  team      Search team discussions, docs, and session history (default)
  code      Search local code index only (queries)
  all       Search both team context and local code index

Searches across team discussions, docs, and session history.
For code search, use: ox code search "<pattern>" or --source=code

Also available as: ox agent <id> query "search text"`

// combinedQueryResponse holds results from both team context and local code search.
type combinedQueryResponse struct {
	TeamContext *api.QueryResponse `json:"team_context,omitempty"`
	CodeResults []search.Result    `json:"code_results,omitempty"` // used by --full-json only
}

// compactQueryResponse is the default agent query output — minimal context footprint.
type compactQueryResponse struct {
	TeamContext *api.QueryResponse    `json:"team_context,omitempty"`
	CodeResults []compactSearchResult `json:"code_results,omitempty"`
	Guidance    string                `json:"guidance,omitempty"`
}

// runAgentQuery handles `ox agent <id> query "search text"`.
// Thin wrapper around executeQuery that adds context byte tracking.
func runAgentQuery(inst *agentinstance.Instance, args []string) error {
	qa, err := parseQueryArgs(args)
	if err != nil {
		return fmt.Errorf("%w\n\n%s", err, queryUsage)
	}

	outputBytes, err := executeQuery(qa, inst.AgentID, inst.AgentType)
	if err != nil {
		return err
	}

	// track context bytes for agent-specific cumulative tracking
	slog.Debug("query response context cost", "agent_id", inst.AgentID, "bytes", outputBytes)
	trackContextBytes(int64(outputBytes))
	return nil
}

// executeQuery performs the core query: resolves project config, searches team context
// and/or local code index based on --source flag, and writes JSON results to stdout.
// Returns bytes written for context tracking.
// agentID and agentType are optional — passed to the server for analytics.
func executeQuery(qa *queryArgs, agentID string, agentType string) (int, error) {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return 0, fmt.Errorf("could not find project root: %w", err)
	}

	if qa.limit <= 0 {
		return 0, fmt.Errorf("invalid --limit: must be > 0")
	}

	combined := &combinedQueryResponse{}

	// query team context if source is "all" or "teamctx"
	if qa.source == "all" || qa.source == "teamctx" {
		resp, err := queryTeamContext(qa, projectRoot, agentID, agentType)
		if err != nil {
			if qa.source == "teamctx" {
				return 0, fmt.Errorf("team context query failed: %w", err)
			}
			slog.Warn("team context query failed, continuing with code search", "error", err)
		} else {
			combined.TeamContext = resp
		}
	}

	// query local code index if source is "all" or "code"
	var codeResults []search.Result
	if qa.source == "all" || qa.source == "code" {
		results, err := queryCodeDB(qa, projectRoot)
		if err != nil {
			if qa.source == "code" {
				return 0, fmt.Errorf("code search failed: %w", err)
			}
			slog.Warn("code search failed, continuing with team context", "error", err)
		} else {
			codeResults = results
		}
	}

	// compact output — minimal context footprint for agents
	resp := compactQueryResponse{
		TeamContext: combined.TeamContext,
	}
	if len(codeResults) > 0 {
		compact := compactSearchResults(codeResults, qa.limit)
		resp.CodeResults = compact.Results
		resp.Guidance = compact.Guidance
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(resp); err != nil {
		return 0, fmt.Errorf("failed to encode response: %w", err)
	}

	outputBytes := buf.Len()
	_, err = buf.WriteTo(os.Stdout)
	return outputBytes, err
}

// queryTeamContext searches team context via the vector search API.
func queryTeamContext(qa *queryArgs, projectRoot, agentID, agentType string) (*api.QueryResponse, error) {
	cfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("could not load project config: %w", err)
	}

	if qa.teamID == "" {
		qa.teamID = cfg.TeamID
	}
	if qa.repoID == "" {
		qa.repoID = cfg.RepoID
	}

	req := &api.QueryRequest{
		Query:     qa.query,
		Mode:      qa.mode,
		K:         qa.limit,
		AgentID:   agentID,
		AgentType: agentType,
	}
	if qa.teamID != "" {
		req.Teams = []string{qa.teamID}
	}
	if qa.repoID != "" {
		req.Repos = []string{qa.repoID}
	}
	if len(req.Teams) == 0 && len(req.Repos) == 0 {
		return nil, fmt.Errorf("no team or repo ID available. Run 'ox init' first or pass --team/--repo flags")
	}

	ep := endpoint.GetForProject(projectRoot)
	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil || token == nil || token.AccessToken == "" {
		return nil, fmt.Errorf("not authenticated. Run 'ox login' first")
	}

	client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken)

	resp, err := client.Query(req)
	if err != nil {
		if errors.Is(err, api.ErrUnauthorized) {
			return nil, fmt.Errorf("not authenticated. Run 'ox login' first")
		}
		if errors.Is(err, api.ErrVersionUnsupported) {
			return nil, fmt.Errorf("CLI version too old. Run 'ox version' and update")
		}
		if isNetworkError(err) {
			return nil, fmt.Errorf("query failed (is %s reachable?): %w", endpoint.NormalizeEndpoint(ep), err)
		}
		return nil, fmt.Errorf("query failed: %w", err)
	}

	return resp, nil
}

// queryCodeDB searches the local code index.
func queryCodeDB(qa *queryArgs, projectRoot string) ([]search.Result, error) {
	dataDir := resolveCodeDBDir(projectRoot)
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		return nil, nil // no index yet, return empty
	}

	db, err := codedb.Open(dataDir)
	if err != nil {
		return nil, fmt.Errorf("open codedb: %w", err)
	}
	defer db.Close()

	// attach daemon-built dirty overlay for uncommitted file search
	if err := db.AttachDirtyIndex(projectRoot); err != nil {
		slog.Debug("dirty overlay not available, searching committed content only", "err", err)
	}

	return db.Search(context.Background(), qa.query)
}
