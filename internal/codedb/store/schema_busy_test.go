package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func migrationBusyDB(t *testing.T) (*sql.DB, *sql.Tx) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "migration.db")
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(0)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, func() error { _, err := db.Exec(baseSchemaV1); return err }())
	_, err = db.Exec(`CREATE TABLE comments(id INTEGER PRIMARY KEY)`)
	require.NoError(t, err)
	writer, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(0)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = writer.Close() })
	tx, err := writer.Begin()
	require.NoError(t, err)
	_, err = tx.Exec(`INSERT INTO repos(name, path) VALUES ('preserved', '/example')`)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	return db, tx
}

func TestAddColumnMigration_RetriesRealBusy(t *testing.T) {
	db, writer := migrationBusyDB(t)
	original := testMigrationBusyHook
	t.Cleanup(func() { testMigrationBusyHook = original })
	busyCount := 0
	testMigrationBusyHook = func(err error) {
		require.True(t, IsSQLiteBusy(err), "the first ALTER must actually encounter SQLite's write lock")
		busyCount++
		require.NoError(t, writer.Commit())
	}
	require.NoError(t, migrateAddComments(db))
	require.Equal(t, 1, busyCount)
	require.True(t, columnExists(t, db, "blobs", "comments_parsed"))
	var name string
	require.NoError(t, db.QueryRow(`SELECT name FROM repos`).Scan(&name))
	require.Equal(t, "preserved", name)
}

func TestAddColumnMigration_BusyExhaustionPreservesDatabase(t *testing.T) {
	db, writer := migrationBusyDB(t)
	original := testMigrationBusyHook
	t.Cleanup(func() { testMigrationBusyHook = original })
	busyCount := 0
	testMigrationBusyHook = func(err error) {
		require.True(t, IsSQLiteBusy(err))
		busyCount++
	}
	err := migrateAddComments(db)
	require.Error(t, err)
	require.True(t, IsSQLiteBusy(err))
	require.Equal(t, schemaBusyAttempts-1, busyCount)
	require.False(t, columnExists(t, db, "blobs", "comments_parsed"))
	require.NoError(t, writer.Commit())
	require.NoError(t, migrateAddComments(db), "a later open must be able to resume the migration")
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM repos WHERE name='preserved'`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestAddColumnMigration_DoesNotRetrySchemaErrors(t *testing.T) {
	db, _ := createOldSchemaDB(t)
	t.Cleanup(func() { _ = db.Close() })
	original := testMigrationBusyHook
	t.Cleanup(func() { testMigrationBusyHook = original })
	testMigrationBusyHook = func(err error) { t.Fatalf("non-busy error entered retry: %v", err) }
	_, err := addColumnsIfMissing(db, "blobs", []columnDDL{{"invalid", `ALTER TABLE blobs ADD COLUMN invalid INVALID SYNTAX !`}})
	require.Error(t, err)
	require.False(t, IsSQLiteBusy(err))
	require.False(t, columnExists(t, db, "blobs", "invalid"))
}

func TestAddColumnMigration_AdoptsConcurrentAlterAfterBusy(t *testing.T) {
	db, writer := migrationBusyDB(t)
	original := testMigrationBusyHook
	t.Cleanup(func() { testMigrationBusyHook = original })
	busyCount := 0
	testMigrationBusyHook = func(err error) {
		require.True(t, IsSQLiteBusy(err))
		busyCount++
		_, err = writer.Exec(`ALTER TABLE blobs ADD COLUMN comments_parsed INTEGER NOT NULL DEFAULT 0`)
		require.NoError(t, err)
		require.NoError(t, writer.Commit())
	}
	require.NoError(t, migrateAddComments(db), "the completed concurrent ALTER must be adopted")
	require.Equal(t, 1, busyCount)
	require.True(t, columnExists(t, db, "blobs", "comments_parsed"))
}
