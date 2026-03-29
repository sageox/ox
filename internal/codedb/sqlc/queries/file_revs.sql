-- name: InsertFileRev :exec
INSERT OR IGNORE INTO file_revs (commit_id, path, blob_id) VALUES (?, ?, ?);

-- name: DeleteFileRevsByCommit :exec
DELETE FROM file_revs WHERE commit_id = ?;
