package main

import (
	"github.com/spf13/cobra"
)

// hooksCmd is the parent command for event hooks and git/CI hooks.
//
// User-facing subcommands (list, add, test, log) manage daemon event hooks.
// The git subcommands (pre-push, post-commit, post-rewrite, commit-msg) are run
// by the git hooks ox installs, not typed by hand.
var hooksCmd = &cobra.Command{
	Use:   "hooks",
	Short: "Manage event hooks for daemon notifications",
	Long: `Manage event hooks triggered by daemon events (session uploads, murmurs, sync).

Event hook subcommands:
  ox hooks list              List registered event hooks
  ox hooks add <event> <cmd> Register a new event hook
  ox hooks log               Show how to view hook execution logs

Git hooks (run by the git hooks ox installs):
  ox hooks pre-push          Record PR/issue linkage on push
  ox hooks post-commit       Run post-commit actions
  ox hooks post-rewrite      Keep recorded commit SHAs current after a rebase
  ox hooks commit-msg        Validate commit messages`,
}

func init() {
	rootCmd.AddCommand(hooksCmd)
}
