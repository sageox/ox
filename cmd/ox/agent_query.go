package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
)

// runAgentQuery handles `ox agent <id> query "search text"`.
// Searches team context and ledger data via the vector search API.
func runAgentQuery(inst *agentinstance.Instance, args []string) error {
	// Parse flags manually (cobra isn't wired for dispatcher subcommands)
	var (
		mode   = "hybrid"
		k      = 5
		teamID string
		repoID string
		query  string
	)

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--mode" && i+1 < len(args):
			mode = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--mode="):
			mode = strings.TrimPrefix(args[i], "--mode=")
		case args[i] == "--k" && i+1 < len(args):
			fmt.Sscanf(args[i+1], "%d", &k)
			i++
		case strings.HasPrefix(args[i], "--k="):
			fmt.Sscanf(strings.TrimPrefix(args[i], "--k="), "%d", &k)
		case args[i] == "--team" && i+1 < len(args):
			teamID = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--team="):
			teamID = strings.TrimPrefix(args[i], "--team=")
		case args[i] == "--repo" && i+1 < len(args):
			repoID = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--repo="):
			repoID = strings.TrimPrefix(args[i], "--repo=")
		case !strings.HasPrefix(args[i], "--"):
			query = args[i]
		}
	}

	if query == "" {
		return fmt.Errorf("query text is required\nUsage: ox agent %s query \"your search query\"", inst.AgentID)
	}

	// Validate mode
	switch mode {
	case "hybrid", "knn", "bm25":
		// ok
	default:
		return fmt.Errorf("invalid mode %q: must be hybrid, knn, or bm25", mode)
	}

	// Resolve project config for team/repo IDs
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	cfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil {
		return fmt.Errorf("could not load project config: %w", err)
	}

	// Use project config defaults if not overridden
	if teamID == "" {
		teamID = cfg.TeamID
	}
	if repoID == "" {
		repoID = cfg.RepoID
	}

	// Build request — include whichever IDs are available
	req := &api.QueryRequest{
		Query: query,
		Mode:  mode,
		K:     k,
	}
	if teamID != "" {
		req.Teams = []string{teamID}
	}
	if repoID != "" {
		req.Repos = []string{repoID}
	}
	if len(req.Teams) == 0 && len(req.Repos) == 0 {
		return fmt.Errorf("no team or repo ID available. Run 'ox init' first or pass --team/--repo flags")
	}

	// Get auth token and create client
	ep := endpoint.GetForProject(projectRoot)
	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil || token == nil || token.AccessToken == "" {
		return fmt.Errorf("not authenticated. Run 'ox login' first")
	}

	client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken)

	// Execute query
	resp, err := client.Query(req)
	if err != nil {
		return fmt.Errorf("query failed: %w", err)
	}

	// Output results as JSON (default agent output)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(resp)
}
