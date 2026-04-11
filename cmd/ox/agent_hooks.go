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

This is the canonical path for git hooks. Legacy 'ox hooks <git-subcommand>'
still works for backward compatibility.

Available subcommands are registered by the same hooks that serve 'ox hooks'.
Use 'ox hooks --help' to see all available git hook subcommands.

Examples:
  ox agent hooks pre-commit
  ox agent hooks post-commit
  ox agent hooks commit-msg`,
}

func init() {
	agentCmd.AddCommand(agentHooksCmd)
}
