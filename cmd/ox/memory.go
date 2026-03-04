package main

import (
	"github.com/sageox/ox/internal/auth"
	"github.com/spf13/cobra"
)

var memoryCmd = &cobra.Command{
	Use:   "memory",
	Short: "Record and manage team memory observations",
	Long:  "Record observations, distill summaries, and manage the team memory pipeline.",
}

func init() {
	memoryCmd.AddCommand(memoryPutCmd)
	memoryCmd.AddCommand(memoryDistillCmd)
	if auth.IsMemoryEnabled() {
		rootCmd.AddCommand(memoryCmd)
	}
}
