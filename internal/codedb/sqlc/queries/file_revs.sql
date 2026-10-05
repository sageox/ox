-- name: InsertFileRev :exec
INSERT OR IGNORE INTO file_revs (commit_id, path, blob_id) VALUES (?, ?, ?);

-- name: DeleteFileRevsByCommit :execrows
DELETE FROM file_revs WHERE commit_id = ?;

-- name: ListDeadSnapshotCommitsByRepo :many
-- Commits of one repo that still own file_revs rows although no ref points at
-- them. Search reaches file_revs only through a refs join, so such a snapshot is
-- unreachable weight. The EXISTS probe rides idx_file_revs_commit: cost scales
-- with commits, not with file_revs rows.
SELECT c.id FROM commits c
WHERE c.repo_id = ?
  AND c.id NOT IN (SELECT r.commit_id FROM refs r)
  AND EXISTS (SELECT 1 FROM file_revs fr WHERE fr.commit_id = c.id)
ORDER BY c.id
LIMIT ?;

-- name: ListDeadSnapshotCommits :many
-- Same as ListDeadSnapshotCommitsByRepo, across every repo, for index-wide maintenance.
SELECT c.id FROM commits c
WHERE c.id NOT IN (SELECT r.commit_id FROM refs r)
  AND EXISTS (SELECT 1 FROM file_revs fr WHERE fr.commit_id = c.id)
ORDER BY c.id
LIMIT ?;
