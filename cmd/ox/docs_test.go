package main

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestPrepareDocsCommandTreeExcludesExperimentalCommands(t *testing.T) {
	root := &cobra.Command{Use: "ox"}
	attest := &cobra.Command{Use: "attest"}
	scout := &cobra.Command{Use: "scout"}
	memory := &cobra.Command{Use: "memory"}
	completion := &cobra.Command{Use: "completion"}
	carts := &cobra.Command{Use: "carts"}
	cartAnalyze := &cobra.Command{Use: "cart-analyze"}
	root.AddCommand(attest, scout, memory, carts, cartAnalyze, completion)

	prepareDocsCommandTree(root)

	for _, command := range []*cobra.Command{attest, scout, memory, completion} {
		if commandRegistered(root, command) {
			t.Errorf("%s command remains registered", command.Name())
		}
	}
	if !carts.Hidden {
		t.Error("carts command is visible")
	}
	if !cartAnalyze.Hidden {
		t.Error("cart-analyze command is visible")
	}
	if !root.CompletionOptions.DisableDefaultCmd {
		t.Error("default completion command remains enabled")
	}
}
