package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

var codedbCmd = &cobra.Command{
	Use:   "codedb",
	Short: "Code search and indexing (powered by CodeDB)",
	Long: `Sourcegraph-style code search across git repositories.

CodeDB indexes git repos (including full commit history) and supports
full-text search, symbol extraction, and commit/diff search.

Data is stored at ~/.local/share/sageox/codedb/ (managed by codedb).

Requires the codedb binary in PATH.`,
}

func init() {
	codedbCmd.GroupID = "dev"
	codedbCmd.AddCommand(codedbIndexCmd)
	codedbCmd.AddCommand(codedbSearchCmd)
	codedbCmd.AddCommand(codedbSQLCmd)
	rootCmd.AddCommand(codedbCmd)
}

// findCodeDB locates the codedb binary in PATH.
func findCodeDB() (string, error) {
	path, err := exec.LookPath("codedb")
	if err != nil {
		return "", fmt.Errorf(
			"codedb not found in PATH.\n\n" +
				"Install CodeDB:\n" +
				"  go install github.com/sageox/CodeDBGo/cmd/codedb@latest\n\n" +
				"Or build from source:\n" +
				"  cd CodeDBGo && make build && make install")
	}
	return path, nil
}

// runCodeDB executes the codedb binary with the given arguments,
// streaming stdout/stderr to the terminal.
func runCodeDB(bin string, args []string) error {
	c := exec.Command(bin, args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	if err := c.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("failed to run codedb: %w", err)
	}
	return nil
}
