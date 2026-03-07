package main

import (
	"github.com/spf13/cobra"
)

var codedbIndexCmd = &cobra.Command{
	Use:                "index [url] [flags...]",
	Short:              "Index a git repository",
	Long:               "Clone and index a git repository for code search.\n\nRun `codedb index --help` for full options.",
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		bin, err := findCodeDB()
		if err != nil {
			return err
		}
		return runCodeDB(bin, append([]string{"index"}, args...))
	},
}
