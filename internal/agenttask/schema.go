package agenttask

import (
	"database/sql"
	_ "embed"
)

//go:embed schema.sql
var schemaDDL string

// CreateSchema initializes the task table and indexes. Idempotent.
func CreateSchema(db *sql.DB) error {
	_, err := db.Exec(schemaDDL)
	return err
}
