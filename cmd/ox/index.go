package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/index"
	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	gh "github.com/sageox/ox/internal/github"
	"github.com/sageox/ox/internal/githubmirror"
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

		// try daemon-based indexing first (preferred: handles concurrency, fsnotify)
		if err := daemon.EnsureDaemon(); err == nil {
			payload := daemon.CodeIndexPayload{Full: full}
			if len(args) > 0 {
				payload.URL = args[0]
				fmt.Fprintf(cmd.ErrOrStderr(), "Indexing %s...\n", args[0])
			} else if full {
				fmt.Fprintf(cmd.ErrOrStderr(), "Full reindex of local repo...\n")
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "Indexing local repo...\n")
			}

			client := daemon.NewClientForCurrentRepoWithTimeout(5 * time.Minute)
			result, err := client.CodeIndex(payload, func(stage string, percent *int, message string) {
				if message != "" {
					fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", message)
				}
			})
			if err == nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Done. Parsed %d blobs, %d symbols\n",
					result.BlobsParsed, result.SymbolsExtracted)
				return nil
			}
			slog.Debug("daemon indexing failed, falling back to in-process", "error", err)
		}

		// fallback: in-process indexing (no daemon needed)
		return indexCodeInProcess(cmd, args, full)
	},
}

// indexCodeInProcess runs code indexing directly in the CLI process when the
// daemon is unavailable.
func indexCodeInProcess(cmd *cobra.Command, args []string, full bool) error {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return fmt.Errorf("not in a git repository")
	}

	dataDir := resolveCodeDBDir(gitRoot)
	ctx := cmd.Context()
	opts := index.IndexOptions{}

	// build performs the COMPLETE codedb build against db: git index + symbol and
	// comment parse. Both the atomic from-scratch path and the incremental
	// self-heal path run this exact closure, so they stay behaviorally identical.
	var symbolsExtracted uint64
	build := func(ctx context.Context, db *codedb.DB) error {
		if len(args) > 0 {
			if err := db.IndexRepo(ctx, args[0], opts); err != nil {
				return fmt.Errorf("index: %w", err)
			}
		} else {
			if err := db.IndexLocalRepo(ctx, gitRoot, opts); err != nil {
				return fmt.Errorf("index local: %w", err)
			}
		}
		stats, err := db.ParseSymbols(ctx, nil)
		if err != nil {
			// Cache corruption must abort the build so the recovery paths can
			// react (OpenIndexWithHeal discards + retries; BuildCodeDBAtomic
			// refuses to swap a partial index into place). Ordinary parse
			// failures stay non-fatal.
			if index.IsCorruptionError(err) {
				return fmt.Errorf("parse symbols: %w", err)
			}
			slog.Warn("symbol parsing failed (non-fatal)", "error", err)
		}
		symbolsExtracted = stats.SymbolsExtracted
		if _, err := db.ParseComments(ctx, nil); err != nil {
			if index.IsCorruptionError(err) {
				return fmt.Errorf("parse comments: %w", err)
			}
			slog.Warn("comment parsing failed (non-fatal)", "error", err)
		}
		return nil
	}

	var runErr error
	if full {
		// Prevent tier: build into a sibling temp dir and atomically swap it into
		// place, so a kill mid-build never leaves a half-written cache that would
		// crash-loop every subsequent run.
		fmt.Fprintf(cmd.ErrOrStderr(), "Full reindex (in-process)...\n")
		runErr = codedb.BuildCodeDBAtomic(ctx, dataDir, build)
	} else {
		// Recover tier: index in place. OpenIndexWithHeal escalates to a
		// from-scratch rebuild when Open self-healed a corrupt sub-index (marker
		// present), and discards + retries once if the pass itself fails on a
		// corrupt cache — so a half-written cache neither crash-loops the caller
		// nor silently leaves search empty.
		fmt.Fprintf(cmd.ErrOrStderr(), "Indexing local repo (in-process)...\n")
		var db *codedb.DB
		db, runErr = codedb.OpenIndexWithHeal(ctx, dataDir, build)
		if db != nil {
			defer db.Close()
		}
	}

	if runErr != nil {
		if errors.Is(runErr, index.ErrAlternatesUnsupported) {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"codedb: skipping local index — repo uses git alternates (go-git v6 limitation).\n"+
					"  Remediation: `git repack -a -d --local` to copy objects locally, or\n"+
					"  re-clone without --shared / --reference. See `ox doctor` (git-alternates check).\n")
			return nil
		}
		return fmt.Errorf("index (in-process): %w", runErr)
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "Done (in-process). %d symbols extracted\n", symbolsExtracted)
	return nil
}

var indexGitHubCmd = &cobra.Command{
	Use:   "github",
	Short: "Index GitHub PRs and issues",
	Long: `Fetch recent pull requests and issues from GitHub and make them searchable.

Recent data is published to the team bulletin and indexed into CodeDB for search
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

var runManualGitHubMirror = daemon.RunProjectGitHubMirror

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

	codeDBDir := githubFallbackCodeDBDir(gitRoot)
	target := daemon.MirrorTarget{RepoID: config.GetRepoID(gitRoot), LedgerPath: ledgerPath, Owner: owner, Repo: repo, GitHubToken: token, PullRequests: syncPRs, Issues: syncIssues}
	opts := daemon.ManualMirrorOptions{Days: maxDays, Full: full, NoPush: noPush}
	if noPush {
		// Build the normal code index first, so a GitHub-only database does
		// not hide an existing Ledger index on a fresh project.
		sharedDir := resolveCodeDBDir(gitRoot)
		if _, err := os.Stat(filepath.Join(sharedDir, store.MetadataDBFile)); errors.Is(err, os.ErrNotExist) {
			if err := indexCodeInProcess(cmd, nil, false); err != nil {
				return fmt.Errorf("build code index: %w", err)
			}
			codeDBDir = githubFallbackCodeDBDir(gitRoot)
			if _, err := os.Stat(filepath.Join(sharedDir, store.MetadataDBFile)); err != nil {
				return fmt.Errorf("code search index was not built: %w", err)
			}
		}
		if codeDBDir == "" {
			return errors.New("code search index is not available")
		}
		opts.KnownLocal = func(ctx context.Context) (map[string]githubmirror.ItemState, error) {
			db, err := codedb.OpenSQLOnly(codeDBDir)
			if err != nil {
				return nil, err
			}
			defer db.Close()
			return index.KnownLocalGitHubItems(ctx, db.Store(), owner, repo)
		}
		opts.IndexLocal = func(ctx context.Context, items []githubmirror.Item) (int, error) {
			db, err := codedb.OpenSQLOnly(codeDBDir)
			if err != nil {
				return 0, err
			}
			defer db.Close()
			return index.IndexLocalGitHubItems(ctx, db.Store(), items)
		}
	}
	result, err := runManualGitHubMirror(cmd.Context(), gitRoot, target, opts)
	if err != nil {
		return fmt.Errorf("index GitHub: %w", err)
	}
	if noPush {
		fmt.Printf("Indexed %d GitHub items locally; %d already current. Nothing was published.\n", result.Indexed, result.Unchanged)
		return nil
	}
	fmt.Printf("GitHub bulletin: %d published, %d current, %d unchanged, %d expired.\n", result.Accepted, result.Current, result.Unchanged, result.Expired)
	// Publication and Team Context synchronization are separate operations.
	if err := daemon.EnsureDaemon(); err != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), "Published; search indexing is pending.")
		return nil
	}
	client := daemon.NewClientForCurrentRepoWithTimeout(5 * time.Minute)
	results, syncErr := client.TeamSyncWithProgress(nil)
	team := config.FindRepoTeamContext(gitRoot)
	synced := false
	for _, r := range results {
		if team != nil && r.TeamID == team.TeamID && r.Status == "synced" {
			synced = true
		}
	}
	if err := indexGitHubViaDaemon(); err != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), "Published; search indexing is pending.")
		return nil
	}
	if syncErr != nil || !synced {
		fmt.Fprintln(cmd.ErrOrStderr(), "Published; search refresh requested through repository access.")
	} else {
		fmt.Fprintln(cmd.ErrOrStderr(), "Team Context synced; search refresh requested.")
	}
	return nil

}

// githubFallbackCodeDBDir returns where the in-process fallback may index
// GitHub data: the shared CodeDB, where the daemon indexes it. Until that index
// is built, search reads the Ledger index instead, and a shared index holding
// only GitHub data would take its place and hide code results — so it returns
// "" then, leaving the build to `ox index code`.
func githubFallbackCodeDBDir(gitRoot string) string {
	if _, useLedger := resolvePreferredCodeDBDir(gitRoot); useLedger {
		return ""
	}
	return resolveCodeDBDir(gitRoot)
}

// githubIndexRun is one resolved `ox index github` run: everything the command
// derives from flags, config, remotes and credentials.
type githubIndexRun struct {
	Fetcher    ledger.GitHubFetcher
	LedgerPath string
	// CodeDBDir is where the in-process fallback indexes; the daemon resolves
	// its own (the same directory). Empty disables the fallback.
	CodeDBDir  string
	Owner      string
	Repo       string
	MaxDays    int
	SyncPRs    bool
	SyncIssues bool
	NoPush     bool
}

// indexGitHubViaDaemon asks the daemon to run its index pass, which ends with
// the same GitHub-data stage the daemon runs after its own syncs (plus the team
// board's mirrored posts). A variable so tests can take the daemon away.
var indexGitHubViaDaemon = func() error {
	if err := daemon.EnsureDaemon(); err != nil {
		return err
	}
	client := daemon.NewClientForCurrentRepoWithTimeout(5 * time.Minute)
	_, err := client.CodeIndex(daemon.CodeIndexPayload{}, nil)
	return err
}

// indexGitHubCodeDB makes freshly synced PRs and issues searchable, the way the
// daemon does after each of its syncs: through the daemon when one answers,
// otherwise by indexing the Ledger's GitHub data into CodeDB in-process.
func indexGitHubCodeDB(ctx context.Context, run githubIndexRun) error {
	err := indexGitHubViaDaemon()
	if err == nil {
		return nil
	}
	slog.Debug("daemon github indexing failed, falling back to in-process", "error", err)
	if run.CodeDBDir == "" {
		return errors.New("the code search index is not built yet; run `ox index code`, then `ox index github --no-push`")
	}

	db, err := codedb.Open(run.CodeDBDir)
	if err != nil {
		return fmt.Errorf("open codedb: %w", err)
	}
	defer db.Close()
	if db.ReadOnly() {
		return fmt.Errorf("cannot index into %s: read-only", run.CodeDBDir)
	}
	if _, err := db.IndexGitHubData(ctx, run.LedgerPath, nil); err != nil {
		return fmt.Errorf("index github data: %w", err)
	}
	return nil
}

// syncGitHubIndex syncs PRs and issues into the Ledger and pushes them.
func syncGitHubIndex(ctx context.Context, run githubIndexRun) error {
	logger := slog.Default()
	combined := &ledger.SyncResult{}

	if run.SyncPRs {
		prResult, prErr := ledger.SyncPRs(ctx, run.Fetcher, run.LedgerPath, run.Owner, run.Repo, run.MaxDays, logger)
		if prErr != nil {
			return fmt.Errorf("sync PRs: %w", prErr)
		}
		combined.PRTotal = prResult.PRTotal
		combined.PRCreated = prResult.PRCreated
		combined.PRUpdated = prResult.PRUpdated
		combined.PRFailed = prResult.PRFailed
	}

	if run.SyncIssues {
		issueResult, issueErr := ledger.SyncIssues(ctx, run.Fetcher, run.LedgerPath, run.Owner, run.Repo, run.MaxDays, logger)
		if issueErr != nil {
			return fmt.Errorf("sync issues: %w", issueErr)
		}
		combined.IssueTotal = issueResult.IssueTotal
		combined.IssueCreated = issueResult.IssueCreated
		combined.IssueUpdated = issueResult.IssueUpdated
		combined.IssueFailed = issueResult.IssueFailed
	}

	if run.SyncPRs {
		fmt.Printf("Synced %d PRs (%d new, %d updated) from %s/%s\n",
			combined.PRTotal, combined.PRCreated, combined.PRUpdated, run.Owner, run.Repo)
	}
	if run.SyncIssues {
		fmt.Printf("Synced %d issues (%d new, %d updated) from %s/%s\n",
			combined.IssueTotal, combined.IssueCreated, combined.IssueUpdated, run.Owner, run.Repo)
	}

	if failed := combined.PRFailed + combined.IssueFailed; failed > 0 {
		cli.PrintWarning(fmt.Sprintf("%d item(s) skipped because their comments could not be fetched; they are retried on the next sync", failed))
	}

	// Index what is now in the Ledger, pushed or not: --no-push skips the
	// push, not the extract.
	fmt.Fprintln(os.Stderr, "Indexing GitHub data for search...")
	if err := indexGitHubCodeDB(ctx, run); err != nil {
		cli.PrintWarning(fmt.Sprintf("GitHub data synced but not indexed for search: %v", err))
	}

	if run.NoPush {
		return nil
	}

	if err := ledger.CommitAndPushGitHubData(context.Background(), run.LedgerPath, run.Owner, run.Repo, combined, pushLedger); err != nil {
		return fmt.Errorf("push to ledger: %w", err)
	}

	fmt.Println("GitHub data pushed to ledger.")
	return nil
}

// detectGitHubRemote finds the GitHub owner/repo from git remotes.
// offline-safe: returns error for non-GitHub/local-only repos; caller handles gracefully
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

func init() {
	// ox index code flags
	indexCodeCmd.Flags().Bool("full", false, "wipe index and rebuild from scratch")

	// ox index github flags
	indexGitHubCmd.Flags().IntP("days", "d", defaultGitHubSyncMaxDays, "max days of history to fetch")
	indexGitHubCmd.Flags().Bool("no-push", false, "index locally without publishing")
	indexGitHubCmd.Flags().Bool("prs-only", false, "index only pull requests")
	indexGitHubCmd.Flags().Bool("issues-only", false, "index only issues")
	indexGitHubCmd.Flags().Bool("full", false, "re-fetch selected GitHub history")

	// propagate common flags to parent for runIndexAll
	indexCmd.Flags().Bool("full", false, "wipe all indexes and rebuild from scratch")
	indexCmd.Flags().IntP("days", "d", defaultGitHubSyncMaxDays, "max days of GitHub history to fetch")
	indexCmd.Flags().Bool("no-push", false, "index locally without publishing")
	indexCmd.Flags().Bool("prs-only", false, "index only pull requests (GitHub)")
	indexCmd.Flags().Bool("issues-only", false, "index only issues (GitHub)")

	indexCmd.AddCommand(indexCodeCmd)
	indexCmd.AddCommand(indexGitHubCmd)

	// ox index status → delegates to ox code status (read-only, no side effects)
	indexStatusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show index status (alias for 'ox code status')",
		RunE:  codeStatusCmd.RunE,
	}
	indexStatusCmd.Flags().Bool("json", false, "output as JSON")
	indexCmd.AddCommand(indexStatusCmd)

	// hidden alias: ox index stats → ox index status
	indexStatsAlias := &cobra.Command{
		Use:    "stats",
		Hidden: true,
		RunE:   codeStatusCmd.RunE,
	}
	indexStatsAlias.Flags().Bool("json", false, "output as JSON")
	indexCmd.AddCommand(indexStatsAlias)

	indexCmd.GroupID = "dev"
	rootCmd.AddCommand(indexCmd)
}
