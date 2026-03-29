-- name: InsertDiff :exec
INSERT OR IGNORE INTO diffs (commit_id, path, old_blob_id, new_blob_id) VALUES (?, ?, ?, ?);

-- name: GetDiffIDByCommitPath :one
SELECT id FROM diffs WHERE commit_id = ? AND path = ?;
