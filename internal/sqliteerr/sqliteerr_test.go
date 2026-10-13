package sqliteerr

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// TestClassify: a held lock's error, as SQLite returns it, is busy and never
// damage; an error from outside SQLite is neither. Failure prevented: a store
// takes a busy open for corruption and deletes a database other connections
// are using (#1339). codedb/store's TestIsSQLiteDamage pins the damage codes
// through IsSQLiteDamage.
func TestClassify(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locked.db")
	holder, err := sql.Open("sqlite", path)
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
	reader, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	_, busyErr := reader.Exec("SELECT COUNT(*) FROM t")

	tests := []struct {
		name         string
		err          error
		damage, busy bool
	}{
		{"busy", busyErr, false, true},
		{"not from SQLite", context.Canceled, false, false},
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
