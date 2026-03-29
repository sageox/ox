-- name: UpsertRepo :exec
INSERT INTO repos (name, path) VALUES (?, ?)
ON CONFLICT(name) DO UPDATE SET path = excluded.path;

-- name: GetRepoIDByName :one
SELECT id FROM repos WHERE name = ?;

-- name: ListRepoPaths :many
SELECT path FROM repos;

-- name: ListCommitHashesByRepo :many
SELECT hash FROM commits WHERE repo_id = ?;

-- name: ListRefsByRepo :many
SELECT r.name, c.hash FROM refs r JOIN commits c ON r.commit_id = c.id WHERE r.repo_id = ?;
