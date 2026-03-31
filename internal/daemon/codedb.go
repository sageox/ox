package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/index"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/paths"
)

// CodeDBManager manages CodeDB indexing in the daemon.
// It ensures only one indexing operation runs at a time and tracks index status.
//
// Concurrency note: The in-process mutex prevents concurrent indexing within a
// single daemon, but today multiple daemons can exist for the same repo (one per
// worktree). Cross-process safety currently relies on SQLite WAL mode with
// busy_timeout(5000ms) — concurrent readers are fine, but two daemons indexing
// simultaneously could contend on SQLite/Bleve write locks. This is a known
// short-term limitation. When the daemon model moves to one-per-repo (shared
// across worktrees), the in-process mutex will be sufficient and no flock will
// be needed.
type CodeDBManager struct {
	projectRoot string
	ledgerPath  string // path to ledger checkout; empty if no ledger exists
	logger      *slog.Logger
	telemetry   *TelemetryCollector

	mu        sync.Mutex
	indexing  bool
	lastIndex time.Time
	lastErr   error
	stats     CodeDBStats // cached after each index run; read by Stats() without opening DB
	dataDir   string      // cached data dir; resolved once on first use

	// testHook is called at the start of doIndex; nil in production.
	// Tests use it to synchronize with or measure the indexing goroutine.
	testHook func()

	issues *IssueTracker // emits structured issues for ox status / ox doctor

	// baseline index state (separate lifecycle from worktree indexing)
	baselineIndexing bool
	baselineStats    CodeDBStats
	baselineDataDir  string // cached baseline dir; resolved once on first use
	// baselineTestHook is called at the start of BuildBaseline; nil in production.
	baselineTestHook func()
}

// CodeDBStats tracks index statistics.
type CodeDBStats struct {
	Commits     int         `json:"commits"`
	Blobs       int         `json:"blobs"`
	Symbols     int         `json:"symbols"`
	Comments    int         `json:"comments"`
	PRs         int         `json:"prs"`
	Issues      int         `json:"issues"`
	Repos       []RepoStats `json:"repos,omitempty"`
	LastIndexed time.Time   `json:"last_indexed,omitempty"`
	IndexingNow bool        `json:"indexing_now"`
	LastError   string      `json:"last_error,omitempty"`
	DataDir     string      `json:"data_dir"`
	IndexExists bool        `json:"index_exists"`

	// baseline index fields (ledger main branch, worktree-independent)
	BaselineExists      bool `json:"baseline_exists"`
	BaselineCommits     int  `json:"baseline_commits"`
	BaselineIndexingNow bool `json:"baseline_indexing_now"`
}

// RepoStats tracks per-repo statistics within the index.
type RepoStats struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Commits int    `json:"commits"`
	Blobs   int    `json:"blobs"`
}

// CodeIndexPayload is the IPC payload for code_index requests.
type CodeIndexPayload struct {
	// URL is an optional remote git URL to index. If empty, indexes the local repo.
	URL string `json:"url,omitempty"`
	// Full wipes the existing index before rebuilding. Used by 'ox index --full'.
	Full bool `json:"full,omitempty"`
}

// CodeIndexResult is the result of a code_index operation.
type CodeIndexResult struct {
	BlobsParsed       uint64 `json:"blobs_parsed"`
	SymbolsExtracted  uint64 `json:"symbols_extracted"`
	CommentsExtracted uint64 `json:"comments_extracted"`

	// Per-stage timing in milliseconds
	IndexDurationMs   int64 `json:"index_duration_ms"`
	SymbolDurationMs  int64 `json:"symbol_duration_ms"`
	CommentDurationMs int64 `json:"comment_duration_ms"`
	TotalDurationMs   int64 `json:"total_duration_ms"`
}

// NewCodeDBManager creates a new CodeDB manager for the given project root.
// Resolves the shared CodeDB path via project config (ledger cache).
// Falls back to the legacy per-worktree path if project config is unavailable.
func NewCodeDBManager(projectRoot string, logger *slog.Logger, telemetry *TelemetryCollector) *CodeDBManager {
	return &CodeDBManager{
		projectRoot: projectRoot,
		logger:      logger,
		telemetry:   telemetry,
	}
}

// resolveSharedDataDir returns the shared CodeDB directory from project config.
// Falls back to legacy per-worktree path if config is unavailable.
// Result is cached after first resolution.
func (m *CodeDBManager) resolveSharedDataDir() string {
	m.mu.Lock()
	if m.dataDir != "" {
		dir := m.dataDir
		m.mu.Unlock()
		return dir
	}
	projectRoot := m.projectRoot // snapshot under lock to avoid races with UpdateProjectRoot
	m.mu.Unlock()

	ctx, err := config.LoadProjectContext(projectRoot)
	if err == nil {
		if dir := paths.CodeDBSharedDir(ctx.RepoID(), ctx.Endpoint()); dir != "" {
			// clean up legacy root-level codedb/ if it exists
			legacyDir := paths.LedgersDataDir(ctx.RepoID(), ctx.Endpoint())
			if legacyDir != "" {
				legacyCodedb := filepath.Join(legacyDir, "codedb")
				if _, statErr := os.Stat(legacyCodedb); statErr == nil {
					m.logger.Info("removing legacy codedb at ledger root", "old", legacyCodedb, "new", dir)
					_ = os.RemoveAll(legacyCodedb)
				}
			}
			m.mu.Lock()
			m.dataDir = dir
			m.mu.Unlock()
			return dir
		}
	}
	m.logger.Debug("falling back to legacy codedb path", "reason", err)
	dir := paths.CodeDBDataDir(projectRoot)
	m.mu.Lock()
	m.dataDir = dir
	m.mu.Unlock()
	return dir
}

// ledgerRootForDataDir returns the ledger root directory if dataDir is inside a
// ledger's .sageox/cache/ tree. Returns "" if dataDir is not a ledger-based path.
// The shared CodeDB path is <ledger_root>/.sageox/cache/codedb/, so we look for
// the /.sageox/cache/ suffix to extract the ledger root.
func (m *CodeDBManager) ledgerRootForDataDir(dataDir string) string {
	// CodeDBSharedDir produces: <ledger_root>/.sageox/cache/codedb
	const marker = string(filepath.Separator) + ".sageox" + string(filepath.Separator) + "cache" + string(filepath.Separator)
	if idx := strings.Index(dataDir, marker); idx > 0 {
		return dataDir[:idx]
	}
	return ""
}

// SetLedgerPath sets the ledger checkout path for GitHub data indexing.
// Called by the daemon when the ledger workspace is discovered.
func (m *CodeDBManager) SetLedgerPath(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ledgerPath = path
}

// SetIssueTracker wires the daemon's issue tracker so doIndex can emit
// structured issues when the cache directory is missing.
func (m *CodeDBManager) SetIssueTracker(tracker *IssueTracker) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.issues = tracker
}

// resolveBaselineDataDir returns the baseline CodeDB directory from project config.
// Returns empty string if project config is unavailable.
// Result is cached after first resolution.
func (m *CodeDBManager) resolveBaselineDataDir() string {
	m.mu.Lock()
	if m.baselineDataDir != "" {
		dir := m.baselineDataDir
		m.mu.Unlock()
		return dir
	}
	projectRoot := m.projectRoot
	m.mu.Unlock()

	ctx, err := config.LoadProjectContext(projectRoot)
	if err != nil {
		return ""
	}
	dir := paths.CodeDBBaselineDir(ctx.RepoID(), ctx.Endpoint())
	if dir == "" {
		return ""
	}
	m.mu.Lock()
	m.baselineDataDir = dir
	m.mu.Unlock()
	return dir
}

// BuildBaseline builds or refreshes the baseline index from the ledger's main branch.
// This is independent of the worktree index — baseline provides committed content search
// even when no worktree is active.
// Non-blocking: if a baseline build is already in progress, returns immediately.
func (m *CodeDBManager) BuildBaseline(ctx context.Context, ledgerPath string) {
	if ledgerPath == "" {
		return
	}

	m.mu.Lock()
	if m.baselineIndexing {
		m.mu.Unlock()
		return
	}
	m.baselineIndexing = true
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.baselineIndexing = false
		m.mu.Unlock()
	}()

	if m.baselineTestHook != nil {
		m.baselineTestHook()
	}

	baselineDir := m.resolveBaselineDataDir()
	if baselineDir == "" {
		m.logger.Debug("codedb baseline: no baseline dir available")
		return
	}

	if _, err := os.Stat(ledgerPath); os.IsNotExist(err) {
		m.logger.Debug("codedb baseline: ledger path gone", "path", ledgerPath)
		return
	}

	if err := os.MkdirAll(baselineDir, 0o755); err != nil {
		m.logger.Warn("codedb baseline: create dir failed", "error", err)
		return
	}

	indexCtx, cancel := context.WithTimeout(ctx, maxIndexDuration)
	defer cancel()

	start := time.Now()
	m.logger.Info("codedb baseline build started", "ledger", ledgerPath, "dir", baselineDir)

	db, err := codedb.Open(baselineDir)
	if err != nil {
		m.logger.Warn("codedb baseline: open failed", "error", err)
		return
	}
	defer db.Close()

	opts := index.IndexOptions{}

	if err := db.IndexLocalRepo(indexCtx, ledgerPath, opts); err != nil {
		m.logger.Warn("codedb baseline: index failed", "error", err)
		return
	}

	if _, err := db.ParseSymbols(indexCtx, nil); err != nil {
		m.logger.Warn("codedb baseline: parse symbols failed", "error", err)
		// non-fatal: committed content is already indexed
	}

	if _, err := db.ParseComments(indexCtx, nil); err != nil {
		m.logger.Warn("codedb baseline: parse comments failed", "error", err)
		// non-fatal
	}

	cached := queryStatsFromDB(db, baselineDir)
	m.mu.Lock()
	m.baselineStats = cached
	m.mu.Unlock()

	m.logger.Info("codedb baseline build complete", "duration", time.Since(start).Round(time.Millisecond), "commits", cached.Commits, "symbols", cached.Symbols)
}

// maxIndexDuration caps how long a single indexing run may take before being
// canceled. 21h+ runaway burns were observed when a worktree was deleted mid-index.
const maxIndexDuration = 2 * time.Hour

// Index runs indexing with progress reporting. Only one indexing operation runs at a time.
// If indexing is already in progress, returns an error immediately.
//
// TODO: When multiple daemons share the same CodeDB (worktree scenario), add a
// filesystem flock on the data dir to prevent concurrent write contention across
// processes. Until then, busy_timeout(5000ms) on SQLite provides best-effort
// protection but Bleve's bolt backend only allows one writer at a time and will
// error if two daemons index simultaneously.
func (m *CodeDBManager) Index(ctx context.Context, payload CodeIndexPayload, pw *ProgressWriter) (*CodeIndexResult, error) {
	// in-process mutex — sufficient for single-daemon-per-repo, not cross-process.
	// see struct-level comment for the multi-daemon worktree caveat.
	m.mu.Lock()
	if m.indexing {
		m.mu.Unlock()
		return nil, fmt.Errorf("indexing already in progress")
	}
	m.indexing = true
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.indexing = false
		m.mu.Unlock()
	}()

	return m.doIndex(ctx, payload, pw)
}

// doIndex executes the full indexing pipeline. Callers must already own the
// m.indexing flag (i.e. have set it to true under m.mu before calling).
func (m *CodeDBManager) doIndex(ctx context.Context, payload CodeIndexPayload, pw *ProgressWriter) (*CodeIndexResult, error) {
	if m.testHook != nil {
		m.testHook()
	}

	m.mu.Lock()
	projectRoot := m.projectRoot // snapshot under lock to avoid races with UpdateProjectRoot
	m.mu.Unlock()

	// Fail fast if the worktree no longer exists (e.g. Conductor deleted it).
	// go-git can still open the gitdir and iterate all of history indefinitely
	// even after the worktree directory is removed.
	if payload.URL == "" {
		if _, err := os.Stat(projectRoot); os.IsNotExist(err) {
			return nil, fmt.Errorf("project root no longer exists, skipping index %s: %w", projectRoot, err)
		}
	}

	dataDir := m.resolveSharedDataDir()

	// Guard: if dataDir lives inside a ledger that hasn't been cloned yet,
	// skip indexing. Without this check, os.MkdirAll(dataDir) creates the
	// ledger root directory as a side effect, which causes the subsequent
	// git clone to fail with "already exists and is not an empty directory".
	if ledgerRoot := m.ledgerRootForDataDir(dataDir); ledgerRoot != "" {
		gitDir := filepath.Join(ledgerRoot, ".git")
		if _, err := os.Stat(gitDir); os.IsNotExist(err) {
			return nil, fmt.Errorf("ledger not yet cloned at %s, skipping index", ledgerRoot)
		}
	}

	target := projectRoot
	if payload.URL != "" {
		target = payload.URL
	}
	m.logger.Info("codedb indexing started", "target", target, "data_dir", dataDir, "full", payload.Full)

	// --full: wipe existing index so we rebuild from scratch
	if payload.Full {
		m.logger.Info("codedb full reindex requested, wiping existing index", "path", dataDir)
		if err := os.RemoveAll(dataDir); err != nil {
			return nil, fmt.Errorf("wipe codedb for full reindex: %w", err)
		}
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create codedb dir: %w", err)
	}

	db, err := codedb.Open(dataDir)
	if err != nil {
		return nil, fmt.Errorf("open codedb: %w", err)
	}
	defer db.Close()

	totalStart := time.Now()

	// periodic progress logging for long-running indexing
	progressCtx, progressCancel := context.WithCancel(ctx)
	defer progressCancel()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-progressCtx.Done():
				return
			case <-ticker.C:
				m.logger.Info("codedb indexing in progress", "elapsed", time.Since(totalStart).Round(time.Second), "target", target)
			}
		}
	}()

	opts := index.IndexOptions{
		Progress: func(msg string) {
			if pw != nil {
				_ = pw.WriteMessage(msg)
			}
		},
	}

	// stage 1: git indexing (committed content only)
	indexStart := time.Now()
	if payload.URL != "" {
		if pw != nil {
			_ = pw.WriteStage("indexing", fmt.Sprintf("Indexing %s...", payload.URL))
		}
		if err := db.IndexRepo(ctx, payload.URL, opts); err != nil {
			m.setError(err)
			return nil, fmt.Errorf("index: %w", err)
		}
	} else {
		if pw != nil {
			_ = pw.WriteStage("indexing", fmt.Sprintf("Indexing local repo %s...", projectRoot))
		}
		if err := db.IndexLocalRepo(ctx, projectRoot, opts); err != nil {
			m.setError(err)
			return nil, fmt.Errorf("index local: %w", err)
		}
	}
	indexDuration := time.Since(indexStart)
	m.logger.Info("codedb stage complete", "stage", "git-index", "duration", indexDuration.Round(time.Millisecond))

	// stage 2: symbol extraction
	symbolStart := time.Now()
	if pw != nil {
		_ = pw.WriteStage("symbols", "Parsing symbols...")
	}
	stats, err := db.ParseSymbols(ctx, func(msg string) {
		if pw != nil {
			_ = pw.WriteMessage(msg)
		}
	})
	if err != nil {
		m.setError(err)
		return nil, fmt.Errorf("parse symbols: %w", err)
	}
	symbolDuration := time.Since(symbolStart)
	m.logger.Info("codedb stage complete", "stage", "symbols", "duration", symbolDuration.Round(time.Millisecond), "extracted", stats.SymbolsExtracted)

	// stage 3: comment extraction
	commentStart := time.Now()
	if pw != nil {
		_ = pw.WriteStage("comments", "Extracting comments...")
	}
	cStats, err := db.ParseComments(ctx, func(msg string) {
		if pw != nil {
			_ = pw.WriteMessage(msg)
		}
	})
	if err != nil {
		m.setError(err)
		return nil, fmt.Errorf("parse comments: %w", err)
	}
	commentDuration := time.Since(commentStart)
	m.logger.Info("codedb stage complete", "stage", "comments", "duration", commentDuration.Round(time.Millisecond), "extracted", cStats.CommentsExtracted)

	// stage 4: build dirty overlay index for uncommitted worktree files
	var dirtyDuration time.Duration
	if payload.URL == "" {
		dirtyStart := time.Now()
		if pw != nil {
			_ = pw.WriteStage("dirty", "Indexing dirty files...")
		}
		dirtyCount, dirtyErr := db.BuildDirtyIndex(ctx, projectRoot, opts)
		if dirtyErr != nil {
			m.logger.Warn("dirty index build failed", "error", dirtyErr)
		} else if dirtyCount > 0 {
			m.logger.Debug("dirty index built", "files", dirtyCount)
		}
		dirtyDuration = time.Since(dirtyStart)

		// also write dirty overlay to baseline dir so CLI search finds it
		if baseDir := m.resolveBaselineDataDir(); baseDir != "" && baseDir != dataDir {
			baseDB, bErr := codedb.Open(baseDir)
			if bErr == nil {
				baseDB.BuildDirtyIndex(ctx, projectRoot, opts)
				baseDB.Close()
			}
		}
	}
	totalDuration := time.Since(totalStart)

	// index GitHub data from ledger (PRs, issues)
	m.mu.Lock()
	lp := m.ledgerPath
	m.mu.Unlock()

	if lp != "" {
		if pw != nil {
			_ = pw.WriteStage("github", "Indexing GitHub data from ledger...")
		}
		ghStart := time.Now()
		ghStats, ghErr := db.IndexGitHubData(ctx, lp, func(msg string) {
			if pw != nil {
				_ = pw.WriteMessage(msg)
			}
		})
		if ghErr != nil {
			m.logger.Warn("github data indexing failed", "error", ghErr)
			// non-fatal: don't fail the whole index for GitHub data
		} else if ghStats.PRsIndexed > 0 || ghStats.IssuesIndexed > 0 {
			m.logger.Info("codedb stage complete", "stage", "github-data", "duration", time.Since(ghStart).Round(time.Millisecond), "prs", ghStats.PRsIndexed, "issues", ghStats.IssuesIndexed)
		}
	}

	// cache stats from the still-open DB connection so Stats() never has to reopen
	cachedStats := queryStatsFromDB(db, dataDir)

	m.mu.Lock()
	m.lastIndex = time.Now()
	m.lastErr = nil
	m.stats = cachedStats
	m.mu.Unlock()

	logArgs := []any{
		"blobs_parsed", stats.BlobsParsed,
		"symbols_extracted", stats.SymbolsExtracted,
		"comments_extracted", cStats.CommentsExtracted,
		"index_ms", indexDuration.Milliseconds(),
		"symbols_ms", symbolDuration.Milliseconds(),
		"comments_ms", commentDuration.Milliseconds(),
		"total_ms", totalDuration.Milliseconds(),
	}
	if dirtyDuration > 0 {
		logArgs = append(logArgs, "dirty_ms", dirtyDuration.Milliseconds())
	}
	m.logger.Info("codedb indexing complete", logArgs...)

	return &CodeIndexResult{
		BlobsParsed:       stats.BlobsParsed,
		SymbolsExtracted:  stats.SymbolsExtracted,
		CommentsExtracted: cStats.CommentsExtracted,
		IndexDurationMs:   indexDuration.Milliseconds(),
		SymbolDurationMs:  symbolDuration.Milliseconds(),
		CommentDurationMs: commentDuration.Milliseconds(),
		TotalDurationMs:   totalDuration.Milliseconds(),
	}, nil
}

// CheckFreshness checks if the index needs refreshing and triggers a background
// re-index if needed. If no index exists yet, creates the initial index.
// This is non-blocking and safe to call from the scheduler or daemon startup.
func (m *CodeDBManager) CheckFreshness(ctx context.Context) {
	// Claim the indexing flag BEFORE launching the goroutine.
	// Without this, rapid CheckFreshness calls (e.g. from the file-watcher path)
	// can see m.indexing=false, all launch goroutines, and then race to call
	// m.Index() — the losers immediately return "indexing already in progress"
	// and call gcDirtyIndexes, which opens codedb while the winner holds bbolt's
	// exclusive lock. That open fails with a timeout (not ENOENT), which
	// openOrCreateBleveIndex misinterprets as corruption and wipes the bleve
	// directory mid-index.
	m.mu.Lock()
	if m.indexing {
		m.mu.Unlock()
		return
	}
	m.indexing = true
	m.mu.Unlock()

	// guard: skip dirty rebuild when worktree is gone (baseline is unaffected)
	m.mu.Lock()
	projectRoot := m.projectRoot
	m.mu.Unlock()
	if _, err := os.Stat(projectRoot); os.IsNotExist(err) {
		m.logger.Debug("codedb skipping freshness check, worktree gone", "path", projectRoot)
		m.mu.Lock()
		m.indexing = false
		m.mu.Unlock()
		return
	}

	dataDir := m.resolveSharedDataDir()
	isInitial := false
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		isInitial = true
	} else {
		// dir exists but check if it was never successfully indexed
		// (e.g. a prior run created the schema then crashed before writing commits)
		m.mu.Lock()
		emptyDB := m.stats.Commits == 0 && !m.stats.IndexExists
		m.mu.Unlock()
		if emptyDB {
			isInitial = true
		}
	}

	// run background index (initial build or incremental refresh)
	go func() {
		defer func() {
			m.mu.Lock()
			m.indexing = false
			m.mu.Unlock()
		}()

		if isInitial {
			m.logger.Info("codedb auto-indexing repo for first time")
		} else {
			m.logger.Info("codedb freshness check starting")
		}
		// Cap indexing time — without a deadline, a deleted worktree can cause
		// go-git to iterate git history indefinitely (observed: 21+ hours).
		indexCtx, cancel := context.WithTimeout(ctx, maxIndexDuration)
		defer cancel()
		// Call doIndex directly — we already own m.indexing, so Index() would
		// deadlock (it also tries to claim the flag).
		result, err := m.doIndex(indexCtx, CodeIndexPayload{}, nil)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && m.issues != nil {
				m.issues.SetIssue(DaemonIssue{
					Type:     IssueTypeCodeDBCacheWiped,
					Severity: SeverityWarning,
					Repo:     "codedb",
					Summary:  "codedb cache directory missing; sparse-checkout may have wiped .sageox/cache/",
				})
			}
			if isInitial {
				m.logger.Warn("codedb initial index failed", "error", err)
			} else {
				m.logger.Debug("codedb freshness check failed", "error", err)
			}
		}
		if err == nil && m.issues != nil {
			m.issues.ClearIssue(IssueTypeCodeDBCacheWiped, "codedb")
		}
		if m.telemetry != nil && result != nil {
			m.telemetry.RecordCodeIndexComplete(result, "success")
		}

		// GC stale dirty overlays for worktrees that no longer exist.
		// Run after indexing so the new overlay (if any) is in place before we inspect.
		m.gcDirtyIndexes(dataDir)
	}()
}

// gcDirtyIndexes removes stale dirty overlay directories and logs the result.
func (m *CodeDBManager) gcDirtyIndexes(dataDir string) {
	db, err := codedb.Open(dataDir)
	if err != nil {
		return
	}
	defer db.Close()
	removed, err := db.GCDirtyIndexes()
	if err != nil {
		m.logger.Warn("codedb dirty index GC failed", "error", err)
		return
	}
	if removed > 0 {
		m.logger.Info("codedb dirty index GC: removed stale overlays", "count", removed)
	}
}

// Stats returns current index statistics.
// Returns cached stats from the last index run to avoid blocking on SQLite
// during active indexing. Only queries the DB on cold start (no cached stats
// and not currently indexing).
func (m *CodeDBManager) Stats() CodeDBStats {
	m.mu.Lock()
	indexing := m.indexing
	lastIndex := m.lastIndex
	lastErr := m.lastErr
	cached := m.stats
	baselineIndexing := m.baselineIndexing
	baselineStats := m.baselineStats
	m.mu.Unlock()

	dataDir := m.resolveSharedDataDir()

	// merge baseline fields into result
	mergeBaseline := func(s *CodeDBStats) {
		s.BaselineIndexingNow = baselineIndexing
		s.BaselineExists = baselineStats.IndexExists
		s.BaselineCommits = baselineStats.Commits
	}

	// if we have cached stats, return them with live metadata
	if cached.IndexExists {
		cached.IndexingNow = indexing
		cached.LastIndexed = lastIndex
		cached.DataDir = dataDir
		cached.LastError = ""
		if lastErr != nil {
			cached.LastError = lastErr.Error()
		}
		mergeBaseline(&cached)
		return cached
	}

	// no cached stats yet — cold start before first index completes
	result := CodeDBStats{
		DataDir:     dataDir,
		IndexingNow: indexing,
		LastIndexed: lastIndex,
	}
	if lastErr != nil {
		result.LastError = lastErr.Error()
	}

	// if indexing is active, don't try to open the DB (it will block)
	if indexing {
		if _, err := os.Stat(dataDir); err == nil {
			result.IndexExists = true
		}
		mergeBaseline(&result)
		return result
	}

	// not indexing and no cache: try a quick read to populate initial stats
	if _, err := os.Stat(dataDir); err == nil {
		result.IndexExists = true

		db, err := codedb.Open(dataDir)
		if err == nil {
			defer db.Close()
			result = queryStatsFromDB(db, dataDir)
			result.IndexingNow = indexing
			result.LastIndexed = lastIndex
			if lastErr != nil {
				result.LastError = lastErr.Error()
			}

			// cache for future calls
			m.mu.Lock()
			m.stats = result
			m.mu.Unlock()
		}
	}

	mergeBaseline(&result)
	return result
}

// queryStatsFromDB reads index stats from an already-open DB connection.
func queryStatsFromDB(db *codedb.DB, dataDir string) CodeDBStats {
	stats := CodeDBStats{
		DataDir:     dataDir,
		IndexExists: true,
	}
	_ = db.Store().QueryRow("SELECT COUNT(*) FROM commits").Scan(&stats.Commits)
	_ = db.Store().QueryRow("SELECT COUNT(*) FROM blobs").Scan(&stats.Blobs)
	_ = db.Store().QueryRow("SELECT COUNT(*) FROM symbols").Scan(&stats.Symbols)
	_ = db.Store().QueryRow("SELECT COUNT(*) FROM comments").Scan(&stats.Comments)
	_ = db.Store().QueryRow("SELECT COUNT(*) FROM pull_requests").Scan(&stats.PRs)
	_ = db.Store().QueryRow("SELECT COUNT(*) FROM issues").Scan(&stats.Issues)

	rows, err := db.Store().Query(`
		SELECT r.name, r.path, COUNT(DISTINCT c.id) as commits,
		       COUNT(DISTINCT fr.blob_id) as blobs
		FROM repos r
		LEFT JOIN commits c ON c.repo_id = r.id
		LEFT JOIN file_revs fr ON fr.commit_id = c.id
		GROUP BY r.id
		ORDER BY r.name`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var rs RepoStats
			if rows.Scan(&rs.Name, &rs.Path, &rs.Commits, &rs.Blobs) == nil {
				stats.Repos = append(stats.Repos, rs)
			}
		}
	}

	return stats
}

// UpdateProjectRoot updates the project root path used for indexing.
// Called when a heartbeat arrives from a different workspace (e.g., Conductor
// creates a new workspace after deleting the old one).
//
// dataDir is intentionally NOT reset here: all worktrees of the same repo share
// the same dataDir (keyed by repo ID + endpoint). Resetting it on every heartbeat
// from a different worktree causes repeated config re-resolution on repos with
// many active Conductor workspaces, producing log spam and unnecessary I/O.
func (m *CodeDBManager) UpdateProjectRoot(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if path == "" || path == m.projectRoot {
		return
	}
	// Only switch if the current project root no longer exists on disk.
	// This handles the Conductor pattern (old workspace deleted, new one created)
	// while preventing oscillation when multiple sessions share the same daemon
	// (e.g., one session in the main worktree, one in a Conductor worktree,
	// both alive simultaneously — without this guard, their heartbeats cause
	// the root to flip every 60s, triggering repeated full re-indexes).
	if _, err := os.Stat(m.projectRoot); err == nil {
		return
	}
	old := m.projectRoot
	m.projectRoot = path
	m.logger.Info("codedb project root updated", "old", old, "new", path)
}

func (m *CodeDBManager) setError(err error) {
	m.mu.Lock()
	m.lastErr = err
	m.mu.Unlock()
}
