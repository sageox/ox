package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/blevesearch/bleve/v2"
)

// ErrReadOnly reports that the codedb store lives on media this process cannot
// write. It is deliberately not ErrCorrupt: nothing about the store is damaged,
// so nothing about it is deleted, rebuilt, or self-healed. A read-only store
// that opens successfully serves reads and sets Store.ReadOnly; one that cannot
// be served read-only fails with this error explaining why.
var ErrReadOnly = errors.New("codedb index is read-only")

// storeIsWritable reports whether this process can create a file inside root.
//
// It exists to keep "cannot write" from being diagnosed as "corrupt". On
// read-only media, PRAGMA integrity_check fails with SQLITE_READONLY_DIRECTORY
// — WAL mode has to create the `-shm` wal-index before it can read anything —
// and at that call site the failure is indistinguishable from real corruption,
// whose remedy is deleting the database. So writability is settled before any
// corruption verdict is reached.
//
// Probing with a real file rather than reading mode bits is what makes the
// answer true for the cases that actually occur: a read-only bind mount, a
// container image layer, an ACL, or a chmod. It runs on every open of an
// existing store; a create-close-unlink is cheap next to the SQLite and bleve
// opens that follow it in the same call.
func storeIsWritable(root string) bool {
	f, err := os.CreateTemp(root, ".ox-write-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// fileIsWritable reports whether this process can open path for writing.
// Distinct from storeIsWritable, which asks about the directory: SQLite needs
// both, and a store can fail on either one alone.
func fileIsWritable(path string) bool {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// openSQLiteReadOnly opens metadata.db for reading on media this process cannot
// write. The store is in WAL mode, so which of SQLite's two read-only openings
// applies depends on whether an un-checkpointed WAL is sitting next to it.
//
//   - No WAL sidecar (the state a cleanly closed store is left in: SQLite
//     checkpoints and deletes it when the last connection closes) — the main
//     file is the whole database, and `immutable=1` reads it without creating
//     the `-shm` wal-index it would otherwise need. Plain `mode=ro` fails here
//     with SQLITE_READONLY_DIRECTORY.
//
//   - A WAL sidecar with content (a store copied or killed mid-write) — SQLite
//     reads a WAL database with no write access as long as both `-wal` and
//     `-shm` are present and readable, so `mode=ro` sees the whole database and
//     immutable=1 must NOT be used: it ignores the WAL, and the rows parked
//     there can be anything up to the entire schema. If that open fails,
//     refuse. A partial database answering queries fluently is the failure this
//     whole path exists to prevent.
func openSQLiteReadOnly(dbPath string) (*sql.DB, error) {
	immutable := true
	if fi, err := os.Stat(dbPath + "-wal"); err == nil && fi.Size() > 0 {
		immutable = false
	}

	db, err := sql.Open("sqlite", readOnlyDSN(dbPath, immutable))
	if err != nil {
		return nil, fmt.Errorf("%w: open sqlite: %w", ErrReadOnly, err)
	}

	// Prove the store answers a query before handing it back, so an unreadable
	// file, an absent schema, or a WAL whose `-shm` is missing surfaces here
	// instead of as "no such table" on every later query. PRAGMA
	// integrity_check is skipped: it costs O(database size) — 0.8s to 18s on
	// stores we already ship — and its only remedy is the deletion this path
	// exists to refuse.
	var name string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='commits'`).Scan(&name); err != nil {
		_ = db.Close()
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s has no codedb schema and cannot be initialized without write access", ErrReadOnly, dbPath)
		}
		if !immutable {
			return nil, fmt.Errorf("%w: %s holds un-checkpointed writes and its write-ahead log cannot be read here: %w", ErrReadOnly, dbPath, err)
		}
		return nil, fmt.Errorf("%w: %w", ErrReadOnly, err)
	}

	// A store built by an older ox is missing tables and columns the current
	// search path queries, and the writable open repairs that by running
	// CreateSchema on every open. Here it cannot, so a stale store would answer
	// some queries and fail others with "no such column".
	//
	// CreateSchema is reused as the check rather than a hand-listed set of
	// required objects: every statement in it is guarded (CREATE ... IF NOT
	// EXISTS, addColumnsIfMissing, the issue-474 sentinel), so against a
	// fully-migrated store it writes nothing and succeeds on a read-only
	// connection, and against a stale one it attempts the missing DDL and fails
	// naming the object. A parallel list would drift from the migrations the
	// first time someone adds one.
	if err := CreateSchema(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%w: %s needs a schema migration that cannot be applied without write access: %w", ErrReadOnly, dbPath, err)
	}
	return db, nil
}

// readOnlyDSN builds the SQLite URI for a read-only open. The `file:` scheme is
// what makes SQLite parse `immutable` at all, and the URI form means the path
// must be escaped — a database under a home directory containing a space would
// otherwise be truncated at the space. journal_mode, synchronous and
// foreign_keys are all write-side and are omitted.
//
// Windows needs both conversions below. ToSlash turns `C:\...` separators into
// the `/` SQLite's URI parser requires (a no-op on POSIX, where a backslash is
// an ordinary filename character and must stay one). The leading slash makes a
// drive-letter path absolute: without it `url.URL` renders `C:` as the URI's
// authority — `file://C:/...` — and the open fails.
func readOnlyDSN(dbPath string, immutable bool) string {
	uriPath := filepath.ToSlash(dbPath)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	dsn := u.String() + "?mode=ro&_pragma=cache_size(-65536)&_pragma=mmap_size(268435456)&_pragma=temp_store(MEMORY)"
	if immutable {
		dsn += "&immutable=1"
	}
	return dsn
}

// openBleveReadOnly opens one bleve sub-index with scorch's read_only config,
// which opens root.bolt O_RDONLY and creates no directory — the two things
// bleve.Open does that a read-only store cannot afford.
//
// Errors are returned as-is. The self-heal and mapping-upgrade paths that handle
// them on a writable store both nuke and recreate the sub-index directory, which
// is neither possible nor wanted here.
func openBleveReadOnly(path, name string) (bleve.Index, error) {
	idx, err := recoverBleveOpen(func() (bleve.Index, error) {
		return bleve.OpenUsing(path, map[string]interface{}{"read_only": true})
	})
	if err != nil {
		return nil, fmt.Errorf("%w: open bleve %s sub-index at %s: %w", ErrReadOnly, name, path, err)
	}
	return idx, nil
}

// recoverBleveOpen runs open, turning a panic into an error. bleve can panic on
// a structurally broken index rather than returning one — safeOpenBleve carries
// the same guard for the writable path. Factored out so the recovery contract is
// testable without a corrupt index on disk, the same reason runBleveProbe is
// separate from CheckIntegrity.
func recoverBleveOpen(open func() (bleve.Index, error)) (idx bleve.Index, err error) {
	defer func() {
		if r := recover(); r != nil {
			idx = nil
			err = fmt.Errorf("panic during open: %v", r)
		}
	}()
	return open()
}
