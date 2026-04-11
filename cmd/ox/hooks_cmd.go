package main

import (
	"github.com/spf13/cobra"
)

// hooksCmd is the parent command for event hooks and git/CI hooks.
//
// User-facing subcommands (list, add, test, log) manage daemon event hooks.
// Git/CI subcommands (pre-commit, post-commit, etc.) are called by agent
// integrations — prefer "ox agent hooks" for new integrations.
var hooksCmd = &cobra.Command{
	Use:   "hooks",
	Short: "Manage event hooks for daemon notifications",
	Long: `Manage event hooks triggered by daemon events (session uploads, murmurs, sync).

Event hook subcommands:
  ox hooks list              List registered event hooks
  ox hooks add <event> <cmd> Register a new event hook
  ox hooks test <event>      Fire a synthetic event to test hooks
  ox hooks log               Show how to view hook execution logs

Git/CI hooks (backward compat, prefer 'ox agent hooks'):
  ox hooks pre-commit        Run pre-commit checks
  ox hooks post-commit       Run post-commit actions
  ox hooks commit-msg        Validate commit messages`,
}

func init() {
	rootCmd.AddCommand(hooksCmd)
}
