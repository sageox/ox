package main

import (
	"github.com/spf13/cobra"
)

// agentHooksCmd is the canonical path for deterministic git/CI hooks
// that agent integrations call into ox. This replaces the legacy
// direct 'ox hooks <git-subcommand>' path for new integrations.
//
// The old 'ox hooks pre-commit' etc. still work for backward compat.
var agentHooksCmd = &cobra.Command{
	Use:   "hooks",
	Short: "Git and CI hooks for agent integrations",
	Long: `Deterministic hooks called by git and CI tooling via agent integrations.

The git hook subcommands are currently served by 'ox hooks <git-subcommand>';
'ox agent hooks' has no subcommands of its own yet.
Use 'ox hooks --help' to see all available git hook subcommands.

Examples:
  ox hooks post-commit
  ox hooks commit-msg
  ox hooks pre-push`,
}

func init() {
	agentCmd.AddCommand(agentHooksCmd)
}
