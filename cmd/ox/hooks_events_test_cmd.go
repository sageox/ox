package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/sageox/ox/internal/daemon/hooks"
	"github.com/spf13/cobra"
)

var hooksEventsTestCmd = &cobra.Command{
	Use:   "test <event>",
	Short: "Fire a synthetic event to test hooks",
	Long: `Fires a synthetic event through the hook runner to verify hooks work.

The event is dispatched to all matching hooks with a test payload.
Hooks have a 500ms timeout. Results are printed after ~700ms.

Example:
  ox hooks test murmur.received
  ox hooks test daemon.started`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		eventName := args[0]
		if eventName != "*" && !hooks.ValidEvent(eventName) {
			return fmt.Errorf("unknown event %q", eventName)
		}

		cfgs, err := hooks.LoadHooks()
		if err != nil {
			return fmt.Errorf("failed to load hooks: %w", err)
		}

		if len(cfgs) == 0 {
			fmt.Println("No hooks configured. Use 'ox hooks add' first.")
			return nil
		}

		// count matching hooks
		var matching int
		for _, c := range cfgs {
			if c.Event == "*" || c.Event == eventName {
				matching++
			}
		}

		if matching == 0 {
			fmt.Printf("No hooks match event %q.\n", eventName)
			return nil
		}

		logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
		runner := hooks.NewHookRunner(cfgs, logger)

		event := hooks.Event{
			Name:    eventName,
			Project: "/tmp/ox-hooks-test",
			RepoID:  "repo_test",
			Payload: map[string]any{"test": true},
		}

		fmt.Printf("Firing %q to %d hook(s)...\n", eventName, matching)
		runner.Dispatch(context.Background(), event)

		// wait for hooks to complete (500ms timeout + margin)
		time.Sleep(700 * time.Millisecond)
		fmt.Printf("Done. Check stderr for hook execution logs.\n")
		return nil
	},
}

func init() {
	hooksCmd.AddCommand(hooksEventsTestCmd)
}
