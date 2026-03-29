-- name: InsertCommit :exec
INSERT OR IGNORE INTO commits (repo_id, hash, author, message, timestamp)
VALUES (?, ?, ?, ?, ?);

-- name: GetCommitIDByHash :one
SELECT id FROM commits WHERE hash = ?;

-- name: InsertCommitParent :exec
INSERT OR IGNORE INTO commit_parents (commit_id, parent_id) VALUES (?, ?);
