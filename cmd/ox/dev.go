package main

import "github.com/spf13/cobra"

// devCmd is a hidden umbrella for maintainer-only utilities. It does not
// appear in `ox --help` and is not part of the user-facing contract.
// Each child is also Hidden: true; see .claude/rules/design.md rule 10.
var devCmd = &cobra.Command{
	Use:    "dev",
	Short:  "Developer/maintainer utilities (hidden)",
	Hidden: true,
}

func init() {
	rootCmd.AddCommand(devCmd)
}
