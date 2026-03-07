package main

import (
	"github.com/spf13/cobra"
)

var codedbSearchCmd = &cobra.Command{
	Use:                "search [query] [flags...]",
	Short:              "Search indexed code (Sourcegraph-style queries)",
	Long:               "Search across indexed repositories using Sourcegraph-style query syntax.\n\nRun `codedb search --help` for full options and query syntax.",
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		bin, err := findCodeDB()
		if err != nil {
			return err
		}
		return runCodeDB(bin, append([]string{"search"}, args...))
	},
}
