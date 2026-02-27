package main

import (
	"github.com/spf13/cobra"
)

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage AI coworker sessions",
	Long: `View and manage sessions of human + AI coworker conversations.

Sessions are recordings of interactive discussions between developers
and AI coworkers. Capture struggles, solutions, and learnings to build
collective team knowledge.

Commands:
  ox session list      List all sessions
  ox session view      View a session (html, text, or json)
  ox session status    Check recording status`,
}

func init() {
	sessionCmd.GroupID = "dev"

	// register subcommands
	sessionCmd.AddCommand(sessionCommitCmd)
	sessionCmd.AddCommand(sessionHydrateCmd)
	sessionCmd.AddCommand(sessionUploadCmd)
	sessionCmd.AddCommand(sessionPushSummaryCmd)

	// TODO(post-MVP): commit, download, and upload should be automated.
	// Users should only need start/stop — the rest is implementation detail.
	// Currently surfaced because automation isn't reliable enough yet.

	rootCmd.AddCommand(sessionCmd)
}
