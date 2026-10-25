package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// damageBTreePage overwrites the b-tree header of page number pageNo (1-based)
// so any statement that reads that page fails with SQLITE_CORRUPT, while the
// database header and every other page stay intact. Page 1 is special: the
// b-tree header sits after the 100-byte database header.
func damageBTreePage(t *testing.T, dbPath string, pageNo int) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open for page size: %v", err)
	}
	var pageSize int
	if err := db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		t.Fatalf("page_size: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	offset := int64(pageNo-1) * int64(pageSize)
	if pageNo == 1 {
		offset += 100
	}
	f, err := os.OpenFile(dbPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open db file: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteAt([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, offset); err != nil {
		t.Fatalf("damage page %d: %v", pageNo, err)
	}
}

// damageTableRoot damages the root page of a table.
func damageTableRoot(t *testing.T, dbPath, table string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open for rootpage: %v", err)
	}
	var root int
	if err := db.QueryRow("SELECT rootpage FROM sqlite_master WHERE name = ?", table).Scan(&root); err != nil {
		t.Fatalf("rootpage of %s: %v", table, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	damageBTreePage(t, dbPath, root)
}

// newClosedStore creates a store under a fresh root and closes it, returning
// the root and the database path, so a test can damage the file before reopening.
func newClosedStore(t *testing.T) (root, dbPath string) {
	t.Helper()
	root = t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return root, filepath.Join(root, MetadataDBFile)
}

// Open used to run a full integrity scan, which is what cost minutes on a
// multi-GB index every time the daemon opened it. Damage the scan WOULD have
// found but that no open-time statement reads must not fail Open: that is the
// only timing-independent proof that Open no longer scans, and the same file
// must still be reported damaged by the check that is supposed to find it.
//
// Failure prevented: every daemon pass re-reads the whole index just to open it.
func TestOpen_DoesNotScanTheDatabase(t *testing.T) {
	root, dbPath := newClosedStore(t)
	damageTableRoot(t, dbPath, "pr_comments")

	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open failed on damage that only a page scan can see: %v", err)
	}
	defer func() { _ = s.Close() }()
	if _, statErr := os.Stat(dbPath); statErr != nil {
		t.Fatalf("Open removed a database it never found damaged: %v", statErr)
	}

	// the damage is real: the scan Open skips does see it
	if err := s.CheckIntegrityContext(context.Background()); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("CheckIntegrityContext = %v, want ErrCorrupt (the fixture must actually be damaged)", err)
	}
}

// What the removed scan caught at open must still surface as ErrCorrupt from
// Open, because ErrCorrupt (and the removal behind it) is what licenses the
// callers' delete-and-rebuild.
func TestOpen_DamageStillYieldsErrCorruptAndRemovesTheDatabase(t *testing.T) {
	tests := []struct {
		name   string
		damage func(t *testing.T, dbPath string)
	}{
		{
			name: "garbage header",
			damage: func(t *testing.T, dbPath string) {
				garbage := make([]byte, 4096)
				if _, err := rand.Read(garbage); err != nil {
					t.Fatalf("rand: %v", err)
				}
				if err := os.WriteFile(dbPath, garbage, 0o600); err != nil {
					t.Fatalf("write garbage: %v", err)
				}
			},
		},
		{
			// the file header is intact but the schema's own b-tree page is not
			name: "damaged schema page",
			damage: func(t *testing.T, dbPath string) {
				damageBTreePage(t, dbPath, 1)
			},
		},
		{
			// header and schema page are fine, so the header probe passes; the
			// schema migration is the first statement to read this index page
			name: "damaged page first read by the migration",
			damage: func(t *testing.T, dbPath string) {
				damageTableRoot(t, dbPath, "sqlite_autoindex_github_file_mtimes_1")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, dbPath := newClosedStore(t)
			tt.damage(t, dbPath)

			_, err := Open(root)
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Open = %v, want ErrCorrupt", err)
			}
			if errors.Is(err, ErrIntegrityUnknown) {
				t.Errorf("Open = %v, damage must not be reported as unknown", err)
			}
			if _, statErr := os.Stat(dbPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("damaged database still on disk after ErrCorrupt (stat err = %v)", statErr)
			}

			// the heal: the next open starts from a clean database
			s, err := Open(root)
			if err != nil {
				t.Fatalf("Open after removal: %v", err)
			}
			_ = s.Close()
		})
	}
}

// An I/O-class failure names this process or this moment, not the data, so it
// must never carry the verdict that destroys the index (#875). Here the path
// where metadata.db belongs is a directory: SQLite cannot open it, and Open
// must report that without removing anything.
func TestOpen_IOFailureIsNotCorruption(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, MetadataDBFile)
	if err := os.MkdirAll(dbPath, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	marker := filepath.Join(dbPath, "keep")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	_, err := Open(root)
	if err == nil {
		t.Fatal("Open succeeded with a directory in place of the database")
	}
	if errors.Is(err, ErrCorrupt) {
		t.Errorf("Open = %v, an unopenable path must not be a corruption verdict", err)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Errorf("Open removed what it could not read: %v", statErr)
	}
}

// quick_check is bound to the caller's context so daemon shutdown and the
// maintenance deadline interrupt a multi-minute page walk. A canceled check
// has said nothing about the data: it is ErrIntegrityUnknown, and must never
// be ErrCorrupt.
//
// Failure prevented: shutdown blocks behind an uninterruptible scan.
func TestCheckSQLiteIntegrity_HonorsCanceledContext(t *testing.T) {
	s := openStore(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := checkSQLiteIntegrity(ctx, s.db)
	if err == nil {
		t.Fatal("a canceled context did not stop the check")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to wrap context.Canceled", err)
	}
	if !errors.Is(err, ErrIntegrityUnknown) {
		t.Errorf("error = %v, want ErrIntegrityUnknown", err)
	}
	if errors.Is(err, ErrCorrupt) {
		t.Errorf("error = %v, a canceled check must not be a corruption verdict", err)
	}

	if err := s.CheckIntegrityContext(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("CheckIntegrityContext = %v, want it to wrap context.Canceled", err)
	}
}

// Which failures count as damage is the whole safety argument for deleting an
// index on a statement's say-so, so it is pinned against real SQLite errors.
func TestIsSQLiteDamage(t *testing.T) {
	// real errors, produced by SQLite rather than constructed
	notADBErr := execErr(t, writeNotADB(t), "PRAGMA schema_version")

	_, dbPath := newClosedStore(t)
	damageBTreePage(t, dbPath, 1)
	corruptErr := execErr(t, dbPath, "SELECT COUNT(*) FROM sqlite_master")

	cantOpenErr := execErr(t, filepath.Join(t.TempDir(), "no-such-dir", "x.db"), "PRAGMA schema_version")

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"not a database", notADBErr, true},
		{"malformed image", corruptErr, true},
		{"wrapped damage", fmt.Errorf("index commit: %w", corruptErr), true},
		{"cannot open file", cantOpenErr, false},
		{"plain error", errors.New("disk image is malformed"), false},
		{"canceled context", context.Canceled, false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSQLiteDamage(tt.err); got != tt.want {
				t.Errorf("IsSQLiteDamage(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func writeNotADB(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "notadb.db")
	if err := os.WriteFile(p, []byte("this is plainly not a sqlite database file, just text padding padding"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

// execErr runs query against the database at path and returns the error it
// provokes.
func execErr(t *testing.T, path, query string) error {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	var out any
	return db.QueryRow(query).Scan(&out)
}
