package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var hooksEventsLogCmd = &cobra.Command{
	Use:   "log",
	Short: "Show how to view hook execution logs",
	Long:  `Hook execution is logged to the daemon log. This command shows how to find those entries.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("Hook execution is logged to the daemon log.")
		fmt.Println()
		fmt.Println("View hook entries:")
		fmt.Println("  ox doctor --logs | grep hook")
		fmt.Println()
		fmt.Println("Hook log levels:")
		fmt.Println("  DEBUG  hook completed (exit 0)")
		fmt.Println("  WARN   hook failed (non-zero exit)")
		fmt.Println("  WARN   hook timed out (>500ms)")
		return nil
	},
}

func init() {
	hooksCmd.AddCommand(hooksEventsLogCmd)
}
