package main

import (
	"fmt"
	"os"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/pkg/agentx"
	"github.com/spf13/cobra"
)

var queryCmd = &cobra.Command{
	Use:   "query",
	Short: "Search team knowledge",
	Long: `Search across team discussions, docs, and session history.

Examples:
  ox query "how do we handle authentication?"
  ox query "database migration patterns" --limit 10
  ox query "deployment process" --team team_abc123

Flags:
  --limit N    Max results to return (default: 5)
  --team ID    Team ID to search (default: from project config)
  --repo ID    Repo ID to search (default: from project config)`,
	Args:               cobra.ArbitraryArgs,
	DisableFlagParsing: true,
	RunE:               runQuery,
}

// runQuery handles the top-level `ox query "search text"` command.
// Auto-detects agent context when available for server-side analytics.
func runQuery(cmd *cobra.Command, args []string) error {
	// handle --help manually since DisableFlagParsing is true
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return cmd.Help()
		}
	}

	qa, err := parseQueryArgs(args)
	if err != nil {
		return fmt.Errorf("%w\n\n%s", err, queryUsage)
	}

	agentID, agentType := detectAgentContext()

	_, err = executeQuery(qa, agentID, agentType)
	return err
}

// detectAgentContext returns the agent ID and type if running inside an agent session.
// Uses layered detection:
//  1. SAGEOX_AGENT_ID env var → instance store lookup (gives both ID + type)
//  2. agentx runtime detection (type only, covers agents that haven't primed)
//  3. Returns empty strings if no agent detected
func detectAgentContext() (agentID string, agentType string) {
	// try instance store lookup first — gives both ID and type
	if envID := os.Getenv("SAGEOX_AGENT_ID"); agentinstance.IsValidAgentID(envID) {
		inst, err := resolveInstance(envID)
		if err == nil {
			return inst.AgentID, inst.AgentType
		}
		// instance lookup failed but we still have the ID
		agentID = envID
	}

	// fall back to runtime agent detection for type
	if agent := agentx.CurrentAgent(); agent != nil {
		agentType = string(agent.Type())
	}

	return agentID, agentType
}
