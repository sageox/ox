-- name: InsertWhisper :exec
INSERT OR IGNORE INTO whispers
    (id, scope, type, source, topic, content, importance, created_at,
     agent_id, principal_id, principal_type, team_id, metadata)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetCursor :one
SELECT last_seen FROM cursors WHERE agent_id = ?;

-- name: UpsertCursor :exec
INSERT INTO cursors (agent_id, last_seen, updated_at) VALUES (?, ?, ?)
ON CONFLICT(agent_id) DO UPDATE SET last_seen = excluded.last_seen, updated_at = excluded.updated_at;

-- name: RemoveCursor :exec
DELETE FROM cursors WHERE agent_id = ?;

-- name: IsRelayed :one
SELECT COUNT(*) FROM relayed_murmurs WHERE murmur_id = ? AND scope = ?;

-- name: MarkRelayed :exec
INSERT OR IGNORE INTO relayed_murmurs (murmur_id, scope, relayed_at) VALUES (?, ?, ?);

-- name: PruneWhispers :execresult
DELETE FROM whispers WHERE created_at < ?;

-- name: PruneRelayed :execresult
DELETE FROM relayed_murmurs WHERE relayed_at < ?;

-- name: PruneCursors :execresult
DELETE FROM cursors WHERE updated_at < ?;

-- name: DeleteAllWhispers :exec
DELETE FROM whispers;

-- name: DeleteAllRelayed :exec
DELETE FROM relayed_murmurs;
