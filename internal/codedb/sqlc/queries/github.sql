-- name: ListFileMtimes :many
SELECT source_path, mtime_unix FROM github_file_mtimes;

-- name: UpsertFileMtime :exec
INSERT OR REPLACE INTO github_file_mtimes (source_path, mtime_unix) VALUES (?, ?);

-- name: GetPRIDByNumber :one
SELECT id FROM pull_requests WHERE number = ?;

-- name: DeletePRCommentsByPR :exec
DELETE FROM pr_comments WHERE pr_id = ?;

-- name: DeletePRCommitsByPR :exec
DELETE FROM pr_commits WHERE pr_id = ?;

-- name: DeletePullRequest :exec
DELETE FROM pull_requests WHERE id = ?;

-- name: InsertPullRequest :execresult
INSERT INTO pull_requests
    (number, title, body, author, state, labels, created_at, merged_at, closed_at, updated_at, merge_commit, url, source_path)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: InsertPRComment :exec
INSERT INTO pr_comments (pr_id, author, body, path, line, created_at) VALUES (?, ?, ?, ?, ?, ?);

-- name: InsertPRCommit :exec
INSERT INTO pr_commits (pr_id, sha) VALUES (?, ?);

-- name: GetIssueIDByNumber :one
SELECT id FROM issues WHERE number = ?;

-- name: DeleteIssueCommentsByIssue :exec
DELETE FROM issue_comments WHERE issue_id = ?;

-- name: DeleteIssue :exec
DELETE FROM issues WHERE id = ?;

-- name: InsertIssue :execresult
INSERT INTO issues
    (number, title, body, author, state, labels, created_at, closed_at, updated_at, url, source_path)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: InsertIssueComment :exec
INSERT INTO issue_comments (issue_id, author, body, created_at) VALUES (?, ?, ?, ?);
