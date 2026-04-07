package store

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// MaintenanceResult captures what happened during codedb maintenance.
type MaintenanceResult struct {
	OrphanBlobsPruned int64
	OldDiffsPruned    int64
	StaleSymbolsCount int64
	Vacuumed          bool
	IntegrityOK       bool
	SizeBefore        int64
	SizeAfter         int64
	Duration          time.Duration
}

// TotalPruned returns the total number of rows removed.
func (r MaintenanceResult) TotalPruned() int64 {
	return r.OrphanBlobsPruned + r.OldDiffsPruned + r.StaleSymbolsCount
}

// DBPath returns the path to the SQLite database file.
func (s *Store) DBPath() string {
	return filepath.Join(s.Root, MetadataDBFile)
}

// Maintain runs cleanup: prunes orphaned blobs, old diffs, and vacuums.
// Safe to call while the store is in use — all operations use transactions.
func (s *Store) Maintain(ctx context.Context) MaintenanceResult {
	start := time.Now()
	result := MaintenanceResult{
		SizeBefore: -1,
		SizeAfter:  -1,
	}

	// measure size before
	if info, err := os.Stat(s.DBPath()); err == nil {
		result.SizeBefore = info.Size()
	}

	// integrity check
	if err := checkSQLiteIntegrity(s.db); err != nil {
		slog.Warn("codedb integrity check failed during maintenance", "error", err)
		result.IntegrityOK = false
		result.Duration = time.Since(start)
		return result
	}
	result.IntegrityOK = true

	// identify orphan blobs: blobs not referenced by any file_rev
	if ctx.Err() != nil {
		result.Duration = time.Since(start)
		return result
	}
	// prune symbol_refs and symbols for orphan blobs BEFORE deleting blobs
	// (symbols.blob_id has FK to blobs.id — must delete children first)
	const deleteOrphanSymbolRefs = `DELETE FROM symbol_refs WHERE blob_id IN (SELECT id FROM blobs WHERE id NOT IN (SELECT DISTINCT blob_id FROM file_revs))`
	const deleteOrphanSymbols = `DELETE FROM symbols WHERE blob_id IN (SELECT id FROM blobs WHERE id NOT IN (SELECT DISTINCT blob_id FROM file_revs))`

	if ctx.Err() != nil {
		result.Duration = time.Since(start)
		return result
	}
	if _, err := s.db.ExecContext(ctx, deleteOrphanSymbolRefs); err != nil {
		slog.Warn("codedb maintenance: failed to prune orphan symbol_refs", "error", err)
	}

	res, err := s.db.ExecContext(ctx, deleteOrphanSymbols)
	if err != nil {
		slog.Warn("codedb maintenance: failed to prune orphan symbols", "error", err)
	} else {
		result.StaleSymbolsCount, _ = res.RowsAffected()
	}

	// now delete orphan blobs (children already removed)
	if ctx.Err() != nil {
		result.Duration = time.Since(start)
		return result
	}
	res, err = s.db.ExecContext(ctx,
		`DELETE FROM blobs WHERE id NOT IN (SELECT DISTINCT blob_id FROM file_revs)`)
	if err != nil {
		slog.Warn("codedb maintenance: failed to prune orphan blobs", "error", err)
	} else {
		result.OrphanBlobsPruned, _ = res.RowsAffected()
	}

	// prune old diffs: diffs for commits older than 90 days that aren't
	// referenced by any ref (stale history)
	if ctx.Err() != nil {
		result.Duration = time.Since(start)
		return result
	}
	cutoff := time.Now().Add(-90 * 24 * time.Hour).Unix()
	res, err = s.db.ExecContext(ctx, `DELETE FROM diffs WHERE commit_id IN (
		SELECT c.id FROM commits c
		WHERE c.timestamp < ?
		AND c.id NOT IN (SELECT commit_id FROM refs)
	)`, cutoff)
	if err != nil {
		slog.Warn("codedb maintenance: failed to prune old diffs", "error", err)
	} else {
		result.OldDiffsPruned, _ = res.RowsAffected()
	}

	// vacuum if enough was deleted
	if ctx.Err() != nil {
		result.Duration = time.Since(start)
		return result
	}
	totalPruned := result.TotalPruned()
	if totalPruned > 100 {
		if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
			slog.Warn("codedb maintenance: vacuum failed", "error", err)
		} else {
			result.Vacuumed = true
		}
	}

	// measure size after
	if info, err := os.Stat(s.DBPath()); err == nil {
		result.SizeAfter = info.Size()
	}

	result.Duration = time.Since(start)
	return result
}
