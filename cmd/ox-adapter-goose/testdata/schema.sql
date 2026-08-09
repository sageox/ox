-- Real Goose sessions.db DDL, captured with:
--   sqlite3 -readonly ~/.local/share/goose/sessions/sessions.db ".schema"
-- on 2026-08-09. Only the "sessions" and "messages" tables are reproduced
-- here (the tables this adapter reads); the on-disk database also has
-- schema_version and provider_inventory_* tables that are unrelated to
-- session reading and are omitted for brevity.
--
-- CAVEAT: the goose CLI is not installed on the machine this was captured
-- from, and the source database was last written 2026-06-02 (about two
-- months before capture). block/goose is a Rust project; this schema is
-- confirmed against that one on-disk database, not independently verified
-- against the current crates/goose/src/session/session_manager.rs. Treat
-- this as "proven against one real artifact," not "proven against upstream
-- goose HEAD."
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    user_set_name BOOLEAN DEFAULT FALSE,
    session_type TEXT NOT NULL DEFAULT 'user',
    working_dir TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    extension_data TEXT DEFAULT '{}',
    total_tokens INTEGER,
    input_tokens INTEGER,
    output_tokens INTEGER,
    accumulated_total_tokens INTEGER,
    accumulated_input_tokens INTEGER,
    accumulated_output_tokens INTEGER,
    accumulated_cost REAL,
    schedule_id TEXT,
    recipe_json TEXT,
    user_recipe_values_json TEXT,
    provider_name TEXT,
    model_config_json TEXT,
    goose_mode TEXT NOT NULL DEFAULT 'auto',
    archived_at TIMESTAMP,
    project_id TEXT
);
CREATE TABLE messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id TEXT,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    role TEXT NOT NULL,
    content_json TEXT NOT NULL,
    created_timestamp INTEGER NOT NULL,
    timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    tokens INTEGER,
    metadata_json TEXT
);
CREATE INDEX idx_messages_session ON messages(session_id);
CREATE INDEX idx_messages_timestamp ON messages(timestamp);
CREATE INDEX idx_messages_message_id ON messages(message_id);
CREATE INDEX idx_sessions_updated ON sessions(updated_at DESC);
CREATE INDEX idx_sessions_type ON sessions(session_type);
