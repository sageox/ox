package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
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
	indexing   bool
	lastIndex time.Time
	lastErr   error
	stats     CodeDBStats
}

// CodeDBStats tracks index statistics.
type CodeDBStats struct {
	Commits     int       `json:"commits"`
	Blobs       int       `json:"blobs"`
	Symbols     int       `json:"symbols"`
	Comments    int       `json:"comments"`
	PRs         int       `json:"prs"`
	Issues      int       `json:"issues"`
	Repos       []RepoStats `json:"repos,omitempty"`
	LastIndexed time.Time `json:"last_indexed,omitempty"`
	IndexingNow bool      `json:"indexing_now"`
	LastError   string    `json:"last_error,omitempty"`
	DataDir     string    `json:"data_dir"`
	IndexExists bool      `json:"index_exists"`
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
func (m *CodeDBManager) resolveSharedDataDir() string {
	ctx, err := config.LoadProjectContext(m.projectRoot)
	if err == nil {
		if dir := paths.CodeDBSharedDir(ctx.RepoID(), ctx.Endpoint()); dir != "" {
			return dir
		}
	}
	m.logger.Debug("falling back to legacy codedb path", "reason", err)
	return paths.CodeDBDataDir(m.projectRoot)
}

// SetLedgerPath sets the ledger checkout path for GitHub data indexing.
// Called by the daemon when the ledger workspace is discovered.
func (m *CodeDBManager) SetLedgerPath(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ledgerPath = path
}

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

	dataDir := m.resolveSharedDataDir()

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
			_ = pw.WriteStage("indexing", fmt.Sprintf("Indexing local repo %s...", m.projectRoot))
		}
		if err := db.IndexLocalRepo(ctx, m.projectRoot, opts); err != nil {
			m.setError(err)
			return nil, fmt.Errorf("index local: %w", err)
		}
	}
	indexDuration := time.Since(indexStart)

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

	// stage 4: build dirty overlay index for uncommitted worktree files
	var dirtyDuration time.Duration
	if payload.URL == "" {
		dirtyStart := time.Now()
		if pw != nil {
			_ = pw.WriteStage("dirty", "Indexing dirty files...")
		}
		dirtyCount, dirtyErr := db.BuildDirtyIndex(ctx, m.projectRoot, opts)
		if dirtyErr != nil {
			m.logger.Warn("dirty index build failed", "error", dirtyErr)
		} else if dirtyCount > 0 {
			m.logger.Debug("dirty index built", "files", dirtyCount)
		}
		dirtyDuration = time.Since(dirtyStart)
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
		ghStats, ghErr := db.IndexGitHubData(ctx, lp, func(msg string) {
			if pw != nil {
				_ = pw.WriteMessage(msg)
			}
		})
		if ghErr != nil {
			m.logger.Warn("github data indexing failed", "error", ghErr)
			// non-fatal: don't fail the whole index for GitHub data
		} else if ghStats.PRsIndexed > 0 || ghStats.IssuesIndexed > 0 {
			m.logger.Info("github data indexed", "prs", ghStats.PRsIndexed, "issues", ghStats.IssuesIndexed)
		}
	}

	m.mu.Lock()
	m.lastIndex = time.Now()
	m.lastErr = nil
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
	m.mu.Lock()
	if m.indexing {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	dataDir := m.resolveSharedDataDir()
	isInitial := false
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		isInitial = true
	}

	// run background index (initial build or incremental refresh)
	go func() {
		if isInitial {
			m.logger.Info("codedb auto-indexing repo for first time")
		} else {
			m.logger.Debug("codedb freshness check starting")
		}
		result, err := m.Index(ctx, CodeIndexPayload{}, nil)
		if err != nil {
			if isInitial {
				m.logger.Warn("codedb initial index failed", "error", err)
			} else {
				m.logger.Debug("codedb freshness check failed", "error", err)
			}
		}
		if m.telemetry != nil && result != nil {
			m.telemetry.RecordCodeIndexComplete(result, "success")
		}
	}()
}

// Stats returns current index statistics.
func (m *CodeDBManager) Stats() CodeDBStats {
	m.mu.Lock()
	indexing := m.indexing
	lastIndex := m.lastIndex
	lastErr := m.lastErr
	m.mu.Unlock()

	dataDir := m.resolveSharedDataDir()
	stats := CodeDBStats{
		DataDir:     dataDir,
		IndexingNow: indexing,
		LastIndexed: lastIndex,
	}
	if lastErr != nil {
		stats.LastError = lastErr.Error()
	}

	if _, err := os.Stat(dataDir); err == nil {
		stats.IndexExists = true

		db, err := codedb.Open(dataDir)
		if err == nil {
			defer db.Close()
			_ = db.Store().QueryRow("SELECT COUNT(*) FROM commits").Scan(&stats.Commits)
			_ = db.Store().QueryRow("SELECT COUNT(*) FROM blobs").Scan(&stats.Blobs)
			_ = db.Store().QueryRow("SELECT COUNT(*) FROM symbols").Scan(&stats.Symbols)
			_ = db.Store().QueryRow("SELECT COUNT(*) FROM comments").Scan(&stats.Comments)
			_ = db.Store().QueryRow("SELECT COUNT(*) FROM pull_requests").Scan(&stats.PRs)
			_ = db.Store().QueryRow("SELECT COUNT(*) FROM issues").Scan(&stats.Issues)

			// per-repo breakdown
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
		}
	}

	return stats
}

func (m *CodeDBManager) setError(err error) {
	m.mu.Lock()
	m.lastErr = err
	m.mu.Unlock()
}
