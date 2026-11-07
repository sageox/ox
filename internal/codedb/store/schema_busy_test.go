package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// migrationBusyDB returns a V1 database that needs migrating and a second
// connection holding the write lock with an uncommitted row. busy_timeout(0)
// turns any attempt to take that lock into an immediate SQLITE_BUSY.
func migrationBusyDB(t *testing.T) (*sql.DB, *sql.Tx) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "migration.db")
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(0)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, func() error { _, err := db.Exec(baseSchemaV1); return err }())
	_, err = db.Exec(`CREATE TABLE comments(id INTEGER PRIMARY KEY)`)
	require.NoError(t, err)
	return db, holdWriteLock(t, path)
}

// holdWriteLock opens another connection to path and leaves it inside a write
// transaction, the way the daemon's indexer holds the codedb.
func holdWriteLock(t *testing.T, path string) *sql.Tx {
	t.Helper()
	writer, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(0)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = writer.Close() })
	tx, err := writer.Begin()
	require.NoError(t, err)
	_, err = tx.Exec(`INSERT INTO repos(name, path) VALUES ('preserved', '/example')`)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

// An open of an up-to-date codedb must not take the write lock: the daemon's
// indexer holds it for long stretches, and every `ox code` command opens the
// store. Failure prevented: CreateSchema always beginning a write transaction,
// so opens queue behind the indexer and fail once busy_timeout runs out.
func TestCreateSchema_CurrentSchemaNeedsNoWriteLock(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "current.db")
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(0)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, CreateSchema(db))

	holdWriteLock(t, path)
	require.NoError(t, CreateSchema(db), "a current schema must open while another connection holds the write lock")
}

// A migration that cannot get the write lock leaves the database exactly as it
// was, and the next open applies it. Failure prevented: a locked-out open
// leaving a partly migrated schema, or one that later opens cannot finish.
func TestCreateSchema_LockedOutMigrationChangesNothing(t *testing.T) {
	t.Parallel()
	db, writer := migrationBusyDB(t)
	before := snapshotSchema(t, db)

	err := CreateSchema(db)
	require.Error(t, err)
	require.True(t, IsSQLiteBusy(err), "want the held write lock to refuse the migration, got %v", err)
	require.Equal(t, before, snapshotSchema(t, db), "a migration that could not take the write lock must change nothing")

	require.NoError(t, writer.Commit())
	require.NoError(t, CreateSchema(db), "a later open must be able to apply the migration")
	require.True(t, columnExists(t, db, "blobs", "comments_parsed"))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM repos WHERE name='preserved'`).Scan(&count))
	require.Equal(t, 1, count)
}
