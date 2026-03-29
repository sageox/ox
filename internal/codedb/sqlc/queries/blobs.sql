-- name: GetBlobIDByHash :one
SELECT id FROM blobs WHERE content_hash = ?;

-- name: InsertBlob :exec
INSERT OR IGNORE INTO blobs (content_hash, language) VALUES (?, ?);

-- name: MarkBlobParsed :exec
UPDATE blobs SET parsed = 1 WHERE id = ?;

-- name: MarkBlobCommentsParsed :exec
UPDATE blobs SET comments_parsed = 1 WHERE id = ?;
