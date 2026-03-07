package main

import (
	"bytes"
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
}

// parseQueryArgs extracts flags and the positional query from raw args.
// --limit not --k: self-describing flag names over ML jargon;
// agents and humans guess --limit first
func parseQueryArgs(args []string) (*queryArgs, error) {
	qa := &queryArgs{
		mode:  "hybrid",
		limit: 5,
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

	return qa, nil
}

const queryUsage = `Usage: ox query "search text" [flags]

Flags:
  --limit N    Max results to return (default: 5)
  --team ID    Team ID to search (default: from project config)
  --repo ID    Repo ID to search (default: from project config)

Searches across team discussions, docs, and session history.
Use when MEMORY.md or AGENTS.md don't have the answer.

Also available as: ox agent <id> query "search text"`

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

// executeQuery performs the core query: resolves project config, calls the API,
// and writes JSON results to stdout. Returns bytes written for context tracking.
// agentID and agentType are optional — passed to the server for analytics.
func executeQuery(qa *queryArgs, agentID string, agentType string) (int, error) {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return 0, fmt.Errorf("could not find project root: %w", err)
	}

	cfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil {
		return 0, fmt.Errorf("could not load project config: %w", err)
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
		return 0, fmt.Errorf("no team or repo ID available. Run 'ox init' first or pass --team/--repo flags")
	}

	ep := endpoint.GetForProject(projectRoot)
	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil || token == nil || token.AccessToken == "" {
		return 0, fmt.Errorf("not authenticated. Run 'ox login' first")
	}

	client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken)

	resp, err := client.Query(req)
	if err != nil {
		if errors.Is(err, api.ErrUnauthorized) {
			return 0, fmt.Errorf("not authenticated. Run 'ox login' first")
		}
		if errors.Is(err, api.ErrVersionUnsupported) {
			return 0, fmt.Errorf("CLI version too old. Run 'ox version' and update")
		}
		return 0, fmt.Errorf("query failed (is sageox.ai reachable?): %w", err)
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
