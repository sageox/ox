package main

import (
	"encoding/json"
	"fmt"
	"os"

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

		if jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if cfgs == nil {
				cfgs = []hooks.HookConfig{}
			}
			return enc.Encode(cfgs)
		}

		if len(cfgs) == 0 {
			fmt.Println("No event hooks configured.")
			fmt.Printf("Use 'ox hooks add <event> <command>' to register one.\n")
			fmt.Printf("Config: %s\n", hooks.HooksFilePath())
			return nil
		}

		fmt.Printf("%-25s %s\n", "EVENT", "COMMAND")
		for _, c := range cfgs {
			fmt.Printf("%-25s %s\n", c.Event, c.Command)
		}
		fmt.Printf("\nConfig: %s\n", hooks.HooksFilePath())
		return nil
	},
}

func init() {
	hooksEventsListCmd.Flags().Bool("json", false, "Output as JSON")
	hooksCmd.AddCommand(hooksEventsListCmd)
}
