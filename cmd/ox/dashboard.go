package main

import (
	"fmt"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/dashboard"
	"github.com/sageox/ox/internal/flags"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(dashboardCmd)
}

var dashboardCmd = &cobra.Command{
	Use:    "dashboard",
	Short:  "Open the interactive SageOx dashboard",
	Long:   "Launch a full-screen TUI dashboard showing team context, session activity, sync health, and daemon status.",
	Hidden: true, // visible once server-side or env flag enables TUI
	RunE: func(cmd *cobra.Command, args []string) error {
		if !flags.Get().TUIEnabled {
			return fmt.Errorf("ox dashboard is not yet enabled — set FEATURE_TUI=true or contact your team admin")
		}
		if agentx.IsAgentContext() {
			return fmt.Errorf("ox dashboard cannot run inside an agent session — run in an interactive terminal instead")
		}
		if !cli.IsInteractive() {
			return fmt.Errorf("ox dashboard requires an interactive terminal — remove --no-interactive or run in a TTY")
		}
		return dashboard.Run(dashboard.DefaultDeps())
	},
}
