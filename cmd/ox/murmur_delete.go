package main

import (
	"context"
	"fmt"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/ledger"
	"github.com/spf13/cobra"
)

var murmurDeleteCmd = &cobra.Command{
	Use:   "delete <murmur-id>",
	Short: "Delete a murmur by ID",
	Long: `Remove a murmur from the ledger or team context.

The murmur ID can be found via 'ox murmur list --json'.

Examples:
  ox murmur delete 019d3201-87d2-7693-a6ef-aba37863fd18
  ox murmur delete 019d3201-87d2-7693-a6ef-aba37863fd18 --scope=team`,
	Args: cobra.ExactArgs(1),
	RunE: runMurmurDelete,
}

func init() {
	murmurCmd.AddCommand(murmurDeleteCmd)
	murmurDeleteCmd.Flags().String("scope", "ledger", "scope: ledger (this repo) or team (all repos)")
}

func runMurmurDelete(cmd *cobra.Command, args []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a SageOx project: %w", err)
	}

	murmurID := args[0]
	scope, _ := cmd.Flags().GetString("scope")

	targetDir, err := resolveMurmurTarget(projectRoot, scope)
	if err != nil {
		return err
	}

	relPath, err := ledger.FindMurmur(targetDir, murmurID)
	if err != nil {
		return err
	}

	// git rm handles both disk deletion and staging
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := gitutil.RunGit(ctx, targetDir, "rm", "-f", relPath); err != nil {
		return fmt.Errorf("git rm: %w", err)
	}

	commitMsg := fmt.Sprintf("murmur: delete %s", murmurID)
	if _, err := gitutil.RunGit(ctx, targetDir, "commit", "-m", commitMsg); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Murmur deleted: %s\n", murmurID)
	return nil
}
