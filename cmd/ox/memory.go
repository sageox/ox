package main

import "github.com/spf13/cobra"

var memoryCmd = &cobra.Command{
	Use:   "memory",
	Short: "Record and manage team memory observations",
	Long:  "Record observations, distill summaries, and manage the team memory pipeline.",
}

func init() {
	memoryCmd.AddCommand(memoryPutCmd)
	rootCmd.AddCommand(memoryCmd)
}
