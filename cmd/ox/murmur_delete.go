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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := commitMurmurDeletion(ctx, targetDir, relPath, murmurID); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Murmur deleted: %s\n", murmurID)
	return nil
}

// murmurDeleteGitTimeout bounds the rm and commit once the repo lock is held.
const murmurDeleteGitTimeout = 30 * time.Second

// commitMurmurDeletion removes relPath and commits the removal under the repo
// lock, so the commit cannot move the branch under a daemon sync rebase
// (sageox/ox#1541). git rm handles both disk deletion and staging. ctx bounds
// only the lock wait: the git commands get their own budget, so a long wait
// cannot expire between the rm and the commit and strand a staged removal.
func commitMurmurDeletion(ctx context.Context, targetDir, relPath, murmurID string) error {
	return gitutil.WithRepoLock(ctx, targetDir, func() error {
		gitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), murmurDeleteGitTimeout)
		defer cancel()
		if err := gitutil.IsSafeForIndexWrite(gitCtx, targetDir); err != nil {
			return err
		}
		if _, err := gitutil.RunGit(gitCtx, targetDir, "rm", "-f", "--sparse", "--", ":(literal)"+relPath); err != nil {
			return fmt.Errorf("git rm: %w", err)
		}
		if _, err := gitutil.CommitLedgerSnapshot(gitCtx, targetDir, fmt.Sprintf("murmur: delete %s", murmurID), ":(literal)"+relPath); err != nil {
			return fmt.Errorf("git commit: %w", err)
		}
		return nil
	})
}
