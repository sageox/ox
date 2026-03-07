package main

import (
	"github.com/spf13/cobra"
)

var codedbSQLCmd = &cobra.Command{
	Use:                "sql [query]",
	Short:              "Run raw SQL against the metadata database",
	Long:               "Execute SQL queries directly against the CodeDB metadata database.\n\nRun `codedb sql --help` for full options.",
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		bin, err := findCodeDB()
		if err != nil {
			return err
		}
		return runCodeDB(bin, append([]string{"sql"}, args...))
	},
}
