package store

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/blevesearch/bleve/v2"
)

// requireUnwritableMedia makes dir and everything under it unwritable, or skips.
// chmod is a no-op for root and does not model a read-only filesystem on
// Windows, so a test relying on it would silently assert nothing there.
func requireUnwritableMedia(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("chmod does not make a directory unwritable on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod does not deny writes")
	}
	if out, err := exec.Command("chmod", "-R", "a-w", dir).CombinedOutput(); err != nil {
		t.Fatalf("chmod -R a-w %s: %v: %s", dir, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("chmod", "-R", "u+w", dir).Run() })

	if f, err := os.CreateTemp(dir, ".probe-*"); err == nil {
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
		t.Skipf("%s is still writable after chmod -R a-w", dir)
	}
}

// seedStore builds a store holding one repo row and one indexed code document,
// then closes it so SQLite checkpoints and removes the WAL.
func seedStore(t *testing.T, root string) {
	t.Helper()
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.Exec(`INSERT INTO repos (name, path) VALUES ('demo', '/tmp/demo')`); err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	if err := s.CodeIndex.Index("doc1", map[string]any{"content": "func createRepoWorkspace() {}"}); err != nil {
		t.Fatalf("index doc: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// A store on read-only media is a legitimate state, not a damaged one: it must
// open, answer SQL and bleve queries, and above all still be on disk afterwards.
// The reported failure (#871) was PRAGMA integrity_check reading
// SQLITE_READONLY_DIRECTORY as corruption and deleting the index over it.
func TestOpenReadOnlyStoreServesReadsWithoutDeleting(t *testing.T) {
	root := t.TempDir()
	seedStore(t, root)
	requireUnwritableMedia(t, root)

	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open on read-only media: %v", err)
	}
	defer s.Close()

	if !s.ReadOnly {
		t.Error("Store.ReadOnly = false, want true")
	}

	var repos int
	if err := s.QueryRow(`SELECT COUNT(*) FROM repos`).Scan(&repos); err != nil {
		t.Fatalf("count repos: %v", err)
	}
	if repos != 1 {
		t.Errorf("repos = %d, want 1", repos)
	}

	res, err := s.CodeIndex.Search(bleve.NewSearchRequest(bleve.NewMatchQuery("createRepoWorkspace")))
	if err != nil {
		t.Fatalf("bleve search: %v", err)
	}
	if res.Total != 1 {
		t.Errorf("bleve hits = %d, want 1", res.Total)
	}
}

// A store captured mid-write holds committed rows — up to and including the
// whole schema — only in its WAL sidecar, and immutable=1 would skip past them.
// Reading it must either see the WAL (SQLite's sidecar-present read-only mode)
// or refuse; what it must never do is answer confidently from the main file
// alone, which is the same fluent-empty-answer failure as #871 itself.
func TestOpenReadOnlyStoreHonorsUncheckpointedWAL(t *testing.T) {
	live := t.TempDir()
	s, err := Open(live)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.Exec(`INSERT INTO repos (name, path) VALUES ('demo', '/tmp/demo')`); err != nil {
		t.Fatalf("insert repo: %v", err)
	}

	// Copy while the connection is open, so the snapshot keeps the WAL sidecar.
	snap := t.TempDir()
	if out, err := exec.Command("cp", "-R", live+"/.", snap).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v: %s", err, out)
	}
	_ = s.Close()

	if fi, err := os.Stat(filepath.Join(snap, MetadataDBFile+"-wal")); err != nil || fi.Size() == 0 {
		t.Skipf("snapshot has no un-checkpointed WAL to test against (err=%v)", err)
	}
	requireUnwritableMedia(t, snap)

	got, err := Open(snap)
	if err != nil {
		if !errors.Is(err, ErrReadOnly) {
			t.Errorf("error = %v, want ErrReadOnly", err)
		}
		if errors.Is(err, ErrCorrupt) {
			t.Errorf("error = %v, must not be reported as corruption", err)
		}
	} else {
		defer got.Close()
		if !got.ReadOnly {
			t.Error("Store.ReadOnly = false, want true")
		}
		// The row lives only in the WAL. Seeing zero here means immutable=1
		// read past it and the store is answering from half a database.
		var repos int
		if err := got.QueryRow(`SELECT COUNT(*) FROM repos`).Scan(&repos); err != nil {
			t.Fatalf("count repos: %v", err)
		}
		if repos != 1 {
			t.Errorf("repos = %d, want 1 — the WAL was skipped", repos)
		}
	}
	// Backstop for the corruption remedy itself. On read-only media the
	// removal fails anyway, so the load-bearing assertion is the ErrCorrupt
	// check above; this catches a future path that deletes from a writable
	// parent instead.
	if _, statErr := os.Stat(filepath.Join(snap, MetadataDBFile)); statErr != nil {
		t.Errorf("metadata.db missing after read-only open: %v", statErr)
	}
}

// Guards the choice made in openSQLiteReadOnly: immutable=1 is what lets a
// cleanly closed store be read at all, and it is exactly what must be dropped
// when a WAL sidecar is present, because immutable ignores the WAL.
func TestReadOnlyDSNImmutableOnlyWhenAsked(t *testing.T) {
	if !strings.Contains(readOnlyDSN("/tmp/metadata.db", true), "immutable=1") {
		t.Error("immutable DSN is missing immutable=1")
	}
	if strings.Contains(readOnlyDSN("/tmp/metadata.db", false), "immutable") {
		t.Error("WAL-present DSN sets immutable, which would skip the WAL")
	}
}

// OpenSQLOnly shares openSQLite with Open, so it must report the same state —
// `ox code insights` and the status counters go through it.
func TestOpenSQLOnlyReportsReadOnly(t *testing.T) {
	root := t.TempDir()
	seedStore(t, root)
	requireUnwritableMedia(t, root)

	s, err := OpenSQLOnly(root)
	if err != nil {
		t.Fatalf("OpenSQLOnly on read-only media: %v", err)
	}
	defer s.Close()

	if !s.ReadOnly {
		t.Error("Store.ReadOnly = false, want true")
	}
	var repos int
	if err := s.QueryRow(`SELECT COUNT(*) FROM repos`).Scan(&repos); err != nil {
		t.Fatalf("count repos: %v", err)
	}
	if repos != 1 {
		t.Errorf("repos = %d, want 1", repos)
	}
}

// A read-only store missing its schema must say so, not hand back a handle that
// fails "no such table" on every later query.
func TestOpenReadOnlyStoreRejectsMissingSchema(t *testing.T) {
	root := t.TempDir()
	seedStore(t, root)
	// Replace metadata.db with an empty (valid, schema-less) SQLite file.
	dbPath := filepath.Join(root, MetadataDBFile)
	if err := os.WriteFile(dbPath, nil, 0o600); err != nil {
		t.Fatalf("truncate db: %v", err)
	}
	requireUnwritableMedia(t, root)

	got, err := Open(root)
	if err == nil {
		_ = got.Close()
		t.Fatal("Open succeeded on a schema-less read-only store; want refusal")
	}
	if !errors.Is(err, ErrReadOnly) {
		t.Errorf("error = %v, want ErrReadOnly", err)
	}
}

// The read-only DSN is a file: URI, so an unescaped path would be truncated at
// the first space — silently opening a different (nonexistent) database.
func TestReadOnlyDSNEscapesPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "dir with space")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seedStore(t, root)

	dsn := readOnlyDSN(filepath.Join(root, MetadataDBFile), true)
	if strings.Contains(dsn, "dir with space") {
		t.Errorf("DSN %q leaves the space unescaped", dsn)
	}

	requireUnwritableMedia(t, root)
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open on read-only media with a spaced path: %v", err)
	}
	defer s.Close()
	var repos int
	if err := s.QueryRow(`SELECT COUNT(*) FROM repos`).Scan(&repos); err != nil {
		t.Fatalf("count repos: %v", err)
	}
	if repos != 1 {
		t.Errorf("repos = %d, want 1", repos)
	}
}

// storeIsWritable is the gate that keeps "cannot write" from reaching the
// corruption verdict, so both of its answers need to be true.
func TestStoreIsWritable(t *testing.T) {
	root := t.TempDir()
	if !storeIsWritable(root) {
		t.Fatal("storeIsWritable = false on a fresh temp dir, want true")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("probe left %d entries behind: %v", len(entries), entries)
	}

	requireUnwritableMedia(t, root)
	if storeIsWritable(root) {
		t.Error("storeIsWritable = true on unwritable media, want false")
	}
}
