package main

import (
	"fmt"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/dashboard"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(dashboardCmd)
}

var dashboardCmd = &cobra.Command{
	Use:   "dashboard",
	Short: "Open the interactive SageOx dashboard",
	Long:  "Launch a full-screen TUI dashboard showing team context, session activity, sync health, and daemon status.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if agentx.IsAgentContext() {
			return fmt.Errorf("ox dashboard cannot run inside an agent session\nRun in an interactive terminal instead.")
		}
		if !cli.IsInteractive() {
			return fmt.Errorf("ox dashboard requires an interactive terminal\nRemove --no-interactive or run in a TTY.")
		}
		return dashboard.Run(dashboard.DefaultDeps())
	},
}
