package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	gh "github.com/sageox/ox/internal/github"
	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/repotools"

	"github.com/spf13/cobra"
)

// defaultGitHubSyncMaxDays matches ledger.DefaultGitHubDataWindowDays — no point
// fetching more history than the sparse checkout keeps on disk.
const defaultGitHubSyncMaxDays = 30

var indexCmd = &cobra.Command{
	Use:   "index",
	Short: "Index code and project data for search",
	Long: `Build searchable indexes from code, GitHub PRs, issues, and other project data.

Run with no subcommand to index all available sources. Use subcommands to
index specific sources individually.`,
	RunE: runIndexAll,
}

var indexCodeCmd = &cobra.Command{
	Use:   "code [url]",
	Short: "Index git history (commits, symbols, diffs)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		full, _ := cmd.Flags().GetBool("full")

		// ensure daemon is running — indexing happens in the daemon
		if err := daemon.EnsureDaemon(); err != nil {
			return fmt.Errorf("daemon required for indexing: %w", err)
		}

		payload := daemon.CodeIndexPayload{Full: full}
		if len(args) > 0 {
			payload.URL = args[0]
			fmt.Fprintf(cmd.ErrOrStderr(), "Indexing %s...\n", args[0])
		} else if full {
			fmt.Fprintf(cmd.ErrOrStderr(), "Full reindex of local repo...\n")
		} else {
			fmt.Fprintf(cmd.ErrOrStderr(), "Indexing local repo...\n")
		}

		client := daemon.NewClientWithTimeout(5 * time.Minute)
		result, err := client.CodeIndex(payload, func(stage string, percent *int, message string) {
			if message != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", message)
			}
		})
		if err != nil {
			return fmt.Errorf("index: %w", err)
		}

		fmt.Fprintf(cmd.ErrOrStderr(), "Done. Parsed %d blobs, %d symbols\n",
			result.BlobsParsed, result.SymbolsExtracted)
		return nil
	},
}

var indexGitHubCmd = &cobra.Command{
	Use:   "github",
	Short: "Index GitHub PRs and issues",
	Long: `Fetch recent pull requests and issues from GitHub and make them searchable.

Data is extracted to the project ledger and indexed into CodeDB for search
with 'ox code search type:pr' or 'ox code search type:issue'.

Requires a GitHub token (GITHUB_TOKEN, GH_TOKEN, or gh CLI config).
Controlled by github_sync, github_sync_prs, and github_sync_issues
settings in .sageox/config.json (all enabled by default).`,
	RunE: runIndexGitHub,
}

// runIndexAll indexes all available sources (code + github).
func runIndexAll(cmd *cobra.Command, args []string) error {
	var errs []string

	// index code
	fmt.Fprintf(cmd.ErrOrStderr(), "Indexing code...\n")
	if err := indexCodeCmd.RunE(cmd, nil); err != nil {
		errs = append(errs, fmt.Sprintf("code: %v", err))
		slog.Warn("index code failed", "error", err)
	}

	// index github
	fmt.Fprintf(cmd.ErrOrStderr(), "\nIndexing GitHub data...\n")
	if err := runIndexGitHub(cmd, nil); err != nil {
		errs = append(errs, fmt.Sprintf("github: %v", err))
		slog.Warn("index github failed", "error", err)
	}

	if len(errs) > 0 {
		return fmt.Errorf("some sources failed to index:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

func runIndexGitHub(cmd *cobra.Command, args []string) error {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return fmt.Errorf("not in a git repository")
	}

	full, _ := cmd.Flags().GetBool("full")
	prsOnly, _ := cmd.Flags().GetBool("prs-only")
	issuesOnly, _ := cmd.Flags().GetBool("issues-only")

	// check master github_sync toggle
	if config.ResolveGitHubSync(gitRoot) == config.GitHubSyncDisabled {
		fmt.Println("GitHub sync is disabled. Enable with: ox config set github_sync enabled")
		return nil
	}

	// resolve ledger path (needed for sync state stored in ledger cache)
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return fmt.Errorf("resolve ledger: %w", err)
	}

	// --full: selectively clear sync state so items are re-fetched from GitHub.
	if full {
		if err := resetGitHubSyncState(ledgerPath, prsOnly, issuesOnly); err != nil {
			return err
		}
	}

	// detect GitHub remote (graceful skip for non-GitHub repos)
	owner, repo, err := detectGitHubRemote()
	if err != nil {
		slog.Info("no GitHub remote detected, skipping GitHub sync", "error", err)
		fmt.Println("No GitHub remote found — GitHub indexing requires a github.com remote.")
		return nil
	}

	// get GitHub token (graceful skip if not configured)
	token := identity.GetGitHubToken()
	if token == "" {
		slog.Info("no GitHub token found, skipping GitHub sync")
		fmt.Println("No GitHub token found. Set GITHUB_TOKEN, GH_TOKEN, or run 'gh auth login'.")
		return nil
	}

	maxDays, _ := cmd.Flags().GetInt("days")
	if maxDays == 0 {
		maxDays = defaultGitHubSyncMaxDays
	}
	noPush, _ := cmd.Flags().GetBool("no-push")

	// determine which types to sync
	syncPRs := !issuesOnly && config.ResolveGitHubSyncPRs(gitRoot) == config.GitHubSyncEnabled
	syncIssues := !prsOnly && config.ResolveGitHubSyncIssues(gitRoot) == config.GitHubSyncEnabled

	if !syncPRs && !syncIssues {
		fmt.Println("Both PR and issue sync are disabled.")
		return nil
	}

	fetcher := gh.NewFetcher(gh.NewClient(token))
	logger := slog.Default()
	combined := &ledger.SyncResult{}

	if syncPRs {
		prResult, prErr := ledger.SyncPRs(cmd.Context(), fetcher, ledgerPath, owner, repo, maxDays, logger)
		if prErr != nil {
			return fmt.Errorf("sync PRs: %w", prErr)
		}
		combined.PRTotal = prResult.PRTotal
		combined.PRCreated = prResult.PRCreated
		combined.PRUpdated = prResult.PRUpdated
	}

	if syncIssues {
		issueResult, issueErr := ledger.SyncIssues(cmd.Context(), fetcher, ledgerPath, owner, repo, maxDays, logger)
		if issueErr != nil {
			return fmt.Errorf("sync issues: %w", issueErr)
		}
		combined.IssueTotal = issueResult.IssueTotal
		combined.IssueCreated = issueResult.IssueCreated
		combined.IssueUpdated = issueResult.IssueUpdated
	}

	if syncPRs {
		fmt.Printf("Synced %d PRs (%d new, %d updated) from %s/%s\n",
			combined.PRTotal, combined.PRCreated, combined.PRUpdated, owner, repo)
	}
	if syncIssues {
		fmt.Printf("Synced %d issues (%d new, %d updated) from %s/%s\n",
			combined.IssueTotal, combined.IssueCreated, combined.IssueUpdated, owner, repo)
	}

	if noPush {
		return nil
	}

	if err := ledger.CommitAndPushGitHubData(context.Background(), ledgerPath, owner, repo, combined, pushLedger); err != nil {
		return fmt.Errorf("push to ledger: %w", err)
	}

	fmt.Println("GitHub data pushed to ledger.")
	return nil
}

// detectGitHubRemote finds the GitHub owner/repo from git remotes.
func detectGitHubRemote() (owner, repo string, err error) {
	urls, err := repotools.GetRemoteURLs()
	if err != nil {
		return "", "", fmt.Errorf("get git remotes: %w", err)
	}

	for _, url := range urls {
		o, r, ok := gh.ParseGitHubRemote(url)
		if ok {
			return o, r, nil
		}
	}

	return "", "", fmt.Errorf("no GitHub remote found (indexing requires a github.com remote)")
}

// resetGitHubSyncState selectively clears sync state for a full re-fetch.
func resetGitHubSyncState(ledgerPath string, prsOnly, issuesOnly bool) error {
	if !prsOnly && !issuesOnly {
		cacheDir := ledger.GitHubSyncCacheDir(ledgerPath)
		if err := os.RemoveAll(cacheDir); err != nil {
			return fmt.Errorf("remove github sync cache: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Cleared all GitHub sync state, re-fetching from scratch...\n")
		return nil
	}

	if prsOnly {
		if err := ledger.ResetGitHubTypeSyncState(ledgerPath, "pr"); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Cleared PR sync state, re-fetching PRs from scratch...\n")
	}
	if issuesOnly {
		if err := ledger.ResetGitHubTypeSyncState(ledgerPath, "issue"); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Cleared issue sync state, re-fetching issues from scratch...\n")
	}
	return nil
}

func init() {
	// ox index code flags
	indexCodeCmd.Flags().Bool("full", false, "wipe index and rebuild from scratch")

	// ox index github flags
	indexGitHubCmd.Flags().IntP("days", "d", defaultGitHubSyncMaxDays, "max days of history to fetch")
	indexGitHubCmd.Flags().Bool("no-push", false, "extract to ledger without pushing")
	indexGitHubCmd.Flags().Bool("prs-only", false, "index only pull requests")
	indexGitHubCmd.Flags().Bool("issues-only", false, "index only issues")
	indexGitHubCmd.Flags().Bool("full", false, "clear sync state and re-fetch everything")

	// propagate common flags to parent for runIndexAll
	indexCmd.Flags().Bool("full", false, "wipe all indexes and rebuild from scratch")
	indexCmd.Flags().IntP("days", "d", defaultGitHubSyncMaxDays, "max days of GitHub history to fetch")
	indexCmd.Flags().Bool("no-push", false, "extract to ledger without pushing")
	indexCmd.Flags().Bool("prs-only", false, "index only pull requests (GitHub)")
	indexCmd.Flags().Bool("issues-only", false, "index only issues (GitHub)")

	indexCmd.AddCommand(indexCodeCmd)
	indexCmd.AddCommand(indexGitHubCmd)
	indexCmd.GroupID = "dev"
	rootCmd.AddCommand(indexCmd)
}
