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
	logger      *slog.Logger

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
}

// CodeIndexResult is the result of a code_index operation.
type CodeIndexResult struct {
	BlobsParsed      uint64 `json:"blobs_parsed"`
	SymbolsExtracted uint64 `json:"symbols_extracted"`
}

// NewCodeDBManager creates a new CodeDB manager for the given project root.
func NewCodeDBManager(projectRoot string, logger *slog.Logger) *CodeDBManager {
	return &CodeDBManager{
		projectRoot: projectRoot,
		logger:      logger,
	}
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

	dataDir := paths.CodeDBDataDir(m.projectRoot)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create codedb dir: %w", err)
	}

	db, err := codedb.Open(dataDir)
	if err != nil {
		return nil, fmt.Errorf("open codedb: %w", err)
	}
	defer db.Close()

	opts := index.IndexOptions{
		Progress: func(msg string) {
			if pw != nil {
				_ = pw.WriteMessage(msg)
			}
		},
	}

	if payload.URL != "" {
		if pw != nil {
			_ = pw.WriteStage("indexing", fmt.Sprintf("Indexing %s...", payload.URL))
		}
		if err := db.IndexRepo(ctx, payload.URL, opts); err != nil {
			m.setError(err)
			return nil, fmt.Errorf("index: %w", err)
		}
	} else {
		// index local repo; IndexLocalRepo validates the path is a git repo
		if pw != nil {
			_ = pw.WriteStage("indexing", fmt.Sprintf("Indexing local repo %s...", m.projectRoot))
		}
		if err := db.IndexLocalRepo(ctx, m.projectRoot, opts); err != nil {
			m.setError(err)
			return nil, fmt.Errorf("index local: %w", err)
		}
	}

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

	m.mu.Lock()
	m.lastIndex = time.Now()
	m.lastErr = nil
	m.mu.Unlock()

	m.logger.Info("codedb indexing complete",
		"blobs_parsed", stats.BlobsParsed,
		"symbols_extracted", stats.SymbolsExtracted,
	)

	return &CodeIndexResult{
		BlobsParsed:      stats.BlobsParsed,
		SymbolsExtracted: stats.SymbolsExtracted,
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

	dataDir := paths.CodeDBDataDir(m.projectRoot)
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
		_, err := m.Index(ctx, CodeIndexPayload{}, nil)
		if err != nil {
			if isInitial {
				m.logger.Warn("codedb initial index failed", "error", err)
			} else {
				m.logger.Debug("codedb freshness check failed", "error", err)
			}
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

	dataDir := paths.CodeDBDataDir(m.projectRoot)
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
