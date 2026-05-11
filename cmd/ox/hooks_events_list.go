package main

import (
	"encoding/json"
	"fmt"

	"github.com/sageox/ox/internal/daemon/hooks"
	"github.com/spf13/cobra"
)

var hooksEventsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List registered event hooks",
	Long:  `Lists all event hooks registered in hooks.yaml.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfgs, err := hooks.LoadHooks()
		if err != nil {
			return fmt.Errorf("failed to load hooks: %w", err)
		}

		jsonOut, _ := cmd.Flags().GetBool("json")
		out := cmd.OutOrStdout()

		if jsonOut {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			if cfgs == nil {
				cfgs = []hooks.HookConfig{}
			}
			return enc.Encode(cfgs)
		}

		if len(cfgs) == 0 {
			fmt.Fprintln(out, "No event hooks configured.")
			fmt.Fprintf(out, "Use 'ox hooks add <event> <command>' to register one.\n")
			fmt.Fprintf(out, "Config: %s\n", hooks.HooksFilePath())
			return nil
		}

		fmt.Fprintf(out, "%-25s %s\n", "EVENT", "COMMAND")
		for _, c := range cfgs {
			fmt.Fprintf(out, "%-25s %s\n", c.Event, c.Command)
		}
		fmt.Fprintf(out, "\nConfig: %s\n", hooks.HooksFilePath())
		return nil
	},
}

func init() {
	hooksEventsListCmd.Flags().Bool("json", false, "Output as JSON")
	hooksCmd.AddCommand(hooksEventsListCmd)
}
