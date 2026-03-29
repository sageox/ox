-- Complete schema including all migrations applied.
-- This is the sqlc source of truth for code generation.
-- Runtime schema creation + migrations remain in store/schema.go.

CREATE TABLE IF NOT EXISTS repos (
    id   INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    path TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS commits (
    id        INTEGER PRIMARY KEY,
    repo_id   INTEGER NOT NULL REFERENCES repos(id),
    hash      TEXT NOT NULL UNIQUE,
    author    TEXT,
    message   TEXT,
    timestamp INTEGER
);

CREATE TABLE IF NOT EXISTS commit_parents (
    commit_id INTEGER NOT NULL REFERENCES commits(id),
    parent_id INTEGER NOT NULL REFERENCES commits(id),
    PRIMARY KEY (commit_id, parent_id)
);

CREATE TABLE IF NOT EXISTS refs (
    id        INTEGER PRIMARY KEY,
    repo_id   INTEGER NOT NULL REFERENCES repos(id),
    name      TEXT NOT NULL,
    commit_id INTEGER NOT NULL REFERENCES commits(id),
    UNIQUE(repo_id, name)
);

CREATE TABLE IF NOT EXISTS blobs (
    id               INTEGER PRIMARY KEY,
    content_hash     TEXT NOT NULL UNIQUE,
    language         TEXT,
    parsed           INTEGER NOT NULL DEFAULT 0,
    comments_parsed  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS file_revs (
    id        INTEGER PRIMARY KEY,
    commit_id INTEGER NOT NULL REFERENCES commits(id),
    path      TEXT NOT NULL,
    blob_id   INTEGER NOT NULL REFERENCES blobs(id),
    UNIQUE(commit_id, path)
);

CREATE TABLE IF NOT EXISTS diffs (
    id          INTEGER PRIMARY KEY,
    commit_id   INTEGER NOT NULL REFERENCES commits(id),
    path        TEXT NOT NULL,
    old_blob_id INTEGER REFERENCES blobs(id),
    new_blob_id INTEGER REFERENCES blobs(id),
    UNIQUE(commit_id, path)
);

CREATE TABLE IF NOT EXISTS symbols (
    id          INTEGER PRIMARY KEY,
    blob_id     INTEGER NOT NULL REFERENCES blobs(id),
    parent_id   INTEGER REFERENCES symbols(id),
    name        TEXT NOT NULL,
    kind        TEXT NOT NULL,
    line        INTEGER NOT NULL,
    col         INTEGER NOT NULL,
    end_line    INTEGER,
    end_col     INTEGER,
    signature   TEXT,
    return_type TEXT,
    params      TEXT
);

CREATE TABLE IF NOT EXISTS symbol_refs (
    id        INTEGER PRIMARY KEY,
    blob_id   INTEGER NOT NULL REFERENCES blobs(id),
    symbol_id INTEGER REFERENCES symbols(id),
    ref_name  TEXT NOT NULL,
    kind      TEXT NOT NULL,
    line      INTEGER NOT NULL,
    col       INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS comments (
    id       INTEGER PRIMARY KEY,
    blob_id  INTEGER NOT NULL REFERENCES blobs(id),
    text     TEXT NOT NULL,
    kind     TEXT NOT NULL,
    line     INTEGER NOT NULL,
    end_line INTEGER NOT NULL,
    col      INTEGER NOT NULL,
    end_col  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS pull_requests (
    id           INTEGER PRIMARY KEY,
    number       INTEGER NOT NULL UNIQUE,
    title        TEXT NOT NULL,
    body         TEXT,
    author       TEXT,
    state        TEXT NOT NULL,
    labels       TEXT,
    created_at   INTEGER,
    merged_at    INTEGER,
    closed_at    INTEGER,
    updated_at   INTEGER,
    merge_commit TEXT,
    url          TEXT,
    source_path  TEXT
);

CREATE TABLE IF NOT EXISTS pr_comments (
    id         INTEGER PRIMARY KEY,
    pr_id      INTEGER NOT NULL REFERENCES pull_requests(id),
    author     TEXT,
    body       TEXT,
    path       TEXT,
    line       INTEGER,
    created_at INTEGER
);

CREATE TABLE IF NOT EXISTS pr_commits (
    id    INTEGER PRIMARY KEY,
    pr_id INTEGER NOT NULL REFERENCES pull_requests(id),
    sha   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS issues (
    id          INTEGER PRIMARY KEY,
    number      INTEGER NOT NULL UNIQUE,
    title       TEXT NOT NULL,
    body        TEXT,
    author      TEXT,
    state       TEXT NOT NULL,
    labels      TEXT,
    created_at  INTEGER,
    closed_at   INTEGER,
    updated_at  INTEGER,
    url         TEXT,
    source_path TEXT
);

CREATE TABLE IF NOT EXISTS issue_comments (
    id         INTEGER PRIMARY KEY,
    issue_id   INTEGER NOT NULL REFERENCES issues(id),
    author     TEXT,
    body       TEXT,
    created_at INTEGER
);

CREATE TABLE IF NOT EXISTS github_file_mtimes (
    source_path TEXT NOT NULL PRIMARY KEY,
    mtime_unix  INTEGER NOT NULL
);
