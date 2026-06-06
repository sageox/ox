package agenttask

import (
	"database/sql"
	_ "embed"
	"fmt"
)

//go:embed schema.sql
var schemaDDL string

// SchemaVersion is the on-disk schema generation. There is no migration tool:
// the queue is ephemeral (gitignored, rebuildable), so a schema change just
// bumps this constant — NewStore detects a mismatched PRAGMA user_version on an
// existing DB and recreates it from scratch rather than migrating. Durable
// stores (e.g. internal/codedb) hand-write Go migrations; this one does not
// need to because it carries no data worth preserving across a schema change.
const SchemaVersion = 1

// CreateSchema initializes the task table and indexes and stamps the schema
// version. Idempotent.
func CreateSchema(db *sql.DB) error {
	if _, err := db.Exec(schemaDDL); err != nil {
		return err
	}
	_, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion))
	return err
}
