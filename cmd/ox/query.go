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
  local     Search locally-cached ledger ONLY (zero network, fast hook path)
  all       Search both team context and local code index

Examples:
  ox query "how do we handle authentication?"
  ox query "database migration patterns" --limit 10
  ox query "deployment process" --team team_abc123
  ox query "error handling" --source=code
  ox query "auth flow" --source=all
  ox query "cache invalidation" --local
  ox query "auth flow" --local --json`,
	Args: cobra.ExactArgs(1),
	RunE: runQuery,
}

func init() {
	queryCmd.Flags().IntP("limit", "k", 5, "max results to return")
	queryCmd.Flags().String("team", "", "team ID to search (default: project config, else the credential's team)")
	queryCmd.Flags().String("repo", "", "repo ID to search (default: from project config)")
	queryCmd.Flags().String("mode", "hybrid", "search mode: hybrid, knn, or bm25")
	queryCmd.Flags().String("source", "team", "search source: team (default), code, local, all")
	// --local is shorthand for --source=local; convenient for hook callers
	queryCmd.Flags().Bool("local", false, "search the locally-cached ledger only (zero network)")
	queryCmd.Flags().Bool("json", false, "emit machine-readable JSON (default in agent context)")
}

// runQuery handles the top-level `ox query "search text"` command.
// Auto-detects agent context when available for server-side analytics.
func runQuery(cmd *cobra.Command, args []string) error {
	limit, _ := cmd.Flags().GetInt("limit")
	teamID, _ := cmd.Flags().GetString("team")
	repoID, _ := cmd.Flags().GetString("repo")
	mode, _ := cmd.Flags().GetString("mode")
	source, _ := cmd.Flags().GetString("source")
	localOnly, _ := cmd.Flags().GetBool("local")
	jsonOut, _ := cmd.Flags().GetBool("json")

	query := strings.TrimSpace(args[0])
	if query == "" {
		return fmt.Errorf("query text is required")
	}

	// --local is sugar for --source=local; --local wins if both given
	if localOnly {
		source = "local"
	}

	qa := &queryArgs{
		query:    query,
		mode:     mode,
		limit:    limit,
		teamID:   teamID,
		repoID:   repoID,
		source:   source,
		jsonOnly: jsonOut,
	}

	// normalize teamctx alias
	if qa.source == "teamctx" {
		qa.source = "team"
	}
	// accept "local-ledger" as a more explicit synonym
	if qa.source == "local-ledger" {
		qa.source = "local"
	}

	switch qa.mode {
	case "hybrid", "knn", "bm25":
		// ok
	default:
		return fmt.Errorf("invalid mode %q: must be hybrid, knn, or bm25", qa.mode)
	}

	switch qa.source {
	case "all", "team", "code", "local":
		// ok
	default:
		return fmt.Errorf("invalid source %q: must be all, team, code, or local", qa.source)
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
