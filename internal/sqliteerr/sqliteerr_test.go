package sqliteerr

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// execErr runs one statement against a fresh handle on dsn and returns its error.
func execErr(t *testing.T, dsn, query string) error {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	defer db.Close()
	_, err = db.Exec(query)
	return err
}

// Whether a failure counts as damage decides whether a store may delete its
// files, so the classifier is pinned against errors SQLite actually returns.
// Failure prevented: a busy open reads as corruption and a store deletes a
// database other connections are using (#1339).
func TestClassify(t *testing.T) {
	notADB := filepath.Join(t.TempDir(), "notadb.db")
	if err := os.WriteFile(notADB, []byte("this is plainly not a sqlite database file, just text padding padding"), 0o600); err != nil {
		t.Fatal(err)
	}
	damaged := execErr(t, notADB, "PRAGMA schema_version")

	locked := filepath.Join(t.TempDir(), "locked.db")
	holder, err := sql.Open("sqlite", locked)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	conn, err := holder.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for _, q := range []string{"CREATE TABLE t (x)", "BEGIN EXCLUSIVE"} {
		if _, err := conn.ExecContext(t.Context(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	busy := execErr(t, locked+"?_pragma=busy_timeout(1)", "SELECT COUNT(*) FROM t")

	tests := []struct {
		name         string
		err          error
		damage, busy bool
	}{
		{"not a database", damaged, true, false},
		{"wrapped damage", fmt.Errorf("open: %w", damaged), true, false},
		{"busy", busy, false, true},
		{"wrapped busy", fmt.Errorf("open: %w", busy), false, true},
		{"the words alone", errors.New("database is locked"), false, false},
		{"canceled context", context.Canceled, false, false},
		{"nil", nil, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDamage(tt.err); got != tt.damage {
				t.Errorf("IsDamage(%v) = %v, want %v", tt.err, got, tt.damage)
			}
			if got := IsBusy(tt.err); got != tt.busy {
				t.Errorf("IsBusy(%v) = %v, want %v", tt.err, got, tt.busy)
			}
		})
	}
}
