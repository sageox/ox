package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
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
	// Composes with --prs-only/--issues-only to allow targeted rebuilds:
	//   ox index github --full                → reset all sync state
	//   ox index github --full --prs-only     → reset PR state only
	//   ox index github --full --issues-only  → reset issue state only
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

	result, err := syncGitHubToLedger(cmd.Context(), ledgerPath, owner, repo, token, maxDays, syncPRs, syncIssues)
	if err != nil {
		return fmt.Errorf("sync GitHub data: %w", err)
	}

	if syncPRs {
		fmt.Printf("Synced %d PRs (%d new, %d updated) from %s/%s\n",
			result.prTotal, result.prCreated, result.prUpdated, owner, repo)
	}
	if syncIssues {
		fmt.Printf("Synced %d issues (%d new, %d updated) from %s/%s\n",
			result.issueTotal, result.issueCreated, result.issueUpdated, owner, repo)
	}

	totalItems := result.prTotal + result.issueTotal
	if totalItems == 0 || noPush {
		return nil
	}

	if err := commitAndPushGitHubData(ledgerPath, owner, repo, result); err != nil {
		return fmt.Errorf("push to ledger: %w", err)
	}

	fmt.Println("GitHub data pushed to ledger.")
	return nil
}

type githubSyncResult struct {
	prTotal      int
	prCreated    int
	prUpdated    int
	issueTotal   int
	issueCreated int
	issueUpdated int
}

func syncGitHubToLedger(ctx context.Context, ledgerPath, owner, repo, token string, maxDays int, syncPRs, syncIssues bool) (*githubSyncResult, error) {
	client := gh.NewClient(token)
	result := &githubSyncResult{}

	if syncPRs {
		if err := syncPRsToLedger(ctx, client, ledgerPath, owner, repo, maxDays, result); err != nil {
			return result, fmt.Errorf("sync PRs: %w", err)
		}
	}

	if syncIssues {
		if err := syncIssuesToLedger(ctx, client, ledgerPath, owner, repo, maxDays, result); err != nil {
			return result, fmt.Errorf("sync issues: %w", err)
		}
	}

	return result, nil
}

func syncPRsToLedger(ctx context.Context, client *gh.Client, ledgerPath, owner, repo string, maxDays int, result *githubSyncResult) error {
	state, err := ledger.ReadGitHubTypeSyncState(ledgerPath, "pr")
	if err != nil {
		return fmt.Errorf("read pr sync state: %w", err)
	}

	since := time.Now().AddDate(0, 0, -maxDays)
	if !state.LastSyncAt.IsZero() && state.LastSyncAt.After(since) {
		since = state.LastSyncAt
	}

	slog.Info("fetching PRs from GitHub", "owner", owner, "repo", repo, "since", since.Format(time.RFC3339))

	prs, _, err := client.ListPullRequests(ctx, owner, repo, gh.ListPRsOptions{
		State: "all",
		Since: since,
	})
	if err != nil {
		return fmt.Errorf("list PRs: %w", err)
	}

	for _, pr := range prs {
		if err := ctx.Err(); err != nil {
			return err
		}

		// determine state: GitHub uses "open"/"closed", we add "merged"
		prState := pr.State
		if pr.MergedAt != nil {
			prState = "merged"
		}

		// detect state transitions (open→closed/merged) to trigger full comment re-extract
		prevState, known := state.KnownStates[pr.Number]
		stateChanged := known && prevState != prState

		var labels []string
		for _, l := range pr.Labels {
			labels = append(labels, l.Name)
		}

		// fetch comments when new or state changed.
		// known limitation: comments added without a state change won't be
		// captured until --full re-sync. Acceptable for MVP — next sync cycle
		// with --full will pick them up.
		var comments []ledger.PRComment
		if !known || stateChanged {
			comments = fetchPRComments(ctx, client, owner, repo, pr.Number)
		}

		prFile := &ledger.PRFile{
			Number:      pr.Number,
			Title:       pr.Title,
			Body:        pr.Body,
			Author:      pr.User.Login,
			State:       prState,
			Labels:      labels,
			CreatedAt:   pr.CreatedAt,
			MergedAt:    pr.MergedAt,
			UpdatedAt:   pr.UpdatedAt,
			MergeCommit: pr.MergeSHA,
			URL:         pr.HTMLURL,
			Comments:    comments,
		}

		if !known {
			result.prCreated++
		} else {
			result.prUpdated++
		}

		if err := ledger.WriteGitHubPR(ledgerPath, prFile); err != nil {
			return fmt.Errorf("write PR %d: %w", pr.Number, err)
		}

		state.KnownStates[pr.Number] = prState
		result.prTotal++
	}

	// persist updated state
	state.LastSyncAt = time.Now()
	state.Count += result.prCreated
	if err := ledger.WriteGitHubTypeSyncState(ledgerPath, "pr", state); err != nil {
		return fmt.Errorf("write pr sync state: %w", err)
	}

	return nil
}

func fetchPRComments(ctx context.Context, client *gh.Client, owner, repo string, number int) []ledger.PRComment {
	var comments []ledger.PRComment

	reviewComments, err := client.ListPRComments(ctx, owner, repo, number)
	if err != nil {
		slog.Warn("fetch review comments failed", "pr", number, "error", err)
	} else {
		for _, c := range reviewComments {
			comments = append(comments, ledger.PRComment{
				Author:    c.User.Login,
				Body:      c.Body,
				Path:      c.Path,
				Line:      c.Line,
				CreatedAt: c.CreatedAt,
			})
		}
	}

	issueComments, err := client.ListIssueComments(ctx, owner, repo, number)
	if err != nil {
		slog.Warn("fetch issue comments failed", "pr", number, "error", err)
	} else {
		for _, c := range issueComments {
			comments = append(comments, ledger.PRComment{
				Author:    c.User.Login,
				Body:      c.Body,
				CreatedAt: c.CreatedAt,
			})
		}
	}

	return comments
}

func syncIssuesToLedger(ctx context.Context, client *gh.Client, ledgerPath, owner, repo string, maxDays int, result *githubSyncResult) error {
	state, err := ledger.ReadGitHubTypeSyncState(ledgerPath, "issue")
	if err != nil {
		return fmt.Errorf("read issue sync state: %w", err)
	}

	since := time.Now().AddDate(0, 0, -maxDays)
	if !state.LastSyncAt.IsZero() && state.LastSyncAt.After(since) {
		since = state.LastSyncAt
	}

	slog.Info("fetching issues from GitHub", "owner", owner, "repo", repo, "since", since.Format(time.RFC3339))

	issues, _, err := client.ListIssues(ctx, owner, repo, gh.ListIssuesOptions{
		State: "all",
		Since: since,
	})
	if err != nil {
		return fmt.Errorf("list issues: %w", err)
	}

	for _, issue := range issues {
		if err := ctx.Err(); err != nil {
			return err
		}

		// detect state transitions (open→closed) to trigger full comment re-extract
		prevState, known := state.KnownStates[issue.Number]
		stateChanged := known && prevState != issue.State

		var labels []string
		for _, l := range issue.Labels {
			labels = append(labels, l.Name)
		}

		// fetch comments when new or state changed
		// on state change (open→closed), re-extract all comments
		// to capture any final discussion added at close time
		var comments []ledger.IssueComment
		if !known || stateChanged {
			issueComments, cErr := client.ListIssueComments(ctx, owner, repo, issue.Number)
			if cErr != nil {
				slog.Warn("fetch issue comments failed", "issue", issue.Number, "error", cErr)
			} else {
				for _, c := range issueComments {
					comments = append(comments, ledger.IssueComment{
						Author:    c.User.Login,
						Body:      c.Body,
						CreatedAt: c.CreatedAt,
					})
				}
			}
		}

		issueFile := &ledger.IssueFile{
			Number:    issue.Number,
			Title:     issue.Title,
			Body:      issue.Body,
			Author:    issue.User.Login,
			State:     issue.State,
			Labels:    labels,
			CreatedAt: issue.CreatedAt,
			ClosedAt:  issue.ClosedAt,
			UpdatedAt: issue.UpdatedAt,
			URL:       issue.HTMLURL,
			Comments:  comments,
		}

		if !known {
			result.issueCreated++
		} else {
			result.issueUpdated++
		}

		if err := ledger.WriteGitHubIssue(ledgerPath, issueFile); err != nil {
			return fmt.Errorf("write issue %d: %w", issue.Number, err)
		}

		state.KnownStates[issue.Number] = issue.State
		result.issueTotal++
	}

	// persist updated state
	state.LastSyncAt = time.Now()
	state.Count += result.issueCreated
	if err := ledger.WriteGitHubTypeSyncState(ledgerPath, "issue", state); err != nil {
		return fmt.Errorf("write issue sync state: %w", err)
	}

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
// When neither prsOnly nor issuesOnly is set, wipes everything.
// When one is set, clears only that type's state file.
func resetGitHubSyncState(ledgerPath string, prsOnly, issuesOnly bool) error {
	if !prsOnly && !issuesOnly {
		// wipe everything
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

// commitAndPushGitHubData stages data/github/ directory, commits, and pushes to the ledger.
func commitAndPushGitHubData(ledgerPath, owner, repo string, result *githubSyncResult) error {
	dataDir := ledger.GitHubDataDir(ledgerPath)

	// stage all files in data/github/ with --sparse
	addCmd := exec.Command("git", "-C", ledgerPath, "add", "--sparse", dataDir)
	if output, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %s: %w", string(output), err)
	}

	// commit
	var parts []string
	if result.prTotal > 0 {
		parts = append(parts, fmt.Sprintf("%d PRs", result.prTotal))
	}
	if result.issueTotal > 0 {
		parts = append(parts, fmt.Sprintf("%d issues", result.issueTotal))
	}
	commitMsg := fmt.Sprintf("github: sync %s from %s/%s", strings.Join(parts, ", "), owner, repo)
	commitCmd := exec.Command("git", "-C", ledgerPath, "commit", "--no-verify", "-m", commitMsg)
	if output, err := commitCmd.CombinedOutput(); err != nil {
		if strings.Contains(string(output), "nothing to commit") {
			return nil
		}
		return fmt.Errorf("git commit failed: %s: %w", string(output), err)
	}

	// push with retry
	return pushLedger(context.Background(), ledgerPath)
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
