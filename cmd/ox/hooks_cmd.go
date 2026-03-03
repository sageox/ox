package main

import (
	"github.com/spf13/cobra"
)

// hooksCmd is the parent command for deterministic git/tooling hooks.
//
// These are traditional hooks called by git or CI — predictable input/output,
// no AI reasoning involved. Compare with "ox agent <id> hook" which handles
// AI coworker lifecycle events (SessionStart, PreCompact, etc.) where output
// is consumed by AI agents and may include non-deterministic guidance.
var hooksCmd = &cobra.Command{
	Use:    "hooks",
	Short:  "Deterministic hooks for git and CI tooling",
	Hidden: true, // called by git hooks, not users directly
}

func init() {
	rootCmd.AddCommand(hooksCmd)
}
