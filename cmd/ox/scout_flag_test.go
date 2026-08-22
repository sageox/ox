package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestScoutFeatureGatedByDefault guards the real wiring: syncFeatureGatedCommands
// must register scoutCmd only when FEATURE_SCOUT is enabled, so scout is absent
// from --help and unknown-command by default. Unlike the generic
// setCommandRegistered fixture in attest_flag_test.go, this drives the actual
// scout gate — it fails if someone re-adds an unconditional rootCmd.AddCommand(scoutCmd)
// or forgets to key the sync on auth.IsScoutEnabled().
// Failure prevented: scout shipping enabled-by-default and advertising a
// third-party Perplexity call no one opted into.
func TestScoutFeatureGatedByDefault(t *testing.T) {
	root := &cobra.Command{Use: "ox"}
	// Detach the shared command globals from this throwaway root when done so a
	// FEATURE_SCOUT=1 branch below can't leave scoutCmd.parent dangling.
	t.Cleanup(func() {
		root.RemoveCommand(scoutCmd)
		root.RemoveCommand(attestCmd)
	})

	t.Setenv("FEATURE_SCOUT", "")
	syncFeatureGatedCommands(root)
	if commandRegistered(root, scoutCmd) {
		t.Fatal("scout registered with FEATURE_SCOUT unset; must be off by default")
	}

	t.Setenv("FEATURE_SCOUT", "1")
	syncFeatureGatedCommands(root)
	if !commandRegistered(root, scoutCmd) {
		t.Fatal("scout not registered with FEATURE_SCOUT=1; opt-in gate is broken")
	}
}

func commandRegistered(root, command *cobra.Command) bool {
	for _, child := range root.Commands() {
		if child == command {
			return true
		}
	}
	return false
}
