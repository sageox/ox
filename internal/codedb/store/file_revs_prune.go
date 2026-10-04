package store

import (
	"context"
	"database/sql"
	"fmt"

	codedbsqlc "github.com/sageox/ox/internal/codedb/sqlc"
)

// unlimitedSnapshots is SQLite's "no limit" LIMIT value.
const unlimitedSnapshots int64 = -1

// DeadSnapshotPrune reports what a dead-snapshot prune removed.
type DeadSnapshotPrune struct {
	Snapshots int   // commits whose file_revs rows were removed
	Rows      int64 // file_revs rows removed
}

// PruneDeadFileRevsForRepo removes the file_revs snapshots of one repo's
// commits that no ref points at, at most maxSnapshots of them.
//
// file_revs holds one full tree snapshot per ref tip. buildTipFileRevs writes
// the new tip's snapshot but nothing used to remove the snapshot the ref moved
// away from, so every ledger build left ~123k rows behind and a long-lived
// index grew to tens of millions. Search reaches file_revs only through a join
// on refs.commit_id, so those rows are unreachable; they also keep old blobs
// "referenced", which stops orphan pruning from ever freeing them.
//
// q is bound to the caller's transaction when called from the indexer, so the
// prune commits or rolls back together with the tip it follows. maxSnapshots
// bounds the work done inside that one transaction; the leftover backlog of a
// legacy index is drained by Store.Maintain instead.
func PruneDeadFileRevsForRepo(ctx context.Context, q *codedbsqlc.Queries, repoID, maxSnapshots int64) (DeadSnapshotPrune, error) {
	commitIDs, err := q.ListDeadSnapshotCommitsByRepo(ctx, codedbsqlc.ListDeadSnapshotCommitsByRepoParams{
		RepoID: repoID,
		Limit:  maxSnapshots,
	})
	if err != nil {
		return DeadSnapshotPrune{}, fmt.Errorf("list dead snapshots: %w", err)
	}
	return deleteSnapshots(ctx, q, commitIDs)
}

// bulkPruneSnapshots is the backlog size from which deleting dead snapshots one
// by one is abandoned for rebuilding file_revs from the live snapshots. A
// snapshot's rows are scattered across idx_file_revs_blob, so every per-snapshot
// DELETE rewrites most of that index: on a 5.6 GB ledger index (394 snapshots,
// 34M rows) that was ~40 minutes of write-locked churn, against ~12 seconds for
// the rebuild. Steady state never reaches the threshold (a ref move leaves one
// dead snapshot, pruned by the build that moved it); only a legacy index does.
const bulkPruneSnapshots = 16

// pruneDeadFileRevs removes every dead snapshot in the index, across all repos.
// A small backlog is deleted snapshot by snapshot (each its own autocommit
// statement); a large one is rebuilt in a single transaction. Either way the
// work honors ctx and leaves the live snapshots exactly as they were.
func (s *Store) pruneDeadFileRevs(ctx context.Context) (DeadSnapshotPrune, error) {
	commitIDs, err := s.queries.ListDeadSnapshotCommits(ctx, unlimitedSnapshots)
	if err != nil {
		return DeadSnapshotPrune{}, fmt.Errorf("list dead snapshots: %w", err)
	}
	if len(commitIDs) >= bulkPruneSnapshots {
		return s.rebuildFileRevsFromRefs(ctx, len(commitIDs))
	}
	return deleteSnapshots(ctx, s.queries, commitIDs)
}

// rebuildFileRevsFromRefs replaces file_revs with just the rows of commits a ref
// points at, in one transaction: stash the live rows in a temp table, empty
// file_revs, put them back.
//
// Emptying a table with no WHERE clause is a truncation — its pages go straight
// to the freelist without a per-row delete — but SQLite only truncates when no
// foreign key can be involved, and file_revs is a child of commits and blobs. So
// FK enforcement is switched off for this one connection. That is safe because
// the transaction can only delete child rows and re-insert rows whose parents
// are untouched, and the write lock is taken first (BEGIN IMMEDIATE) so no ref
// can move between choosing the live set and replacing the table. The connection
// also leaves temp_store=MEMORY for the duration: the stash is a full tree
// snapshot per live ref and belongs on disk, not in RAM.
func (s *Store) rebuildFileRevsFromRefs(ctx context.Context, deadSnapshots int) (DeadSnapshotPrune, error) {
	var pruned DeadSnapshotPrune
	err := s.withConn(ctx, []pragmaOverride{noForeignKeys, fileTempStore}, func(conn *sql.Conn) error {
		return inImmediateTx(ctx, conn, func() error {
			if _, err := conn.ExecContext(ctx, `CREATE TEMP TABLE live_file_revs AS
				SELECT id, commit_id, path, blob_id FROM file_revs
				WHERE commit_id IN (SELECT commit_id FROM refs) ORDER BY id`); err != nil {
				return fmt.Errorf("stash live file_revs: %w", err)
			}
			cleared, err := conn.ExecContext(ctx, `DELETE FROM file_revs`)
			if err != nil {
				return fmt.Errorf("empty file_revs: %w", err)
			}
			removed, err := cleared.RowsAffected()
			if err != nil {
				return fmt.Errorf("count removed file_revs: %w", err)
			}
			restored, err := conn.ExecContext(ctx, `INSERT INTO file_revs (id, commit_id, path, blob_id)
				SELECT id, commit_id, path, blob_id FROM live_file_revs ORDER BY id`)
			if err != nil {
				return fmt.Errorf("restore live file_revs: %w", err)
			}
			kept, err := restored.RowsAffected()
			if err != nil {
				return fmt.Errorf("count restored file_revs: %w", err)
			}
			if _, err := conn.ExecContext(ctx, `DROP TABLE live_file_revs`); err != nil {
				return fmt.Errorf("drop stash: %w", err)
			}
			pruned = DeadSnapshotPrune{Snapshots: deadSnapshots, Rows: removed - kept}
			return nil
		})
	})
	if err != nil {
		return DeadSnapshotPrune{}, err
	}
	return pruned, nil
}

func deleteSnapshots(ctx context.Context, q *codedbsqlc.Queries, commitIDs []int64) (DeadSnapshotPrune, error) {
	var pruned DeadSnapshotPrune
	for _, id := range commitIDs {
		if err := ctx.Err(); err != nil {
			return pruned, err
		}
		rows, err := q.DeleteFileRevsByCommit(ctx, id)
		if err != nil {
			return pruned, fmt.Errorf("delete file_revs of commit %d: %w", id, err)
		}
		pruned.Snapshots++
		pruned.Rows += rows
	}
	return pruned, nil
}
