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

// requirePOSIXPermissions skips a test that models read-only media with POSIX
// tooling. chmod is a no-op for root, and neither chmod nor cp behaves this way
// on Windows — a test relying on either would silently assert nothing there.
// Call it before the first such command, not merely before the chmod.
func requirePOSIXPermissions(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("chmod/cp do not model read-only media on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod does not deny writes")
	}
}

// requireUnwritableMedia makes dir and everything under it unwritable, or skips.
func requireUnwritableMedia(t *testing.T, dir string) {
	t.Helper()
	requirePOSIXPermissions(t)
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
	// Before the cp below, not just before the chmod inside
	// requireUnwritableMedia — cp is not a Windows executable.
	requirePOSIXPermissions(t)

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

// A store built by an older ox lacks tables and columns the current search path
// queries. The writable open repairs that with CreateSchema; a read-only one
// cannot, so it must refuse rather than answer some queries and fail others
// with "no such column".
func TestOpenReadOnlyStoreRejectsStaleSchema(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Roll the store back to a pre-migration shape.
	if _, err := s.Exec(`ALTER TABLE symbols DROP COLUMN signature`); err != nil {
		t.Fatalf("drop symbols.signature: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	requireUnwritableMedia(t, root)

	got, err := Open(root)
	if err == nil {
		_ = got.Close()
		t.Fatal("Open succeeded on a read-only store needing migration; want refusal")
	}
	if !errors.Is(err, ErrReadOnly) {
		t.Errorf("error = %v, want ErrReadOnly", err)
	}
	if errors.Is(err, ErrCorrupt) {
		t.Errorf("error = %v, must not be reported as corruption", err)
	}
	if !strings.Contains(err.Error(), "signature") {
		t.Errorf("error = %v, want it to name the missing object", err)
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

// The read-only DSN is a file: URI. Both of these are silent-wrong-database
// bugs: an unescaped space truncates the path, and a path rendered without the
// third slash puts its first segment in the URI's authority — which is what a
// Windows `C:\...` path does.
func TestReadOnlyDSNIsAnAbsoluteFileURI(t *testing.T) {
	for _, p := range []string{"/tmp/x/metadata.db", `C:\Users\a\metadata.db`, "C:/Users/a/metadata.db"} {
		dsn := readOnlyDSN(p, true)
		if !strings.HasPrefix(dsn, "file:///") {
			t.Errorf("readOnlyDSN(%q) = %q, want a file:/// URI (authority must stay empty)", p, dsn)
		}
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

// SQLite can only read a WAL database without write access when BOTH sidecars
// are present and readable. With the `-shm` gone the store is unreadable here,
// and the error must say that rather than blame corruption — the immutable=1
// fallback that would "work" reads past the WAL and answers from a partial
// database.
func TestOpenReadOnlyStoreRefusesWALWithoutSharedMemory(t *testing.T) {
	requirePOSIXPermissions(t)

	live := t.TempDir()
	s, err := Open(live)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.Exec(`INSERT INTO repos (name, path) VALUES ('demo', '/tmp/demo')`); err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	snap := t.TempDir()
	if out, err := exec.Command("cp", "-R", live+"/.", snap).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v: %s", err, out)
	}
	_ = s.Close()

	if fi, err := os.Stat(filepath.Join(snap, MetadataDBFile+"-wal")); err != nil || fi.Size() == 0 {
		t.Skipf("snapshot has no un-checkpointed WAL to test against (err=%v)", err)
	}
	if err := os.Remove(filepath.Join(snap, MetadataDBFile+"-shm")); err != nil {
		t.Fatalf("remove -shm: %v", err)
	}
	requireUnwritableMedia(t, snap)

	got, err := Open(snap)
	if err == nil {
		_ = got.Close()
		t.Fatal("Open succeeded on a WAL store with no -shm; want refusal")
	}
	if !errors.Is(err, ErrReadOnly) {
		t.Errorf("error = %v, want ErrReadOnly", err)
	}
	if errors.Is(err, ErrCorrupt) {
		t.Errorf("error = %v, must not be reported as corruption", err)
	}
}

// A read-only store whose bleve sub-index is gone cannot be repaired: the
// writable path would nuke and recreate it, which needs a writable directory.
// It must report that, not return a store whose searches dereference nil.
func TestOpenReadOnlyStoreReportsMissingBleveSubIndex(t *testing.T) {
	root := t.TempDir()
	seedStore(t, root)
	if err := os.RemoveAll(filepath.Join(root, "bleve", "code")); err != nil {
		t.Fatalf("remove bleve/code: %v", err)
	}
	requireUnwritableMedia(t, root)

	got, err := Open(root)
	if err == nil {
		_ = got.Close()
		t.Fatal("Open succeeded with no bleve code sub-index; want refusal")
	}
	if !errors.Is(err, ErrReadOnly) {
		t.Errorf("error = %v, want ErrReadOnly", err)
	}
	if !strings.Contains(err.Error(), "code") {
		t.Errorf("error = %v, want it to name the sub-index", err)
	}
}

// A read-only file that is not a SQLite database at all is still not ox's to
// delete. The verdict must be "I cannot read this", never "this is corrupt" —
// only the latter reaches removeSQLiteFiles.
func TestOpenReadOnlyStoreRejectsNonDatabaseFile(t *testing.T) {
	root := t.TempDir()
	seedStore(t, root)
	if err := os.WriteFile(filepath.Join(root, MetadataDBFile), []byte("this is not a database"), 0o600); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	requireUnwritableMedia(t, root)

	got, err := Open(root)
	if err == nil {
		_ = got.Close()
		t.Fatal("Open succeeded on a read-only non-database file; want refusal")
	}
	if !errors.Is(err, ErrReadOnly) {
		t.Errorf("error = %v, want ErrReadOnly", err)
	}
	if errors.Is(err, ErrCorrupt) {
		t.Errorf("error = %v, must not be reported as corruption", err)
	}
}

// The database file is read-only but its directory is not, so SQLite can create
// the WAL sidecars and integrity_check passes — the store looks fine until a
// migration tries to write. This is the one shape where the corruption remedy
// COULD delete the file, which is what makes the survival assertion here mean
// something: the failure is a refusal, not a deletion.
func TestOpenReadOnlyDatabaseFileInWritableDirIsNotDeleted(t *testing.T) {
	requirePOSIXPermissions(t)

	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.Exec(`ALTER TABLE symbols DROP COLUMN signature`); err != nil {
		t.Fatalf("drop symbols.signature: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	dbPath := filepath.Join(root, MetadataDBFile)
	if err := os.Chmod(dbPath, 0o444); err != nil {
		t.Fatalf("chmod db: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dbPath, 0o644) })

	got, err := Open(root)
	if err == nil {
		_ = got.Close()
		t.Fatal("Open succeeded with an unwritable database needing migration; want refusal")
	}
	if errors.Is(err, ErrCorrupt) {
		t.Errorf("error = %v, must not be reported as corruption", err)
	}
	if _, statErr := os.Stat(dbPath); statErr != nil {
		t.Errorf("metadata.db was deleted from a writable directory: %v", statErr)
	}
}
