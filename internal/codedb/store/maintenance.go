package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// vacuumFreelistPercent is the share of the database file that must be free
// pages before VACUUM is worth its cost. VACUUM rewrites every live page under
// an exclusive lock (33 minutes observed on a 1.7 GB index), so it only runs
// when it will hand back a quarter of the file.
const vacuumFreelistPercent = 25

// MaintenanceResult captures what happened during codedb maintenance.
type MaintenanceResult struct {
	OrphanBlobsPruned int64
	OldDiffsPruned    int64
	StaleSymbolsCount int64
	// FileRevsPruned counts rows of file_revs snapshots that no ref points at.
	FileRevsPruned int64
	Vacuumed       bool
	// IntegrityOK is true when the database is not known to be damaged: either
	// quick_check passed this run or it passed within the last day.
	IntegrityOK bool
	// IntegrityChecked is true when quick_check actually ran this run. It is
	// false when the daily gate skipped it.
	IntegrityChecked bool
	// IntegrityErr is the failed check's verdict when IntegrityOK is false:
	// ErrCorrupt when the check found damage, ErrIntegrityUnknown when it could
	// not run (including cancellation).
	IntegrityErr error
	SizeBefore   int64
	SizeAfter    int64
	Duration     time.Duration
}

// TotalPruned returns the total number of rows removed.
func (r MaintenanceResult) TotalPruned() int64 {
	return r.OrphanBlobsPruned + r.OldDiffsPruned + r.StaleSymbolsCount + r.FileRevsPruned
}

// DBPath returns the path to the SQLite database file.
func (s *Store) DBPath() string {
	return filepath.Join(s.Root, MetadataDBFile)
}

// Maintain runs cleanup: verifies the database (at most daily), prunes dead
// file_revs snapshots, orphaned blobs and old diffs, and vacuums when the file
// is a quarter free pages (at most daily). Safe to call while the store is in
// use — all operations use transactions. Every step honors ctx.
func (s *Store) Maintain(ctx context.Context) MaintenanceResult {
	return s.maintain(ctx, time.Now())
}

// maintain is Maintain with the clock injected, so the 24h gates are testable.
func (s *Store) maintain(ctx context.Context, now time.Time) (result MaintenanceResult) {
	start := time.Now()
	result.SizeBefore = -1
	result.SizeAfter = -1

	// measure size before
	if info, err := os.Stat(s.DBPath()); err == nil {
		result.SizeBefore = info.Size()
	}
	defer func() {
		if info, err := os.Stat(s.DBPath()); err == nil {
			result.SizeAfter = info.Size()
		}
		result.Duration = time.Since(start)
	}()

	statePath := maintStatePath(s.DBPath())
	state := loadMaintState(statePath)

	// A full page scan: at most once a day per database, and interruptible.
	if state.verifyDue(now) {
		if err := checkSQLiteIntegrity(ctx, s.db); err != nil {
			result.IntegrityErr = err
			if ctx.Err() == nil {
				slog.Warn("codedb integrity check failed during maintenance", "error", err)
			}
			return result
		}
		result.IntegrityChecked = true
		state.LastVerified = now
		saveMaintState(statePath, state)
	}
	result.IntegrityOK = true

	// snapshots no ref points at: unreachable by search, and they keep every
	// old blob "referenced" so the orphan pruning below could never free it.
	// Runs on indexes that see no new build for a while, which the prune inside
	// the index build cannot reach.
	if ctx.Err() != nil {
		return result
	}
	pruned, err := s.pruneDeadFileRevs(ctx)
	result.FileRevsPruned = pruned.Rows
	if err != nil && ctx.Err() == nil {
		slog.Warn("codedb maintenance: failed to prune dead file_revs snapshots", "error", err)
	}

	if ctx.Err() != nil {
		return result
	}
	symbolsPruned, blobsPruned, err := s.pruneOrphanBlobs(ctx)
	if err != nil {
		slog.Warn("codedb maintenance: failed to prune orphan blobs", "error", err)
	}
	result.StaleSymbolsCount = symbolsPruned
	result.OrphanBlobsPruned = blobsPruned

	// prune old diffs: diffs for commits older than 90 days that aren't
	// referenced by any ref (stale history)
	if ctx.Err() != nil {
		return result
	}
	cutoff := now.Add(-90 * 24 * time.Hour).Unix()
	res, err := s.db.ExecContext(ctx, `DELETE FROM diffs WHERE commit_id IN (
		SELECT c.id FROM commits c
		WHERE c.timestamp < ?
		AND c.id NOT IN (SELECT commit_id FROM refs)
	)`, cutoff)
	if err != nil {
		slog.Warn("codedb maintenance: failed to prune old diffs", "error", err)
	} else {
		result.OldDiffsPruned, _ = res.RowsAffected()
	}

	// What was pruned above only helps the file once VACUUM hands the freed
	// pages back, so the trigger is the free-page ratio, not the row count: a
	// prune that frees nothing worth rewriting the file for must not pay for it.
	if ctx.Err() != nil {
		return result
	}
	result.Vacuumed = s.vacuumIfDue(ctx, now, &state, statePath)
	return result
}

// pruneOrphanBlobs removes blobs that no snapshot and no diff references,
// together with everything derived from them (symbols, symbol_refs,
// symbol_edges, comments), as one transaction.
//
// Two properties matter. First, "orphan" excludes blobs a diff references: a diff
// needs its blob's content hash to be rendered, and on a ledger index every
// historical blob is one — the old definition (merely "not in file_revs")
// selected ~45k blobs there, every one of which then failed the diffs foreign
// key at the end of the statement. Second, it is all-or-nothing and removes a
// blob row only together with its symbols: half a prune would leave a blob row
// marked parsed whose symbols are gone, and the parser skips parsed blobs, so a
// ref that later re-exposed it would serve it without symbols.
//
// Foreign keys are off for the job. With them on, every deleted blob makes
// SQLite scan each child table that has no index on its blob columns (diffs,
// symbol_edges) — tens of thousands of blobs times hundreds of thousands of
// rows, minutes of CPU. That is safe here because the orphan set is chosen
// against every table that references blobs, the dependents are deleted first,
// and the transaction holds the write lock from the start.
func (s *Store) pruneOrphanBlobs(ctx context.Context) (symbols, blobs int64, err error) {
	const orphan = `(SELECT id FROM orphan_blobs)`
	const orphanSymbols = `(SELECT id FROM symbols WHERE blob_id IN ` + orphan + `)`

	err = s.withConn(ctx, []pragmaOverride{noForeignKeys}, func(conn *sql.Conn) error {
		return inImmediateTx(ctx, conn, func() error {
			if _, err := conn.ExecContext(ctx, `CREATE TEMP TABLE orphan_blobs AS
				SELECT id FROM blobs
				WHERE id NOT IN (SELECT blob_id FROM file_revs)
				  AND id NOT IN (SELECT new_blob_id FROM diffs WHERE new_blob_id IS NOT NULL)
				  AND id NOT IN (SELECT old_blob_id FROM diffs WHERE old_blob_id IS NOT NULL)`); err != nil {
				return fmt.Errorf("select orphan blobs: %w", err)
			}
			// an edge from a live blob into an orphan stays, as an unresolved
			// reference (dst_name keeps name lookups working)
			steps := []struct {
				what  string
				query string
			}{
				{"unresolve edges into orphans", `UPDATE symbol_edges SET dst_blob_id = NULL, dst_symbol_id = NULL
					WHERE dst_blob_id IN ` + orphan + ` OR dst_symbol_id IN ` + orphanSymbols},
				{"delete edges from orphans", `DELETE FROM symbol_edges
					WHERE src_blob_id IN ` + orphan + ` OR src_symbol_id IN ` + orphanSymbols},
				{"delete orphan symbol_refs", `DELETE FROM symbol_refs WHERE blob_id IN ` + orphan},
				{"delete orphan comments", `DELETE FROM comments WHERE blob_id IN ` + orphan},
			}
			for _, step := range steps {
				if _, err := conn.ExecContext(ctx, step.query); err != nil {
					return fmt.Errorf("%s: %w", step.what, err)
				}
			}
			res, err := conn.ExecContext(ctx, `DELETE FROM symbols WHERE blob_id IN `+orphan)
			if err != nil {
				return fmt.Errorf("delete orphan symbols: %w", err)
			}
			symbols, _ = res.RowsAffected()
			res, err = conn.ExecContext(ctx, `DELETE FROM blobs WHERE id IN `+orphan)
			if err != nil {
				return fmt.Errorf("delete orphan blobs: %w", err)
			}
			blobs, _ = res.RowsAffected()
			if _, err := conn.ExecContext(ctx, `DROP TABLE orphan_blobs`); err != nil {
				return fmt.Errorf("drop orphan set: %w", err)
			}
			return nil
		})
	})
	if err != nil {
		return 0, 0, err
	}
	return symbols, blobs, nil
}

// vacuumIfDue vacuums when at least vacuumFreelistPercent of the file is free
// pages and the last vacuum is over a day old. Reports whether it vacuumed.
func (s *Store) vacuumIfDue(ctx context.Context, now time.Time, state *maintState, statePath string) bool {
	if !state.vacuumDue(now) {
		return false
	}
	var pages, free int64
	if err := s.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		slog.Warn("codedb maintenance: read page_count failed", "error", err)
		return false
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&free); err != nil {
		slog.Warn("codedb maintenance: read freelist_count failed", "error", err)
		return false
	}
	if !vacuumWorthIt(pages, free) {
		return false
	}

	slog.Info("codedb maintenance: vacuuming", "free_pages", free, "pages", pages)
	if err := s.vacuum(ctx); err != nil {
		if ctx.Err() == nil {
			slog.Warn("codedb maintenance: vacuum failed", "error", err)
		}
		return false
	}
	state.LastVacuum = now
	saveMaintState(statePath, *state)

	// In WAL mode the vacuumed pages land in the WAL and the main file keeps its
	// old size until a checkpoint copies them back and truncates it.
	var busy, logFrames, checkpointed int64
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil && ctx.Err() == nil {
		slog.Warn("codedb maintenance: wal checkpoint after vacuum failed", "error", err)
	}
	return true
}

// vacuumWorthIt reports whether free pages are at least vacuumFreelistPercent of
// the file: freelist_count * 100 / page_count >= 25, without integer truncation.
func vacuumWorthIt(pages, free int64) bool {
	return pages > 0 && free*100 >= pages*vacuumFreelistPercent
}

// vacuum runs VACUUM on a dedicated connection with a file-backed temp store: on
// a pool connection (temp_store=MEMORY) a multi-GB index would be rebuilt
// entirely in RAM.
func (s *Store) vacuum(ctx context.Context) error {
	return s.withConn(ctx, []pragmaOverride{fileTempStore}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "VACUUM")
		return err
	})
}

// saveMaintState persists st; a failure only means the next run re-does a
// gated job, so it is logged rather than failing maintenance.
func saveMaintState(path string, st maintState) {
	if err := st.save(path); err != nil {
		slog.Warn("codedb maintenance: failed to record maintenance state", "path", path, "error", err)
	}
}
