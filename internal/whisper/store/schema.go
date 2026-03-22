package store

import "database/sql"

const schemaDDL = `
CREATE TABLE IF NOT EXISTS whispers (
    id              TEXT PRIMARY KEY,
    scope           TEXT NOT NULL,
    type            TEXT NOT NULL,
    source          TEXT NOT NULL,
    topic           TEXT NOT NULL,
    content         TEXT NOT NULL,
    importance      TEXT NOT NULL,
    created_at      TEXT NOT NULL,
    agent_id        TEXT,
    principal_id    TEXT,               -- who the agent works for (optional)
    principal_type  TEXT,               -- "human" for now (optional, extensible)
    team_id         TEXT,
    metadata        TEXT
);

CREATE INDEX IF NOT EXISTS idx_whispers_created ON whispers(created_at);
CREATE INDEX IF NOT EXISTS idx_whispers_scope ON whispers(scope, created_at);
CREATE INDEX IF NOT EXISTS idx_whispers_importance ON whispers(importance, created_at);
CREATE INDEX IF NOT EXISTS idx_whispers_topic ON whispers(topic, created_at);

CREATE TABLE IF NOT EXISTS cursors (
    agent_id    TEXT PRIMARY KEY,
    last_seen   TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS relayed_murmurs (
    murmur_id   TEXT NOT NULL,
    scope       TEXT NOT NULL,
    relayed_at  TEXT NOT NULL,
    PRIMARY KEY (murmur_id, scope)
);
CREATE INDEX IF NOT EXISTS idx_relayed_scope ON relayed_murmurs(scope, relayed_at);
`

// CreateSchema initializes the whisper store tables and indexes.
func CreateSchema(db *sql.DB) error {
	_, err := db.Exec(schemaDDL)
	return err
}
