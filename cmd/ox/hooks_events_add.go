package main

import (
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/daemon/hooks"
	"github.com/spf13/cobra"
)

var hooksEventsAddCmd = &cobra.Command{
	Use:   "add <event> <command>",
	Short: "Register a new event hook",
	Long: `Register a hook script to run when a daemon event fires.

The command receives the event as JSON on stdin and has a 500ms timeout.

Valid events:
  ` + strings.Join(hooks.AllEventNames(), "\n  ") + `
  * (matches all events)

Examples:
  ox hooks add murmur.received 'notify-send "Murmur" "$(cat)"'
  ox hooks add session.uploaded 'curl -X POST https://slack.example.com/hook -d @-'
  ox hooks add '*' 'logger -t ox'`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		event := args[0]
		command := args[1]

		if event != "*" && !hooks.ValidEvent(event) {
			return fmt.Errorf("unknown event %q; valid events:\n  %s\n  *", event, strings.Join(hooks.AllEventNames(), "\n  "))
		}

		hook := hooks.HookConfig{Event: event, Command: command}
		if err := hooks.SaveHook(hook); err != nil {
			return fmt.Errorf("failed to save hook: %w", err)
		}

		fmt.Printf("Hook added: %s → %s\n", event, command)
		fmt.Printf("Config: %s\n", hooks.HooksFilePath())
		fmt.Println("Note: restart the daemon to pick up new hooks (ox daemon restart)")
		return nil
	},
}

func init() {
	hooksCmd.AddCommand(hooksEventsAddCmd)
}
