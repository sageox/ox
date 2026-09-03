package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/search"
	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/decision"
	"github.com/sageox/ox/internal/observability"
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

// resolveLedgerCodeDBDir returns the ledger CodeDB directory if a built index exists on disk.
// Returns empty string if the ledger index is not available (graceful fallback to legacy).
func resolveLedgerCodeDBDir(root string) string {
	ctx, err := config.LoadProjectContext(root)
	if err == nil {
		if dir := paths.CodeDBLedgerDir(ctx.RepoID(), ctx.Endpoint()); dir != "" {
			if _, statErr := os.Stat(filepath.Join(dir, store.MetadataDBFile)); statErr == nil {
				return dir
			}
		}
	}
	return ""
}

// resolvePreferredCodeDBDir returns the best available CodeDB directory and whether
// it's the ledger index. Prefers the shared CodeDB (project code); falls back to the
// ledger index only if the shared index hasn't been built yet.
func resolvePreferredCodeDBDir(root string) (dataDir string, useLedger bool) {
	dataDir = resolveCodeDBDir(root)
	// check for metadata.db — the shared dir may exist as a parent of the ledger
	// index dir without actually containing an index
	if _, err := os.Stat(filepath.Join(dataDir, store.MetadataDBFile)); err != nil {
		if ledgerDir := resolveLedgerCodeDBDir(root); ledgerDir != "" {
			return ledgerDir, true
		}
	}
	return dataDir, false
}

// isCodeDBIndexing checks whether the daemon is actively indexing the selected backend.
// Bleve's BoltDB backend holds an exclusive file lock during writes,
// so codedb.Open from the CLI would block until indexing finishes.
// useLedger selects which flag to check: ledger index vs worktree.
//
// Exposed as a variable so tests can override it.
var isCodeDBIndexing = func(useLedger bool) bool {
	client := daemon.NewClientForCurrentRepoWithTimeout(500 * time.Millisecond)
	cs, err := client.CodeStatus()
	if err != nil {
		return false
	}
	if useLedger {
		return cs.LedgerIndexingNow
	}
	return cs.IndexingNow
}

var codeCmd = &cobra.Command{
	Use:   "code",
	Short: "Search code, symbols, history, PRs, and comments in this repo",
	Long: `Search the indexed CodeDB for this repo.

CodeDB indexes more than text: symbols, resolved call edges (ADR-019), git
history, diffs, PR/issue bodies and comments, and source comments. Queries
use a Sourcegraph-style DSL with filters that grep cannot match.

Examples:
  ox code search "ResolveSession" type:symbol
  ox code search "" calledby:authenticate
  ox code search "" calls:Handler depth:2
  ox code search "rate limit" type:pr state:open
  ox code search "TODO" type:comment ckind:todo
  ox code prs --sort stalled
  ox code insights

Run 'ox code search --help' for the full DSL grammar and more examples.`,
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
	Short: "Search the CodeDB index using a query DSL",
	Long: `Search the indexed CodeDB for symbols, code, diffs, commits, comments, PRs, and issues.

DSL grammar:
  type:{code,symbol,diff,commit,comment,pr,issue}   what kind of record to match
  repo:<name>[@<rev>]    file:<glob>    lang:<id>
  author:<name>          message:<text>
  before:<date>          after:<date>                ISO 8601 or relative
  calls:<name>           calledby:<name>             resolved call graph (ADR-019)
  returns:<type>         depth:1..10                 transitive call depth
  confidence:{extracted,inferred,ambiguous}          ADR-019 edge confidence
  ckind:<kind>           state:<pr_state>
  select:{repo,file,symbol,symbol.<kind>}
  count:<N>              case:yes
  patterntype:{literal,keyword,regexp}
  OR                                                 boolean across groups
  /pattern/                                          forced regex
  -<filter>                                          negate any filter

Examples:
  # symbol definition (what grep usually misses among text matches)
  ox code search "ResolveSession" type:symbol

  # resolved call graph — who calls authenticate()?
  ox code search "" calledby:authenticate

  # what does Handler call, two hops deep?
  ox code search "" calls:Handler depth:2

  # indexed PR titles + bodies + review comments
  ox code search "rate limit" type:pr state:open

  # source comments tagged TODO
  ox code search "TODO" type:comment ckind:todo

  # git log + content match together
  ox code search "migration" author:rupak after:2026-04-01

  # cross-language regex via /pattern/
  ox code search "/Resolve[A-Z]\w+Recording/"

When the index is unavailable (still building, not yet created), agent-context
invocations emit a structured JSON status instead of erroring — callers should
parse the status field before treating output as results.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCodeSearch(cmd, strings.Join(args, " "))
	},
}

// runCodeSearch is the shared executor used by `ox code search` and the verb
// wrappers (`ox code callers`, `callees`, `defs`, `refs`, `log`). All callers
// build a DSL query string; this function does the index check, dirty-overlay
// attach, search, output formatting, and stats one-liner.
//
// The verb wrappers must inherit the same flags as codeSearchCmd
// (`--limit`, `--snippet`, `--full-json`) so they read identically.
func runCodeSearch(cmd *cobra.Command, query string) error {
	root, err := repotools.FindRepoRoot(repotools.VCSGit)
	if err != nil {
		return fmt.Errorf("not in a git repository")
	}

	dataDir, useLedger := resolvePreferredCodeDBDir(root)
	agentID, _ := detectAgentContext()

	// No index has ever been built: emit the structured not_indexed contract
	// for agents (mirrors code_activity/prs/insights) instead of surfacing a
	// raw codedb.Open error, which trains agents to abandon the tool. Every
	// verb (callers/callees/defs/refs/log) routes through here, so this is the
	// single place that closes the never-indexed gap for all of them.
	if _, statErr := os.Stat(filepath.Join(dataDir, store.MetadataDBFile)); os.IsNotExist(statErr) {
		if agentID != "" {
			return emitIndexNotReadyJSON(cmd, indexStatusNotIndexed,
				"No code index found for this repo.",
				"Run 'ox code index' to build the index, then rerun.")
		}
		return fmt.Errorf("no code index found for this repo — run 'ox code index' first")
	}

	// Index-not-ready paths: emit structured JSON when an agent is calling
	// so it can branch on `status` instead of treating an error as terminal.
	if isCodeDBIndexing(useLedger) {
		if agentID != "" {
			return emitIndexNotReadyJSON(cmd, indexStatusIndexing,
				"Code index is currently being built. Search will be available once indexing completes.",
				"Use Grep/Glob until indexing completes; rerun 'ox code search' afterward.")
		}
		return fmt.Errorf("code index is currently being built — search is unavailable until indexing completes. Run 'ox code status' to check progress")
	}

	db, err := codedb.Open(dataDir)
	if err != nil {
		return fmt.Errorf("open codedb: %w", err)
	}
	defer db.Close()

	dirtyCount := db.AttachAllDirtyIndexes()
	if dirtyCount > 0 {
		slog.Debug("attached dirty overlays", "count", dirtyCount)
	}

	searchStart := time.Now()
	decisionsOnly, _ := cmd.Flags().GetBool("decisions")
	isDecision := decision.PathMatcher(root)
	var results []search.Result
	if decisionsOnly {
		patterns := decision.SearchPathPatterns(root)
		if len(patterns) > 0 {
			results, err = db.SearchRestrictedFiles(context.Background(), query, patterns)
		}
	} else {
		results, err = db.Search(context.Background(), query)
	}
	searchElapsed := time.Since(searchStart)
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}
	if decisionsOnly {
		// Keep the exact corpus predicate as a precision guard for configured
		// doublestar globs; the DB filter above is the pre-limit recall gate.
		filtered := results[:0]
		for _, r := range results {
			if isDecision(r.FilePath) {
				filtered = append(filtered, r)
			}
		}
		results = filtered
	}

	observability.SetResultCount(len(results))

	fullJSON, _ := cmd.Flags().GetBool("full-json")
	limit, _ := cmd.Flags().GetInt("limit")
	snippetLen, _ := cmd.Flags().GetInt("snippet")
	if snippetLen <= 0 {
		snippetLen = defaultSnippetLen
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")

	if fullJSON {
		resp := &combinedQueryResponse{CodeResults: results}
		if err := enc.Encode(resp); err != nil {
			return fmt.Errorf("encode: %w", err)
		}
	} else {
		compact := compactSearchResults(results, limit, snippetLen, isDecision)
		// JIT DSL hint — only when caller issued a bare term, so verb wrappers
		// (which always pass DSL-shaped queries) never re-teach the DSL.
		if isBareQuery(query) && len(compact.Results) > 0 {
			if compact.Guidance != "" {
				compact.Guidance += " "
			}
			compact.Guidance += "Tip: refine with `type:symbol` for defs, `calledby:<name>`/`calls:<name>` for the resolved call graph, `type:pr`/`type:issue` for indexed GitHub records, or `before:`/`after:` to scope by time."
		}
		if err := enc.Encode(compact); err != nil {
			return fmt.Errorf("encode: %w", err)
		}
	}

	quiet, _ := cmd.Flags().GetBool("quiet")
	if !quiet {
		fmt.Fprintf(os.Stderr, "codedb: %d results in %s (dirty overlays: %d)\n",
			len(results), formatSearchLatency(searchElapsed), dirtyCount)
	}

	outputBytes := buf.Len()
	if _, err := buf.WriteTo(os.Stdout); err != nil {
		return err
	}

	if agentID != "" {
		slog.Debug("code search context cost", "agent_id", agentID, "bytes", outputBytes)
		trackContextBytes(int64(outputBytes))
	}
	return nil
}

// defaultSnippetLen bounds the per-result snippet returned in compact mode.
// Bumped from 120 → 200 to better cover typical Go signatures + brief context;
// override via --snippet N for narrower or wider snippets.
const defaultSnippetLen = 200

// indexStatusIndexing / indexStatusNotIndexed are the canonical `status`
// values emitted by emitIndexNotReadyJSON so agent-context consumers can
// branch on a stable string.
const (
	indexStatusIndexing   = "indexing"
	indexStatusNotIndexed = "not_indexed"
)

// indexNotReadyResponse is the structured payload emitted on stdout when an
// agent-context invocation hits an index that is mid-build or missing. Agents
// branch on `status` rather than parsing stderr — this turns a hard error into
// a recoverable signal that includes the recommended fallback.
type indexNotReadyResponse struct {
	Status       string `json:"status"` // "indexing" | "not_indexed"
	Message      string `json:"message"`
	FallbackHint string `json:"fallback_hint"`
}

// emitIndexNotReadyJSON writes a structured status response to stdout and
// returns nil so the command exits 0. Callers should ONLY invoke this when
// they have already confirmed an agent context — humans get a plain error
// instead so the terminal output remains readable.
func emitIndexNotReadyJSON(cmd *cobra.Command, status, message, fallback string) error {
	resp := indexNotReadyResponse{
		Status:       status,
		Message:      message,
		FallbackHint: fallback,
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(resp)
}

// isBareQuery returns true when the query string carries no DSL filters and
// is essentially "one search term." Used to decide whether to append a JIT
// DSL hint to the compact response — agents using `ox code` like a grep get
// a one-line nudge toward the unique-value filters.
func isBareQuery(q string) bool {
	q = strings.TrimSpace(q)
	if q == "" {
		return false
	}
	// any DSL filter syntax (`key:value`) disqualifies — already non-bare
	if strings.Contains(q, ":") {
		return false
	}
	// boolean operators or forced regex also count as non-bare
	if strings.Contains(q, " OR ") || (strings.HasPrefix(q, "/") && strings.HasSuffix(q, "/")) {
		return false
	}
	return true
}

// compactSearchResult is a minimal search result optimized for agent context.
type compactSearchResult struct {
	File        string `json:"file,omitempty"`
	Line        int    `json:"line,omitempty"`
	Lang        string `json:"lang,omitempty"`
	Snippet     string `json:"snippet"`
	Symbol      string `json:"symbol,omitempty"`
	CommentKind string `json:"comment_kind,omitempty"`
	// DocType is "decision" for hits inside the repo's decision-record
	// corpus (docs/adr etc.) — the special tag that lets agents filter or
	// recognize DRs in mixed results.
	DocType string `json:"doc_type,omitempty"`

	// Commit fields — populated for type:commit (e.g. `ox code log <path>`).
	// Empty for code/symbol/comment results.
	CommitHash    string `json:"commit_hash,omitempty"`
	Author        string `json:"author,omitempty"`
	CommitMessage string `json:"commit_message,omitempty"`

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
// snippetLen caps per-result snippet length; pass 0 to use defaultSnippetLen.
// isDecision, when non-nil, tags hits under the repo's decision-record globs.
func compactSearchResults(results []search.Result, limit, snippetLen int, isDecision func(string) bool) compactSearchResponse {
	total := len(results)
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	if snippetLen <= 0 {
		snippetLen = defaultSnippetLen
	}

	compact := make([]compactSearchResult, 0, len(results))
	for _, r := range results {
		snippet := stripANSIEscapes(r.Content)
		snippet = compactSnippet(snippet, snippetLen)
		cr := compactSearchResult{
			File:          r.FilePath,
			Line:          r.Line,
			Lang:          r.Language,
			Snippet:       snippet,
			Symbol:        r.SymbolName,
			CommentKind:   r.CommentKind,
			CommitHash:    r.CommitHash,
			Author:        r.Author,
			CommitMessage: compactSnippet(r.Message, snippetLen),
			Number:        r.Number,
			Title:         r.Title,
			State:         r.State,
			URL:           r.URL,
		}
		if isDecision != nil && isDecision(r.FilePath) {
			cr.DocType = "decision"
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

// compactSnippet collapses whitespace and truncates to maxLen runes.
//
// Truncation is rune-aware (not byte-aware) — slicing a UTF-8 string mid-rune
// produces invalid output and breaks JSON encoding for any non-ASCII content
// (Greek identifiers, emoji in comments, CJK in PR titles, etc.). maxLen is
// counted in runes so the output is bounded by visual width, not bytes.
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

	// Rune-aware truncation: count runes, find the byte offset of the
	// (maxLen+1)-th rune, cut there. Pure ASCII falls through with the same
	// behavior as the original byte slice (1 rune == 1 byte).
	runeCount := 0
	cutAt := -1
	for byteIdx := range result {
		if runeCount == maxLen {
			cutAt = byteIdx
			break
		}
		runeCount++
	}
	if cutAt >= 0 {
		result = result[:cutAt] + "…"
	}
	return result
}

// stripANSIEscapes removes terminal escape sequences and bare control bytes from
// a string so untrusted text (e.g. LLM-generated summaries) can be printed to a
// terminal without injecting cursor moves, clipboard writes, or title changes.
//
// It strips:
//   - CSI sequences: ESC [ … <final byte 0x40-0x7e>
//   - OSC sequences: ESC ] … terminated by BEL (0x07) or ST (ESC \). The OSC 52
//     clipboard-write payload is the motivating attack (security finding #11).
//   - DCS (ESC P), SOS (ESC X), PM (ESC ^), APC (ESC _): … terminated by ST
//   - other two-byte ESC sequences (ESC followed by a single byte)
//   - bare C0 control bytes 0x00-0x1f except tab (0x09) and newline (0x0a)
//   - bare C1 control bytes 0x80-0x9f, which include the 8-bit forms of CSI
//     (0x9b) and OSC (0x9d) that terminals interpret as escape introducers —
//     stripping ESC-prefixed sequences alone would miss them. Matches
//     sanitizeSessionText (session_list.go).
//
// Normal text is unaffected: printable Unicode starts at U+00A0, so only the
// control ranges (never real runes in user content) are dropped.
func stripANSIEscapes(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]

		if r == '\033' { // ESC
			i = skipEscapeSequence(runes, i)
			continue
		}

		// drop bare C0 control bytes except tab and newline
		if r < 0x20 && r != '\t' && r != '\n' {
			continue
		}
		// drop DEL and the C1 control range (0x80-0x9f): 0x9b/0x9d are 8-bit
		// CSI/OSC introducers, so leaving them would reopen the injection hole.
		if r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			continue
		}

		b.WriteRune(r)
	}
	return b.String()
}

// skipEscapeSequence consumes the escape sequence starting at runes[i] (which is
// ESC) and returns the index of its final consumed rune, so the caller's loop
// advances past the whole sequence. Unterminated sequences consume to end.
func skipEscapeSequence(runes []rune, i int) int {
	// i points at ESC; look at the byte after it
	if i+1 >= len(runes) {
		return i // lone trailing ESC — drop it
	}
	next := runes[i+1]

	switch next {
	case '[': // CSI: ESC [ … <final 0x40-0x7e>
		j := i + 2
		for j < len(runes) {
			c := runes[j]
			if c >= 0x40 && c <= 0x7e {
				return j // final byte
			}
			j++
		}
		return len(runes) - 1
	case ']', 'P', 'X', '^', '_': // OSC, DCS, SOS, PM, APC — string terminated by BEL or ST (ESC \)
		j := i + 2
		for j < len(runes) {
			c := runes[j]
			if c == '\a' { // BEL terminator
				return j
			}
			if c == '\033' && j+1 < len(runes) && runes[j+1] == '\\' { // ST: ESC \
				return j + 1
			}
			j++
		}
		return len(runes) - 1
	default:
		// other two-byte escapes (e.g. ESC c, ESC =): drop ESC + the next byte
		return i + 1
	}
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

		dataDir, _ := resolvePreferredCodeDBDir(root)

		// Raw SQL bypasses bleve — open without it so a corrupt/locked/
		// mid-rebuild bleve sub-index can't block the query. SQLite WAL handles
		// concurrent readers even while the daemon writes.
		db, err := codedb.OpenSQLOnly(dataDir)
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

		dataDir, useLedger := resolvePreferredCodeDBDir(root)
		indexExists := false
		if _, err := os.Stat(dataDir); err == nil {
			indexExists = true
		}

		// get daemon stats for freshness and next-check info
		var codeStats *daemon.CodeDBStats
		var syncInterval time.Duration
		client := daemon.NewClientForCurrentRepoWithTimeout(500 * time.Millisecond)
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
		var totalCommits, totalBlobs, totalSymbols, totalPRs, totalIssues int
		type repoRow struct {
			name       string
			path       string
			commits    int
			blobs      int
			lastCommit int64 // unix timestamp of most recent commit
		}
		var repos []repoRow

		// openErr records why the counts below are missing. Without it the
		// output of an index that could not be opened is byte-identical to a
		// warm index holding nothing — zeros under index_exists: true — and a
		// caller searching that store gets no results and reports none.
		var openErr string
		var readOnly bool

		daemonIndexing := codeStats != nil && ((useLedger && codeStats.LedgerIndexingNow) || (!useLedger && codeStats.IndexingNow))
		if indexExists && !daemonIndexing {
			db, err := codedb.Open(dataDir)
			if err != nil {
				openErr = err.Error()
			}
			if err == nil {
				readOnly = db.ReadOnly()
				_ = db.Store().QueryRow("SELECT COUNT(*) FROM commits").Scan(&totalCommits)
				_ = db.Store().QueryRow("SELECT COUNT(*) FROM blobs").Scan(&totalBlobs)
				_ = db.Store().QueryRow("SELECT COUNT(*) FROM symbols").Scan(&totalSymbols)
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
				PRs         int             `json:"prs"`
				Issues      int             `json:"issues"`
				DiskBytes   int64           `json:"disk_bytes"`
				Repos       []jsonRepoStats `json:"repos"`
				DataDir     string          `json:"data_dir"`
				IndexExists bool            `json:"index_exists"`
				IndexingNow bool            `json:"indexing_now"`
				LastIndexed *time.Time      `json:"last_indexed,omitempty"`
				LastError   string          `json:"last_error,omitempty"`
				// OpenError is set when the index directory exists but could not
				// be opened. When present the counts above are unknown, not zero.
				OpenError string `json:"open_error,omitempty"`
				// ReadOnly reports an index served from media ox cannot write:
				// searchable, but never re-indexed or self-healed in place.
				ReadOnly bool `json:"read_only,omitempty"`
			}
			out := jsonStats{
				Commits:     totalCommits,
				Blobs:       totalBlobs,
				Symbols:     totalSymbols,
				PRs:         totalPRs,
				Issues:      totalIssues,
				DiskBytes:   dirSize(dataDir),
				DataDir:     dataDir,
				IndexExists: indexExists,
				OpenError:   openErr,
				ReadOnly:    readOnly,
			}
			if codeStats != nil {
				// daemonIndexing, not codeStats.IndexingNow: when this repo
				// reads the ledger index, LedgerIndexingNow is the flag that
				// gated the direct open above. Reporting the shared index's
				// flag here says "not indexing" while the counts came from the
				// daemon's cache precisely because it is.
				out.IndexingNow = daemonIndexing
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
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		// detect GitHub remote for repo identity
		ghOwner, ghRepo, _ := detectGitHubRemote()

		// human-readable output — Tufte-inspired, matching ox status
		var b strings.Builder

		// check for daemon-reported codedb cache wipe
		if ds, issueErr := client.Status(); issueErr == nil && ds.NeedsHelp {
			for _, issue := range ds.Issues {
				if issue.Type == daemon.IssueTypeCodeDBCacheWiped {
					b.WriteString(statusWarningStyle.Render("⚠ codedb cache was wiped — run 'ox code index' to rebuild"))
					b.WriteString("\n\n")
					break
				}
			}
		}

		// Surface self-heal markers: store.Open nukes + recreates a corrupt
		// bleve sub-index and writes a marker so the daemon's next pass forces
		// a full rebuild. Until that rebuild completes, search returns no
		// results for the affected sub-index — show the user why.
		if indexExists {
			if healing := store.NeedsReindexMarkers(dataDir); len(healing) > 0 {
				b.WriteString(statusWarningStyle.Render(
					fmt.Sprintf("⚠ rebuilding sub-index(es): %s", strings.Join(healing, ", "))))
				b.WriteString("\n")
				b.WriteString(statusLabelStyle.Render(""))
				b.WriteString(statusMutedStyle.Render("Auto-repair after corruption. Search returns empty until daemon completes; force now: "))
				b.WriteString(statusHighlightStyle.Render("ox code index --full"))
				b.WriteString("\n\n")
			}
		}

		b.WriteString(statusHeaderStyle.Render("Code Index"))
		b.WriteString("\n")
		b.WriteString(statusMutedStyle.Render("──────────"))
		b.WriteString("\n")

		// status line — health signal first
		b.WriteString(statusLabelStyle.Render("Status"))
		switch {
		case !indexExists && !daemonIndexing:
			b.WriteString(statusWarningStyle.Render("⚠ not indexed"))
			b.WriteString("\n")
			b.WriteString(statusLabelStyle.Render(""))
			b.WriteString(statusMutedStyle.Render("Run "))
			b.WriteString(statusHighlightStyle.Render("ox code index"))
			b.WriteString(statusMutedStyle.Render(" to create one"))
			b.WriteString("\n")
			fmt.Print(b.String())
			return nil
		case openErr != "":
			// Ranked above every daemon-derived state: the daemon's cached
			// counts describe an index this machine just failed to open, and
			// reporting them would hide that failure behind a healthy number.
			b.WriteString(statusErrorStyle.Render("✗ unreadable"))
			b.WriteString("\n")
			b.WriteString(statusLabelStyle.Render(""))
			b.WriteString(statusMutedStyle.Render(openErr))
		case daemonIndexing:
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
		case indexExists && totalCommits == 0:
			// index dir and schema exist but no data — prior indexing was interrupted or failed
			b.WriteString(statusWarningStyle.Render("⚠ empty index"))
			b.WriteString("\n")
			b.WriteString(statusLabelStyle.Render(""))
			b.WriteString(statusMutedStyle.Render("Run "))
			b.WriteString(statusHighlightStyle.Render("ox code index"))
			b.WriteString(statusMutedStyle.Render(" to populate it"))
		default:
			b.WriteString(statusSuccessStyle.Render("✓ indexed"))
		}
		b.WriteString("\n")

		if readOnly {
			b.WriteString(statusLabelStyle.Render("Mode"))
			b.WriteString(statusWarningStyle.Render("read-only"))
			b.WriteString("\n")
			b.WriteString(statusLabelStyle.Render(""))
			b.WriteString(statusMutedStyle.Render("Served from media ox cannot write — searchable, but "))
			b.WriteString(statusHighlightStyle.Render("ox code index"))
			b.WriteString(statusMutedStyle.Render(" will refuse"))
			b.WriteString("\n")
		}

		// repo identity — show GitHub remote name if detected
		if ghOwner != "" && ghRepo != "" {
			b.WriteString(statusLabelStyle.Render("Repository"))
			b.WriteString(statusHighlightStyle.Render(ghOwner + "/" + ghRepo))
			b.WriteString("\n")
		}

		// disk usage
		if indexExists {
			b.WriteString(statusLabelStyle.Render("Disk"))
			b.WriteString(statusValueStyle.Render(humanSize(dirSize(dataDir))))
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
		if codeStats != nil && !daemonIndexing && indexExists && syncInterval > 0 && !codeStats.LastIndexed.IsZero() {
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

// formatSearchLatency formats a per-query latency for the agent-facing stderr
// stats line. Sub-second precision matters here so the agent learns ox code is
// fast (formatDurationBrief floors to "<1s", which hides the signal). Uses
// microsecond/millisecond resolution under one second, then falls back to the
// human-friendly brief format for anything slower.
func formatSearchLatency(d time.Duration) string {
	switch {
	case d < time.Microsecond:
		return "<1µs"
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	default:
		return formatDurationBrief(d)
	}
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
	codeSearchCmd.Flags().Bool("full-json", false, "full uncompacted JSON output (~4x more context tokens)")
	codeSearchCmd.Flags().Int("limit", 10, "max results to return")
	codeSearchCmd.Flags().Bool("decisions", false, "only results from this repo's decision records (ADRs/DDRs)")
	codeSearchCmd.Flags().Int("snippet", defaultSnippetLen, "max snippet length per result, in characters")

	codeQueryCmd.Flags().Bool("full-json", false, "full uncompacted JSON output (~4x more context tokens)")
	codeQueryCmd.Flags().Int("limit", 10, "max results to return")
	codeQueryCmd.Flags().Int("snippet", defaultSnippetLen, "max snippet length per result, in characters")

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
