package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	codedbsqlc "github.com/sageox/ox/internal/codedb/sqlc"
)

// This file holds the adversarial coverage for schema creation and migration:
// crash-at-every-step recovery, model-based convergence from any starting state,
// data-conservation under concurrent migration, and a stress loop. It sits above
// the targeted per-migration tests in migration_test.go — those prove a single
// fix; these prove the whole schema layer holds under fault and contention.

// openRawDB opens a bare SQLite connection with the exact production DSN that
// openSQLite (store.go) uses, without applying any schema. Multiple openRawDB
// handles to one path model independent ox openers racing the same codedb, so
// these tests must use the same connection config production does or they
// measure a contention profile ox never sees.
func rawDSN(dbPath string) string {
	return dbPath + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-65536)&_pragma=mmap_size(268435456)&_pragma=temp_store(MEMORY)"
}

func openRawDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", rawDSN(dbPath))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return db
}

// schemaShape is a comparable fingerprint of a codedb's structure: each table's
// column set and the full index set. Column and index lists are sorted (via SQL
// ORDER BY) so two shapes are equal iff the schemas are structurally identical,
// regardless of the order statements ran in.
type schemaShape struct {
	columns map[string][]string
	indexes []string
}

func snapshotSchema(t *testing.T, db *sql.DB) schemaShape {
	t.Helper()
	shape := schemaShape{columns: make(map[string][]string)}
	for _, table := range queryStrings(t, db,
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`) {
		shape.columns[table] = queryStrings(t, db, `SELECT name FROM pragma_table_info(?) ORDER BY name`, table)
	}
	shape.indexes = queryStrings(t, db,
		`SELECT name FROM sqlite_master WHERE type='index' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	return shape
}

func queryStrings(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func requireIntegrityOK(t *testing.T, db *sql.DB) {
	t.Helper()
	var result string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if result != "ok" {
		t.Errorf("integrity_check = %q, want ok", result)
	}
}

// goldenSchema is the reference shape: what a clean, single-threaded CreateSchema
// produces. Every chaos scenario below must converge to exactly this.
func goldenSchema(t *testing.T) schemaShape {
	t.Helper()
	db := openRawDB(t, filepath.Join(t.TempDir(), "golden.db"))
	defer func() { _ = db.Close() }()
	if err := CreateSchema(db); err != nil {
		t.Fatalf("golden CreateSchema: %v", err)
	}
	return snapshotSchema(t, db)
}

// runConcurrentCreateSchema fires `runners` independent openers at the codedb
// whose metadata database is dbPath, all at once, and reports any error. Each
// opens through openSQLite, the production open path: its header probe opens
// the first connection and absorbs the BUSY that openers converting a brand-new
// file to WAL can get, which busy_timeout does not cover, and then runs
// CreateSchema. Each opener has its own connection pool, as a separate ox
// process would, though openers in one process also take turns on schemaMu.
func runConcurrentCreateSchema(t *testing.T, dbPath string, runners int) {
	t.Helper()
	root := filepath.Dir(dbPath)
	var wg sync.WaitGroup
	errs := make([]error, runners)
	ready := make(chan struct{}, runners)
	begin := make(chan struct{})
	for i := range runners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ready <- struct{}{}
			<-begin // released together to maximize overlap
			db, _, err := openSQLite(root)
			if err != nil {
				errs[i] = err
				return
			}
			errs[i] = db.Close()
		}(i)
	}
	// Wait until every worker is parked before releasing, so the openers start
	// together instead of trickling in as their goroutines are scheduled.
	for range runners {
		<-ready
	}
	close(begin)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("runner %d open: %v", i, err)
		}
	}
}

// TestMigrations_ResumeFromEveryCrashPoint is the crash-at-every-step recovery
// proof for the ALTER-based migrations. Older ox ran each ALTER as its own
// transaction, so one killed mid-migration left the first k columns present and
// the rest absent. For every prefix k, the test recreates that post-crash state,
// re-runs the migration twice (proving convergence AND idempotency), and asserts
// every column landed.
//
// Failure prevented: a migration that short-circuits on a guard column and
// never completes from a partial state — the class of bug fixed for
// migrateAddTypeInfo. A single hand-picked partial state (see
// TestMigrateAddTypeInfo_ResumesPartialMigration) proves one point; this proves
// the whole prefix matrix for all three ALTER migrations.
func TestMigrations_ResumeFromEveryCrashPoint(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}

	type step struct{ column, ddl string }
	migrations := []struct {
		name  string
		run   func(context.Context, codedbsqlc.DBTX) error
		table string
		steps []step
	}{
		{"edge_version", migrateAddEdgeVersion, "blobs", []step{
			{"edge_version", `ALTER TABLE blobs ADD COLUMN edge_version INTEGER NOT NULL DEFAULT 0`},
		}},
		{"type_info", migrateAddTypeInfo, "symbols", []step{
			{"signature", `ALTER TABLE symbols ADD COLUMN signature TEXT`},
			{"return_type", `ALTER TABLE symbols ADD COLUMN return_type TEXT`},
			{"params", `ALTER TABLE symbols ADD COLUMN params TEXT`},
		}},
		{"comments_parsed", migrateAddComments, "blobs", []step{
			{"comments_parsed", `ALTER TABLE blobs ADD COLUMN comments_parsed INTEGER NOT NULL DEFAULT 0`},
		}},
	}

	for _, m := range migrations {
		for k := 0; k <= len(m.steps); k++ {
			t.Run(fmt.Sprintf("%s/crash_after_%d", m.name, k), func(t *testing.T) {
				db, _ := createOldSchemaDB(t)
				defer func() { _ = db.Close() }()

				// Reproduce the post-crash state: the first k ALTERs landed.
				for i := 0; i < k; i++ {
					if _, err := db.Exec(m.steps[i].ddl); err != nil {
						t.Fatalf("seed crash prefix %d: %v", i, err)
					}
				}

				// Resume; a second run must also succeed (idempotent).
				for run := 0; run < 2; run++ {
					if err := m.run(t.Context(), db); err != nil {
						t.Fatalf("resume run %d after crash_after_%d: %v", run, k, err)
					}
				}

				for _, s := range m.steps {
					if !columnExists(t, db, m.table, s.column) {
						t.Errorf("%s.%s missing after resuming crash_after_%d", m.table, s.column, k)
					}
				}
			})
		}
	}
}

// TestCreateSchema_ConvergesFromAnyState is the model-based convergence proof:
// whatever state a codedb starts in and however many openers call CreateSchema
// at once, the result must equal the golden schema of a clean single-threaded
// create AND pass integrity_check. This generalizes the concurrent-open race
// past the three ALTER columns to the entire schema, and exercises
// migrateAddGitHubTables / migrateAddPRCommits that the targeted tests do not
// touch.
//
// The {v1, runners=1} case doubles as the fresh-vs-migrated equivalence check: a
// historical V1 database brought up through every migration ends up structurally
// identical to a freshly created one — proof that old installs are not left with
// a permanently different schema.
func TestCreateSchema_ConvergesFromAnyState(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}

	golden := goldenSchema(t)

	starts := []struct {
		name  string
		setup func(t *testing.T, db *sql.DB)
	}{
		{"empty", func(t *testing.T, db *sql.DB) {}},
		{"v1", func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(baseSchemaV1); err != nil {
				t.Fatalf("seed v1: %v", err)
			}
		}},
		{"already_created", func(t *testing.T, db *sql.DB) {
			if err := CreateSchema(db); err != nil {
				t.Fatalf("seed already-created: %v", err)
			}
		}},
		{"partial_type_info", func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(baseSchemaV1); err != nil {
				t.Fatalf("seed v1: %v", err)
			}
			if _, err := db.Exec(`ALTER TABLE symbols ADD COLUMN signature TEXT`); err != nil {
				t.Fatalf("seed partial migration: %v", err)
			}
		}},
	}

	for _, start := range starts {
		for _, runners := range []int{1, 4, 16} {
			t.Run(fmt.Sprintf("%s/runners_%d", start.name, runners), func(t *testing.T) {
				dbPath := filepath.Join(t.TempDir(), MetadataDBFile)

				// Persist the starting state on its own connection first, then let
				// the concurrent openers race from that on-disk state.
				seed := openRawDB(t, dbPath)
				start.setup(t, seed)
				if err := seed.Close(); err != nil {
					t.Fatalf("close seed: %v", err)
				}

				runConcurrentCreateSchema(t, dbPath, runners)

				verify := openRawDB(t, dbPath)
				defer func() { _ = verify.Close() }()
				requireIntegrityOK(t, verify)
				if got := snapshotSchema(t, verify); !reflect.DeepEqual(got, golden) {
					t.Errorf("schema from %s (runners=%d) diverged from golden\n got=%+v\nwant=%+v",
						start.name, runners, got, golden)
				}
			})
		}
	}
}

// TestData_SurvivesConcurrentMigration is the conservation proof: rows written
// before a migration must still be present after concurrent openers run
// CreateSchema. The #758 failure mode was a codedb that answered every query
// with zero results; a schema-shape assertion alone would not catch data being
// dropped or hidden, so this counts real rows through the migration under
// contention.
func TestData_SurvivesConcurrentMigration(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}

	dbPath := filepath.Join(t.TempDir(), MetadataDBFile)
	const wantRepos = 25

	seed := openRawDB(t, dbPath)
	if _, err := seed.Exec(baseSchemaV1); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	for i := range wantRepos {
		if _, err := seed.Exec(`INSERT INTO repos (name, path) VALUES (?, ?)`, fmt.Sprintf("repo-%d", i), "/p"); err != nil {
			t.Fatalf("seed repo %d: %v", i, err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed: %v", err)
	}

	runConcurrentCreateSchema(t, dbPath, 16)

	verify := openRawDB(t, dbPath)
	defer func() { _ = verify.Close() }()
	var got int
	if err := verify.QueryRow(`SELECT COUNT(*) FROM repos`).Scan(&got); err != nil {
		t.Fatalf("count repos: %v", err)
	}
	if got != wantRepos {
		t.Errorf("repos after concurrent migration = %d, want %d (rows lost or hidden by migration)", got, wantRepos)
	}
}

// TestCreateSchema_ConcurrentStress reruns the worst-case concurrent create many
// times inside one test, so a timing-dependent corruption that slips a single
// pass — like the read-during-schema-change regression caught during this work —
// gets many chances to surface under -race, without relying on `go test -count`.
func TestCreateSchema_ConcurrentStress(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}

	golden := goldenSchema(t)
	const iterations = 8
	for iter := range iterations {
		dbPath := filepath.Join(t.TempDir(), MetadataDBFile)
		runConcurrentCreateSchema(t, dbPath, 16)

		verify := openRawDB(t, dbPath)
		requireIntegrityOK(t, verify)
		if got := snapshotSchema(t, verify); !reflect.DeepEqual(got, golden) {
			_ = verify.Close()
			t.Fatalf("iteration %d: schema diverged from golden", iter)
		}
		_ = verify.Close()
	}
}
