package store

import (
	"database/sql"
	_ "embed"
)

//go:embed sqlc/schema.sql
var schemaDDL string

// CreateSchema initializes the whisper store tables and indexes.
func CreateSchema(db *sql.DB) error {
	_, err := db.Exec(schemaDDL)
	return err
}
