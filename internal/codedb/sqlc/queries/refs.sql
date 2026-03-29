-- name: UpsertRef :exec
INSERT INTO refs (repo_id, name, commit_id) VALUES (?, ?, ?)
ON CONFLICT(repo_id, name) DO UPDATE SET commit_id = excluded.commit_id;
