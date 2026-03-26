package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/search"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/repotools"
	"github.com/sageox/ox/internal/status"
	"github.com/spf13/cobra"
)

// resolveCodeDBDir returns the shared CodeDB directory for the given repo root.
// Uses project config to resolve via ledger cache; falls back to legacy path.
func resolveCodeDBDir(root string) string {
	ctx, err := config.LoadProjectContext(root)
	if err == nil {
		if dir := paths.CodeDBSharedDir(ctx.RepoID(), ctx.Endpoint()); dir != "" {
			return dir
		}
	}
	return paths.CodeDBDataDir(root)
}

// isCodeDBIndexing checks whether the daemon is actively indexing.
// Bleve's BoltDB backend holds an exclusive file lock during writes,
// so codedb.Open from the CLI would block until indexing finishes.
//
// Exposed as a variable so tests can override it.
var isCodeDBIndexing = func() bool {
	client := daemon.NewClientWithTimeout(500 * time.Millisecond)
	cs, err := client.CodeStatus()
	return err == nil && cs.IndexingNow
}

var codeCmd = &cobra.Command{
	Use:   "code",
	Short: "Search code in this repo",
	Long:  "Search git history and current code of this repo using queries.",
}

// codeIndexCmd is an alias for 'ox index code' — kept for back-compat and discoverability
var codeIndexCmd = &cobra.Command{
	Use:   "index [url]",
	Short: "Index a git repository (alias for 'ox index code')",
	Args:  cobra.MaximumNArgs(1),
	RunE:  indexCodeCmd.RunE,
}

var codeSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search indexed code using queries",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := repotools.FindRepoRoot(repotools.VCSGit)
		if err != nil {
			return fmt.Errorf("not in a git repository")
		}

		query := strings.Join(args, " ")
		dataDir := resolveCodeDBDir(root)

		if isCodeDBIndexing() {
			return fmt.Errorf("code index is currently being built — search is unavailable until indexing completes. Run 'ox code status' to check progress")
		}

		db, err := codedb.Open(dataDir)
		if err != nil {
			return fmt.Errorf("open codedb: %w", err)
		}
		defer db.Close()

		// attach daemon-built dirty overlay for uncommitted file search
		if err := db.AttachDirtyIndex(root); err != nil {
			slog.Debug("dirty overlay not available, searching committed content only", "err", err)
		}

		results, err := db.Search(context.Background(), query)
		if err != nil {
			return fmt.Errorf("search: %w", err)
		}

		fullJSON, _ := cmd.Flags().GetBool("full-json")
		limit, _ := cmd.Flags().GetInt("limit")

		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")

		if fullJSON {
			resp := &combinedQueryResponse{CodeResults: results}
			if err := enc.Encode(resp); err != nil {
				return fmt.Errorf("encode: %w", err)
			}
		} else {
			compact := compactSearchResults(results, limit)
			if err := enc.Encode(compact); err != nil {
				return fmt.Errorf("encode: %w", err)
			}
		}

		outputBytes := buf.Len()
		if _, err := buf.WriteTo(os.Stdout); err != nil {
			return err
		}

		agentID, _ := detectAgentContext()
		if agentID != "" {
			slog.Debug("code search context cost", "agent_id", agentID, "bytes", outputBytes)
			trackContextBytes(int64(outputBytes))
		}
		return nil
	},
}

// compactSearchResult is a minimal search result optimized for agent context.
type compactSearchResult struct {
	File        string `json:"file,omitempty"`
	Line        int    `json:"line,omitempty"`
	Lang        string `json:"lang,omitempty"`
	Snippet     string `json:"snippet"`
	Symbol      string `json:"symbol,omitempty"`
	CommentKind string `json:"comment_kind,omitempty"`

	// PR/issue fields
	Number int    `json:"number,omitempty"`
	Title  string `json:"title,omitempty"`
	State  string `json:"state,omitempty"`
	URL    string `json:"url,omitempty"`
}

// compactSearchResponse is the default search output — minimal context footprint.
type compactSearchResponse struct {
	Results  []compactSearchResult `json:"results"`
	Total    int                   `json:"total"`
	Guidance string                `json:"guidance,omitempty"`
}

// compactSearchResults converts full results into a compact format for agents.
func compactSearchResults(results []search.Result, limit int) compactSearchResponse {
	total := len(results)
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	compact := make([]compactSearchResult, 0, len(results))
	for _, r := range results {
		snippet := stripANSIEscapes(r.Content)
		snippet = compactSnippet(snippet, 120)
		cr := compactSearchResult{
			File:        r.FilePath,
			Line:        r.Line,
			Lang:        r.Language,
			Snippet:     snippet,
			Symbol:      r.SymbolName,
			CommentKind: r.CommentKind,
			Number:      r.Number,
			Title:       r.Title,
			State:       r.State,
			URL:         r.URL,
		}
		compact = append(compact, cr)
	}

	resp := compactSearchResponse{
		Results: compact,
		Total:   total,
	}

	if total > len(compact) {
		resp.Guidance = fmt.Sprintf("Showing %d of %d results. Use --limit N for more, or --full-json for complete output.", len(compact), total)
	}

	return resp
}

// compactSnippet collapses whitespace and truncates to maxLen chars.
func compactSnippet(s string, maxLen int) string {
	// collapse newlines and tabs into single spaces
	var b strings.Builder
	prevSpace := false
	for _, r := range strings.TrimSpace(s) {
		if r == '\n' || r == '\r' || r == '\t' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		prevSpace = r == ' '
		b.WriteRune(r)
	}
	result := b.String()
	// strip leading ellipsis from bleve fragments
	result = strings.TrimPrefix(result, "…")
	result = strings.TrimSpace(result)
	if len(result) > maxLen {
		result = result[:maxLen] + "…"
	}
	return result
}

// stripANSIEscapes removes ANSI escape sequences from a string.
func stripANSIEscapes(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		if r == '\033' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// codeQueryCmd is a hidden alias for codeSearchCmd — agents try "query" as a search verb
var codeQueryCmd = &cobra.Command{
	Use:    "query <query>",
	Short:  codeSearchCmd.Short,
	Hidden: true,
	Args:   cobra.MinimumNArgs(1),
	RunE:   codeSearchCmd.RunE,
}

// codeStatsAliasCmd is a hidden alias for codeStatusCmd — back-compat for "ox code stats"
var codeStatsAliasCmd = &cobra.Command{
	Use:    "stats",
	Hidden: true,
	Short:  codeStatusCmd.Short,
	RunE:   codeStatusCmd.RunE,
}

var codeSQLCmd = &cobra.Command{
	Use:    "sql <query>",
	Short:  "Execute raw SQL against the CodeDB database",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := repotools.FindRepoRoot(repotools.VCSGit)
		if err != nil {
			return fmt.Errorf("not in a git repository")
		}

		dataDir := resolveCodeDBDir(root)

		if isCodeDBIndexing() {
			return fmt.Errorf("code index is currently being built — SQL queries unavailable until indexing completes")
		}

		db, err := codedb.Open(dataDir)
		if err != nil {
			return fmt.Errorf("open codedb: %w", err)
		}
		defer db.Close()

		cols, rows, err := db.RawSQL(args[0])
		if err != nil {
			return err
		}

		// Print as TSV
		fmt.Println(strings.Join(cols, "\t"))
		for _, row := range rows {
			fmt.Println(strings.Join(row, "\t"))
		}
		return nil
	},
}

var codeStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show code index status",
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := repotools.FindRepoRoot(repotools.VCSGit)
		if err != nil {
			return fmt.Errorf("not in a git repository")
		}

		dataDir := resolveCodeDBDir(root)
		indexExists := false
		if _, err := os.Stat(dataDir); err == nil {
			indexExists = true
		}

		// get daemon stats for freshness and next-check info
		var codeStats *daemon.CodeDBStats
		var syncInterval time.Duration
		client := daemon.NewClientWithTimeout(500 * time.Millisecond)
		if cs, err := client.CodeStatus(); err == nil {
			codeStats = cs
		}
		if ds, err := client.Status(); err == nil {
			syncInterval = ds.SyncIntervalRead
		}

		// query DB directly for counts (daemon stats may lag).
		// Skip the direct open when the daemon reports indexing in progress —
		// Bleve's BoltDB backend holds an exclusive file lock during writes,
		// so codedb.Open would block until indexing finishes.
		var totalCommits, totalBlobs, totalSymbols, totalComments, totalPRs, totalIssues int
		type repoRow struct {
			name       string
			path       string
			commits    int
			blobs      int
			lastCommit int64 // unix timestamp of most recent commit
		}
		var repos []repoRow

		daemonIndexing := codeStats != nil && codeStats.IndexingNow
		if indexExists && !daemonIndexing {
			db, err := codedb.Open(dataDir)
			if err == nil {
				_ = db.Store().QueryRow("SELECT COUNT(*) FROM commits").Scan(&totalCommits)
				_ = db.Store().QueryRow("SELECT COUNT(*) FROM blobs").Scan(&totalBlobs)
				_ = db.Store().QueryRow("SELECT COUNT(*) FROM symbols").Scan(&totalSymbols)
				_ = db.Store().QueryRow("SELECT COUNT(*) FROM comments").Scan(&totalComments)
				_ = db.Store().QueryRow("SELECT COUNT(*) FROM pull_requests").Scan(&totalPRs)
				_ = db.Store().QueryRow("SELECT COUNT(*) FROM issues").Scan(&totalIssues)

				rows, qErr := db.Store().Query(`
					SELECT r.name, r.path,
					       COUNT(DISTINCT c.id),
					       COUNT(DISTINCT fr.blob_id),
					       COALESCE(MAX(c.timestamp), 0)
					FROM repos r
					LEFT JOIN commits c ON c.repo_id = r.id
					LEFT JOIN file_revs fr ON fr.commit_id = c.id
					GROUP BY r.id ORDER BY r.name`)
				if qErr == nil {
					for rows.Next() {
						var r repoRow
						if rows.Scan(&r.name, &r.path, &r.commits, &r.blobs, &r.lastCommit) == nil {
							repos = append(repos, r)
						}
					}
					rows.Close()
				}
				db.Close()
			}
		} else if daemonIndexing {
			// use daemon's cached stats from the last completed index run
			totalCommits = codeStats.Commits
			totalBlobs = codeStats.Blobs
			totalSymbols = codeStats.Symbols
			totalComments = codeStats.Comments
			totalPRs = codeStats.PRs
			totalIssues = codeStats.Issues
			for _, r := range codeStats.Repos {
				repos = append(repos, repoRow{name: r.Name, path: r.Path, commits: r.Commits, blobs: r.Blobs})
			}
		}

		raw, _ := cmd.Flags().GetBool("json")
		if raw {
			type jsonRepoStats struct {
				Name    string `json:"name"`
				Path    string `json:"path"`
				Commits int    `json:"commits"`
				Blobs   int    `json:"blobs"`
			}
			type jsonStats struct {
				Commits     int             `json:"commits"`
				Blobs       int             `json:"blobs"`
				Symbols     int             `json:"symbols"`
				Comments    int             `json:"comments"`
				PRs         int             `json:"prs"`
				Issues      int             `json:"issues"`
				Repos       []jsonRepoStats `json:"repos"`
				DataDir     string          `json:"data_dir"`
				IndexExists bool            `json:"index_exists"`
				IndexingNow bool            `json:"indexing_now"`
				LastIndexed *time.Time      `json:"last_indexed,omitempty"`
				LastError   string          `json:"last_error,omitempty"`
			}
			out := jsonStats{
				Commits:     totalCommits,
				Blobs:       totalBlobs,
				Symbols:     totalSymbols,
				Comments:    totalComments,
				PRs:         totalPRs,
				Issues:      totalIssues,
				DataDir:     dataDir,
				IndexExists: indexExists,
			}
			if codeStats != nil {
				out.IndexingNow = codeStats.IndexingNow
				if !codeStats.LastIndexed.IsZero() {
					t := codeStats.LastIndexed
					out.LastIndexed = &t
				}
				out.LastError = codeStats.LastError
			}
			for _, r := range repos {
				out.Repos = append(out.Repos, jsonRepoStats{
					Name: r.name, Path: r.path,
					Commits: r.commits, Blobs: r.blobs,
				})
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		// detect GitHub remote for repo identity
		ghOwner, ghRepo, _ := detectGitHubRemote()

		// human-readable output — Tufte-inspired, matching ox status
		var b strings.Builder

		b.WriteString(statusHeaderStyle.Render("Code Index"))
		b.WriteString("\n")
		b.WriteString(statusMutedStyle.Render("──────────"))
		b.WriteString("\n")

		// status line — health signal first
		b.WriteString(statusLabelStyle.Render("Status"))
		switch {
		case !indexExists && (codeStats == nil || !codeStats.IndexingNow):
			b.WriteString(statusWarningStyle.Render("⚠ not indexed"))
			b.WriteString("\n")
			b.WriteString(statusLabelStyle.Render(""))
			b.WriteString(statusMutedStyle.Render("Run "))
			b.WriteString(statusHighlightStyle.Render("ox code index"))
			b.WriteString(statusMutedStyle.Render(" to create one"))
			b.WriteString("\n")
			fmt.Print(b.String())
			return nil
		case codeStats != nil && codeStats.IndexingNow:
			b.WriteString(statusWarningStyle.Render("◐ indexing…"))
		case codeStats != nil && codeStats.LastError != "" && totalCommits == 0:
			b.WriteString(statusWarningStyle.Render("⚠ pending"))
		case codeStats != nil && codeStats.LastError != "":
			b.WriteString(statusErrorStyle.Render("✗ error"))
			b.WriteString("\n")
			b.WriteString(statusLabelStyle.Render(""))
			b.WriteString(statusMutedStyle.Render(codeStats.LastError))
		case codeStats != nil && !codeStats.LastIndexed.IsZero():
			b.WriteString(statusSuccessStyle.Render("✓ indexed (" + status.FormatTimeAgo(codeStats.LastIndexed) + ")"))
		default:
			b.WriteString(statusSuccessStyle.Render("✓ indexed"))
		}
		b.WriteString("\n")

		// repo identity — show GitHub remote name if detected
		if ghOwner != "" && ghRepo != "" {
			b.WriteString(statusLabelStyle.Render("Repository"))
			b.WriteString(statusHighlightStyle.Render(ghOwner + "/" + ghRepo))
			b.WriteString("\n")
		}

		b.WriteString("\n")

		// git history section
		b.WriteString(statusHeaderStyle.Render("Git History"))
		b.WriteString("\n")

		if totalCommits > 0 || totalBlobs > 0 {
			b.WriteString(statusLabelStyle.Render("Commits"))
			b.WriteString(statusValueStyle.Render(formatComma(totalCommits)))
			b.WriteString("\n")
			b.WriteString(statusLabelStyle.Render("Blobs"))
			b.WriteString(statusValueStyle.Render(formatComma(totalBlobs)))
			b.WriteString("\n")
			b.WriteString(statusLabelStyle.Render("Symbols"))
			if totalSymbols > 0 {
				b.WriteString(statusHighlightStyle.Render(formatComma(totalSymbols)))
			} else {
				b.WriteString(statusWarningStyle.Render("0"))
			}
			b.WriteString("\n")
			b.WriteString(statusLabelStyle.Render("Comments"))
			if totalComments > 0 {
				b.WriteString(statusHighlightStyle.Render(formatComma(totalComments)))
			} else {
				b.WriteString(statusWarningStyle.Render("0"))
			}
			b.WriteString("\n")
		} else {
			b.WriteString(statusLabelStyle.Render(""))
			b.WriteString(statusMutedStyle.Render("no git history indexed"))
			b.WriteString("\n")
		}

		// local worktrees — only show when multiple repos indexed
		if len(repos) > 1 {
			b.WriteString(statusLabelStyle.Render("Worktrees"))
			b.WriteString(statusValueStyle.Render(fmt.Sprintf("%d", len(repos))))
			b.WriteString("\n")

			// identify primary worktree (most commits = full history)
			primaryIdx := 0
			for i, r := range repos {
				if r.commits > repos[primaryIdx].commits {
					primaryIdx = i
				}
			}
			primaryCommits := repos[primaryIdx].commits
			primaryName := repos[primaryIdx].name

			// sort: primary first, then alphabetical
			sort.Slice(repos, func(i, j int) bool {
				iPrimary := repos[i].name == primaryName
				jPrimary := repos[j].name == primaryName
				if iPrimary != jPrimary {
					return iPrimary
				}
				return repos[i].name < repos[j].name
			})
			// primary is always index 0 after sort
			primaryIdx = 0

			// compute max name width for column alignment (including " (primary)" suffix)
			maxNameLen := 0
			for i, r := range repos {
				nameLen := len(r.name)
				if i == primaryIdx {
					nameLen += len(" (primary)")
				}
				if nameLen > maxNameLen {
					maxNameLen = nameLen
				}
			}

			// pre-compute commit strings for column alignment
			type worktreeLine struct {
				commitStr string
			}
			lines := make([]worktreeLine, len(repos))
			maxCommitLen := 0
			for i, r := range repos {
				if i == primaryIdx {
					lines[i].commitStr = formatComma(r.commits) + " commits"
				} else if r.commits > 0 && primaryCommits > 0 {
					lines[i].commitStr = "+" + formatComma(r.commits) + " commits"
				} else {
					lines[i].commitStr = formatComma(r.commits) + " commits"
				}
				if len(lines[i].commitStr) > maxCommitLen {
					maxCommitLen = len(lines[i].commitStr)
				}
			}

			// pre-compute blob strings for column alignment
			maxBlobLen := 0
			blobStrs := make([]string, len(repos))
			for i, r := range repos {
				blobStrs[i] = formatComma(r.blobs) + " blobs"
				if len(blobStrs[i]) > maxBlobLen {
					maxBlobLen = len(blobStrs[i])
				}
			}

			for i, r := range repos {
				connector := "├── "
				if i == len(repos)-1 {
					connector = "└── "
				}
				b.WriteString(statusLabelStyle.Render(""))
				b.WriteString(statusMutedStyle.Render(connector))

				if i == primaryIdx {
					label := r.name + " (primary)"
					padded := label + strings.Repeat(" ", maxNameLen-len(label))
					b.WriteString(statusSuccessStyle.Render(padded))
				} else {
					padded := r.name + strings.Repeat(" ", maxNameLen-len(r.name))
					b.WriteString(statusValueStyle.Render(padded))
				}

				paddedCommits := lines[i].commitStr + strings.Repeat(" ", maxCommitLen-len(lines[i].commitStr))
				paddedBlobs := blobStrs[i] + strings.Repeat(" ", maxBlobLen-len(blobStrs[i]))
				b.WriteString(statusMutedStyle.Render("  " + paddedCommits + ", " + paddedBlobs))

				// show last commit age if available
				if r.lastCommit > 0 {
					age := status.FormatTimeAgo(time.Unix(r.lastCommit, 0))
					b.WriteString(statusMutedStyle.Render("  " + age))
				}
				b.WriteString("\n")
			}
		}

		b.WriteString("\n")

		// GitHub section
		b.WriteString(statusHeaderStyle.Render("GitHub"))
		b.WriteString("\n")

		if totalPRs > 0 || totalIssues > 0 {
			b.WriteString(statusLabelStyle.Render("PRs"))
			b.WriteString(statusHighlightStyle.Render(formatComma(totalPRs)))
			b.WriteString("\n")
			b.WriteString(statusLabelStyle.Render("Issues"))
			b.WriteString(statusHighlightStyle.Render(formatComma(totalIssues)))
			b.WriteString("\n")
		} else if ghOwner != "" {
			b.WriteString(statusLabelStyle.Render(""))
			b.WriteString(statusMutedStyle.Render("not yet indexed — run "))
			b.WriteString(statusHighlightStyle.Render("ox index github"))
			b.WriteString(statusMutedStyle.Render(" or wait for daemon"))
			b.WriteString("\n")
		} else {
			b.WriteString(statusLabelStyle.Render(""))
			b.WriteString(statusMutedStyle.Render("no GitHub remote detected"))
			b.WriteString("\n")
		}

		b.WriteString("\n")

		// next check — only when daemon is running and index exists
		if codeStats != nil && !codeStats.IndexingNow && indexExists && syncInterval > 0 && !codeStats.LastIndexed.IsZero() {
			nextCheck := codeStats.LastIndexed.Add(syncInterval)
			remaining := time.Until(nextCheck)
			b.WriteString(statusLabelStyle.Render("Next check"))
			if remaining <= 0 {
				b.WriteString(statusMutedStyle.Render("due now"))
			} else {
				b.WriteString(statusMutedStyle.Render("in " + formatDurationBrief(remaining)))
			}
			b.WriteString("\n")
		}

		fmt.Print(b.String())
		return nil
	},
}

// formatDurationBrief formats a duration as a compact human string (e.g., "4m 30s").
func formatDurationBrief(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Second {
		return "<1s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm %ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

// formatComma formats an integer with comma separators (e.g., 12847 → "12,847").
func formatComma(n int) string {
	if n < 0 {
		return "-" + formatComma(-n)
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var result strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result.WriteByte(',')
		}
		result.WriteRune(c)
	}
	return result.String()
}

// formatIndexTiming formats per-stage timing from a CodeIndexResult.
func formatIndexTiming(r *daemon.CodeIndexResult) string {
	total := time.Duration(r.TotalDurationMs) * time.Millisecond
	idx := time.Duration(r.IndexDurationMs) * time.Millisecond
	sym := time.Duration(r.SymbolDurationMs) * time.Millisecond
	cmt := time.Duration(r.CommentDurationMs) * time.Millisecond
	return fmt.Sprintf("total %s: index %s, symbols %s, comments %s",
		formatDurationBrief(total), formatDurationBrief(idx),
		formatDurationBrief(sym), formatDurationBrief(cmt))
}

func init() {
	codeSearchCmd.Flags().Bool("full-json", false, "full uncompacted JSON output (~6x more context tokens)")
	codeSearchCmd.Flags().Int("limit", 10, "max results to return")

	codeQueryCmd.Flags().Bool("full-json", false, "full uncompacted JSON output (~6x more context tokens)")
	codeQueryCmd.Flags().Int("limit", 10, "max results to return")

	// mirror indexCodeCmd flags so the alias works correctly
	codeIndexCmd.Flags().Bool("full", false, "wipe index and rebuild from scratch")

	codeStatusCmd.Flags().Bool("json", false, "output as JSON")
	codeStatsAliasCmd.Flags().Bool("json", false, "output as JSON")

	codeCmd.AddCommand(codeIndexCmd)
	codeCmd.AddCommand(codeSearchCmd)
	codeCmd.AddCommand(codeQueryCmd)
	codeCmd.AddCommand(codeSQLCmd)
	codeCmd.AddCommand(codeStatusCmd)
	codeCmd.AddCommand(codeStatsAliasCmd)
	codeCmd.AddCommand(codeInsightsCmd)
	codeCmd.GroupID = "dev"
	rootCmd.AddCommand(codeCmd)
}
