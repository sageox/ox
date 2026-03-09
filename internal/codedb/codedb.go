package codedb

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/sageox/ox/internal/codedb/index"
	"github.com/sageox/ox/internal/codedb/search"
	"github.com/sageox/ox/internal/codedb/store"
)

// DB is the top-level CodeDB facade.
type DB struct {
	store *store.Store
}

// Open opens (or creates) a CodeDB at the given root directory.
func Open(root string) (*DB, error) {
	s, err := store.Open(root)
	if err != nil {
		return nil, fmt.Errorf("open codedb store: %w", err)
	}
	return &DB{store: s}, nil
}

// Close releases all resources.
func (db *DB) Close() error {
	return db.store.Close()
}

// Store returns the underlying store for direct access.
func (db *DB) Store() *store.Store {
	return db.store
}

// IndexRepo clones/fetches and indexes a git repository.
func (db *DB) IndexRepo(ctx context.Context, url string, opts index.IndexOptions) error {
	return index.IndexRepo(ctx, db.store, url, opts)
}

// IndexLocalRepo indexes a local git repository in-place, including dirty working tree files.
func (db *DB) IndexLocalRepo(ctx context.Context, localPath string, opts index.IndexOptions) error {
	return index.IndexLocalRepo(ctx, db.store, localPath, opts)
}

// ParseSymbols extracts symbols from all unparsed blobs with supported languages.
func (db *DB) ParseSymbols(ctx context.Context, progress func(string)) (index.ParseStats, error) {
	return index.ParseSymbols(ctx, db.store, index.ProgressFunc(progress))
}

// Search parses and executes a Sourcegraph-style query.
func (db *DB) Search(ctx context.Context, input string) ([]search.Result, error) {
	query, err := search.ParseQuery(input)
	if err != nil {
		return nil, fmt.Errorf("parse query: %w", err)
	}
	return search.Execute(ctx, db.store, query)
}

// TranslateQuery parses a query and returns the generated SQL without executing.
func (db *DB) TranslateQuery(input string) (*search.TranslatedQuery, error) {
	query, err := search.ParseQuery(input)
	if err != nil {
		return nil, fmt.Errorf("parse query: %w", err)
	}
	return search.Translate(query)
}

// RawSQL executes a raw SQL query and returns results as column-value pairs.
func (db *DB) RawSQL(query string) ([]string, [][]string, error) {
	rows, err := db.store.Query(query)
	if err != nil {
		return nil, nil, fmt.Errorf("execute sql: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, fmt.Errorf("get columns: %w", err)
	}

	var results [][]string
	for rows.Next() {
		values := make([]sql.NullString, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		row := make([]string, len(cols))
		for i, v := range values {
			if v.Valid {
				row[i] = v.String
			} else {
				row[i] = "NULL"
			}
		}
		results = append(results, row)
	}

	return cols, results, nil
}
