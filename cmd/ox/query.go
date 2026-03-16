package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/agentinstance"
	"github.com/spf13/cobra"
)

var queryCmd = &cobra.Command{
	Use:   "query",
	Short: "Search team knowledge",
	Long: `Search across team discussions, docs, session history, and local code.

Sources:
  team      Search team discussions, docs, and session history (default)
  code      Search local code index only (queries)
  all       Search both team context and local code index

Examples:
  ox query "how do we handle authentication?"
  ox query "database migration patterns" --limit 10
  ox query "deployment process" --team team_abc123
  ox query "error handling" --source=code
  ox query "auth flow" --source=all`,
	Args: cobra.ExactArgs(1),
	RunE: runQuery,
}

func init() {
	queryCmd.Flags().IntP("limit", "k", 5, "max results to return")
	queryCmd.Flags().String("team", "", "team ID to search (default: from project config)")
	queryCmd.Flags().String("repo", "", "repo ID to search (default: from project config)")
	queryCmd.Flags().String("mode", "hybrid", "search mode: hybrid, knn, or bm25")
	queryCmd.Flags().String("source", "team", "search source: team (default), code, all")
}

// runQuery handles the top-level `ox query "search text"` command.
// Auto-detects agent context when available for server-side analytics.
func runQuery(cmd *cobra.Command, args []string) error {
	limit, _ := cmd.Flags().GetInt("limit")
	teamID, _ := cmd.Flags().GetString("team")
	repoID, _ := cmd.Flags().GetString("repo")
	mode, _ := cmd.Flags().GetString("mode")
	source, _ := cmd.Flags().GetString("source")

	query := strings.TrimSpace(args[0])
	if query == "" {
		return fmt.Errorf("query text is required")
	}

	qa := &queryArgs{
		query:  query,
		mode:   mode,
		limit:  limit,
		teamID: teamID,
		repoID: repoID,
		source: source,
	}

	// normalize teamctx alias
	if qa.source == "teamctx" {
		qa.source = "team"
	}

	switch qa.mode {
	case "hybrid", "knn", "bm25":
		// ok
	default:
		return fmt.Errorf("invalid mode %q: must be hybrid, knn, or bm25", qa.mode)
	}

	switch qa.source {
	case "all", "team", "code":
		// ok
	default:
		return fmt.Errorf("invalid source %q: must be all, team, or code", qa.source)
	}

	agentID, agentType := detectAgentContext()

	outputBytes, err := executeQuery(qa, agentID, agentType)
	if err != nil {
		return err
	}

	if agentID != "" {
		slog.Debug("query response context cost", "agent_id", agentID, "bytes", outputBytes)
		trackContextBytes(int64(outputBytes))
	}
	return nil
}

// detectAgentContext returns the agent ID and type if running inside an agent session.
// Uses layered detection:
//  1. SAGEOX_AGENT_ID env var → instance store lookup (gives both ID + type)
//  2. agentx runtime detection (type only, covers agents that haven't primed)
//  3. Returns empty strings if no agent detected
func detectAgentContext() (agentID string, agentType string) {
	// try instance store lookup first — gives both ID and type
	if envID := os.Getenv("SAGEOX_AGENT_ID"); agentinstance.IsValidAgentID(envID) {
		agentID = envID
		inst, err := resolveInstance(envID)
		if err == nil {
			agentID = inst.AgentID
			agentType = inst.AgentType
		}
	}

	// fall back to runtime agent detection for type when instance lookup
	// didn't provide one (missing instance or empty AgentType field)
	if agentType == "" {
		if agent := agentx.CurrentAgent(); agent != nil {
			agentType = string(agent.Type())
		}
	}

	return agentID, agentType
}
