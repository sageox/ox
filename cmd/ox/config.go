package main

import (
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage ox configuration",
	Long: `View and modify ox configuration settings.

Commands:
  ox config                            Interactive config editor (TUI)
  ox config list                       List all settings with values
  ox config get <key>                  Show setting with override chain
  ox config set <key> <value>          Set at user level (default)
  ox config set <key> <value> --repo   Set at repo level
  ox config set <key> <value> --team   Set at team level

Available settings:
  session_recording        disabled | manual | auto
  github_sync              enabled | disabled
  github_sync_prs          enabled | disabled
  github_sync_issues       enabled | disabled
  telemetry                on | off
  tips                     on | off
  context_git.auto_commit  on | off
  context_git.auto_push    on | off

Priority: user > repo > team > default`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// if terminal is interactive, launch TUI
		if term.IsTerminal(int(os.Stdout.Fd())) && len(args) == 0 {
			return runConfigTUI()
		}
		// otherwise show list
		return runConfigList(cmd, args)
	},
}

func init() {
	configCmd.AddCommand(configGetCmd)
	configCmd.AddCommand(configSetCmd)
	configCmd.AddCommand(configListCmd)
	rootCmd.AddCommand(configCmd)
}
