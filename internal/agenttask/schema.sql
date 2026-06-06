-- Agent task queue schema (embedded SQLite, pure-Go modernc driver).
--
-- Timestamps are stored as INTEGER unix-nanoseconds (0 = zero/unset) rather
-- than RFC3339 text so the reconcile/reclaim queries can compare deadlines
-- (lease_expires_at < now) with correct, monotonic integer ordering. RFC3339Nano
-- trims trailing fractional zeros, which breaks lexicographic time comparison.
CREATE TABLE IF NOT EXISTS tasks (
    id                  TEXT PRIMARY KEY,
    title               TEXT    NOT NULL,
    body                TEXT    NOT NULL DEFAULT '',
    kind                TEXT    NOT NULL DEFAULT '',
    priority            INTEGER NOT NULL DEFAULT 0,   -- lower = higher priority
    status              TEXT    NOT NULL,             -- ready | in_progress | completed | canceled
    source              TEXT    NOT NULL DEFAULT '',
    target_agent        TEXT    NOT NULL DEFAULT '',  -- normalized on write; '' = any agent
    dedup_key           TEXT    NOT NULL DEFAULT '',
    payload             TEXT    NOT NULL DEFAULT '',  -- JSON object, '' = none
    created_at          INTEGER NOT NULL,             -- unix nanos
    expires_at          INTEGER NOT NULL DEFAULT 0,   -- unix nanos; 0 = never
    claimed_by_agent_id TEXT    NOT NULL DEFAULT '',
    claimed_by_pid      INTEGER NOT NULL DEFAULT 0,   -- host-local liveness; 0 = none
    claimed_host        TEXT    NOT NULL DEFAULT '',  -- PID only meaningful on the same host
    claimed_at          INTEGER NOT NULL DEFAULT 0,
    lease_expires_at    INTEGER NOT NULL DEFAULT 0,   -- reverts to ready once past
    attempts            INTEGER NOT NULL DEFAULT 0,   -- incremented each (re)claim
    completed_at        INTEGER NOT NULL DEFAULT 0,
    result              TEXT    NOT NULL DEFAULT ''
);

-- Drives the claim path: highest-priority, oldest-first ready task per agent.
CREATE INDEX IF NOT EXISTS idx_tasks_ready ON tasks(status, priority, created_at);

-- DB-enforced dedup: at most one ACTIVE (non-terminal) task per dedup_key.
-- A concurrent producer racing the same key fails the INSERT instead of
-- double-enqueuing — the property the JSONL store could only approximate.
CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_dedup ON tasks(dedup_key)
    WHERE dedup_key != '' AND status NOT IN ('completed', 'canceled');
