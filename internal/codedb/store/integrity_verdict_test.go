package store

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// holdExclusiveLock parks an EXCLUSIVE transaction on root's database so a
// second connection's PRAGMA integrity_check comes back SQLITE_BUSY. The store
// is switched off WAL first: in WAL mode readers never block on a writer, which
// is exactly why this failure is rare and why it went unnoticed.
func holdExclusiveLock(t *testing.T, root string) {
	t.Helper()
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.Exec(`INSERT INTO repos (name, path) VALUES ('demo','/tmp/demo')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := s.Exec(`PRAGMA journal_mode=DELETE`); err != nil {
		t.Fatalf("journal_mode=DELETE: %v", err)
	}
	conn, err := s.db.Conn(t.Context())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.ExecContext(t.Context(), `BEGIN EXCLUSIVE`); err != nil {
		t.Fatalf("BEGIN EXCLUSIVE: %v", err)
	}
	t.Cleanup(func() { _, _ = conn.ExecContext(t.Context(), `ROLLBACK`) })
}

// A lock held by another connection says nothing about whether the data is
// intact, but it used to arrive as ErrCorrupt — the verdict that lets callers
// delete the index and `ox doctor --fix` remove the whole dataDir.
func TestCheckSQLiteIntegrity_BusyIsNotCorruption(t *testing.T) {
	root := t.TempDir()
	holdExclusiveLock(t, root)

	// busy_timeout(1) so the lock is reported rather than waited out.
	db, err := sql.Open("sqlite", filepath.Join(root, MetadataDBFile)+"?_pragma=busy_timeout(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Confirm the setup really produces SQLITE_BUSY by asking SQLite directly.
	// Deriving this from checkSQLiteIntegrity's own error would make the test
	// skip itself the moment a regression swallowed the underlying code — the
	// exact regression it exists to catch.
	var probe string
	rawErr := db.QueryRow("PRAGMA integrity_check").Scan(&probe)
	var sqErr *sqlite.Error
	if !errors.As(rawErr, &sqErr) || sqErr.Code()&0xff != sqlite3.SQLITE_BUSY {
		t.Fatalf("setup did not produce SQLITE_BUSY, got: %v", rawErr)
	}

	err = checkSQLiteIntegrity(db)
	if err == nil {
		t.Fatal("expected the locked check to fail")
	}
	if errors.Is(err, ErrCorrupt) {
		t.Errorf("a held lock was reported as corruption: %v", err)
	}
	if !errors.Is(err, ErrIntegrityUnknown) {
		t.Errorf("error = %v, want ErrIntegrityUnknown", err)
	}
}

// The counterpart: a check that RAN and found damage must still say so, or the
// self-heal that rebuilds a genuinely broken index never fires.
func TestCheckSQLiteIntegrity_DamageIsStillCorruption(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = s.Close()

	dbPath := filepath.Join(root, MetadataDBFile)
	garbage := make([]byte, 4096)
	if _, err := rand.Read(garbage); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if err := os.WriteFile(dbPath, garbage, 0o600); err != nil {
		t.Fatalf("write garbage: %v", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := checkSQLiteIntegrity(db); !errors.Is(err, ErrCorrupt) {
		t.Errorf("error = %v, want ErrCorrupt", err)
	}
}

// The decision that matters: an inconclusive check must not reach
// removeSQLiteFiles. The directory here is writable, so the deletion WOULD
// succeed — which is what makes the survival assertion mean something.
func TestOpenSQLite_InconclusiveCheckDoesNotDeleteTheIndex(t *testing.T) {
	requirePOSIXPermissions(t)

	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.Exec(`INSERT INTO repos (name, path) VALUES ('demo','/tmp/demo')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Unreadable database, writable directory: SQLite cannot open the file, so
	// integrity_check reports on this process's access, not on the data.
	dbPath := filepath.Join(root, MetadataDBFile)
	if err := os.Chmod(dbPath, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dbPath, 0o600) })

	db, _, err := openSQLite(root)
	if db != nil {
		_ = db.Close()
	}
	if err == nil {
		t.Fatal("openSQLite succeeded on an unreadable database; want an error")
	}
	if errors.Is(err, ErrCorrupt) {
		t.Errorf("error = %v, must not be a corruption verdict", err)
	}
	if _, statErr := os.Stat(dbPath); statErr != nil {
		t.Errorf("the index was deleted over a check that never ran: %v", statErr)
	}
}
