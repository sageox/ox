package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var maintEpoch = time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)

// pageStats returns the database's page_count and freelist_count.
func pageStats(t *testing.T, s *Store) (pages, free int64) {
	t.Helper()
	if err := s.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatalf("page_count: %v", err)
	}
	if err := s.QueryRow("PRAGMA freelist_count").Scan(&free); err != nil {
		t.Fatalf("freelist_count: %v", err)
	}
	return pages, free
}

// fillFreePages grows the file by `rows` pages and frees all but `keep` of them,
// leaving the freed pages on the freelist. Each zeroblob(3000) row fills one
// 4 KiB page by itself, so rows map to pages one-to-one.
func fillFreePages(t *testing.T, s *Store, rows, keep int) {
	t.Helper()
	if _, err := s.Exec(`CREATE TABLE filler (x BLOB)`); err != nil {
		t.Fatalf("create filler: %v", err)
	}
	for range rows {
		if _, err := s.Exec(`INSERT INTO filler VALUES (zeroblob(3000))`); err != nil {
			t.Fatalf("insert filler: %v", err)
		}
	}
	if _, err := s.Exec(`DELETE FROM filler WHERE rowid > ?`, keep); err != nil {
		t.Fatalf("delete filler: %v", err)
	}
	// WAL mode keeps recent writes in the -wal file; move them into the main
	// file so its size is what a long-lived index has
	if _, err := s.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
}

// seedSnapshot inserts one file_revs snapshot of n files owned by commitID, each
// pointing at its own blob, and returns the number of rows inserted.
func seedSnapshot(t *testing.T, s *Store, commitID int64, n int) int64 {
	t.Helper()
	for i := range n {
		blobID := commitID*1000 + int64(i)
		if _, err := s.Exec(`INSERT INTO blobs (id, content_hash, language) VALUES (?, ?, 'go')`,
			blobID, fmt.Sprintf("hash-%d-%d", commitID, i)); err != nil {
			t.Fatalf("insert blob: %v", err)
		}
		if _, err := s.Exec(`INSERT INTO file_revs (commit_id, path, blob_id) VALUES (?, ?, ?)`,
			commitID, fmt.Sprintf("dir/file-%d.go", i), blobID); err != nil {
			t.Fatalf("insert file_rev: %v", err)
		}
	}
	return int64(n)
}

func seedRepoCommit(t *testing.T, s *Store, repoID, commitID int64) {
	t.Helper()
	if _, err := s.Exec(`INSERT OR IGNORE INTO repos (id, name, path) VALUES (?, ?, '/tmp/x')`,
		repoID, fmt.Sprintf("repo-%d", repoID)); err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	if _, err := s.Exec(`INSERT INTO commits (id, repo_id, hash, timestamp) VALUES (?, ?, ?, ?)`,
		commitID, repoID, fmt.Sprintf("commit-%d", commitID), maintEpoch.Unix()); err != nil {
		t.Fatalf("insert commit: %v", err)
	}
}

func seedRef(t *testing.T, s *Store, repoID int64, name string, commitID int64) {
	t.Helper()
	if _, err := s.Exec(`INSERT INTO refs (repo_id, name, commit_id) VALUES (?, ?, ?)`, repoID, name, commitID); err != nil {
		t.Fatalf("insert ref: %v", err)
	}
}

func snapshotCommits(t *testing.T, s *Store) []int64 {
	t.Helper()
	rows, err := s.Query(`SELECT DISTINCT commit_id FROM file_revs ORDER BY commit_id`)
	if err != nil {
		t.Fatalf("query snapshots: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return ids
}

func equalIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// reopen closes s and opens a fresh store on the same root, so no page cache or
// connection state carries over from before the file was altered.
func reopen(t *testing.T, s *Store) *Store {
	t.Helper()
	root := s.Root
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	next, err := Open(root)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = next.Close() })
	return next
}

// --- verification gate ---

// The full-page verification is at most daily per database, whatever the
// maintenance schedule: it is the cost this change removes from the open path,
// and it must not come back as an hourly one. Latent damage is the witness —
// a damaged table nothing else reads is only visible to the scan, so whether
// the scan ran is observable without trusting a flag the code sets itself.
//
// Failure prevented: an hourly maintenance tick re-scans a multi-GB index.
func TestMaintain_VerifiesAtMostOncePerDay(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	ctx := context.Background()

	s := openStore(t)
	first := s.maintain(ctx, maintEpoch)
	if !first.IntegrityOK || !first.IntegrityChecked {
		t.Fatalf("first run on a healthy index: OK=%v checked=%v, want both true", first.IntegrityOK, first.IntegrityChecked)
	}
	state := loadMaintState(maintStatePath(s.DBPath()))
	if !state.LastVerified.Equal(maintEpoch) {
		t.Fatalf("sidecar last_verified = %v, want %v", state.LastVerified, maintEpoch)
	}

	// damage a table no maintenance statement reads
	root := s.Root
	_ = s.Close()
	damageTableRoot(t, filepath.Join(root, MetadataDBFile), "pr_comments")
	s2, err := Open(root)
	if err != nil {
		t.Fatalf("reopen damaged: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	within := s2.maintain(ctx, maintEpoch.Add(23*time.Hour+59*time.Minute))
	if !within.IntegrityOK || within.IntegrityChecked {
		t.Errorf("run inside 24h: OK=%v checked=%v, want OK without a scan (the damage is not due to be seen yet)", within.IntegrityOK, within.IntegrityChecked)
	}

	due := s2.maintain(ctx, maintEpoch.Add(24*time.Hour))
	if due.IntegrityOK {
		t.Fatal("run at 24h did not re-verify: latent damage went unseen")
	}
	if !errors.Is(due.IntegrityErr, ErrCorrupt) {
		t.Errorf("IntegrityErr = %v, want ErrCorrupt", due.IntegrityErr)
	}
	if errors.Is(due.IntegrityErr, ErrIntegrityUnknown) {
		t.Errorf("IntegrityErr = %v, damage must not be reported as unknown", due.IntegrityErr)
	}

	// a failed verification is not a verification
	state = loadMaintState(maintStatePath(s2.DBPath()))
	if !state.LastVerified.Equal(maintEpoch) {
		t.Errorf("sidecar last_verified = %v after a failed check, want it left at %v", state.LastVerified, maintEpoch)
	}
}

// A damaged index must not be pruned or vacuumed: VACUUM would rewrite the
// damage into a fresh file with a clean header.
func TestMaintain_DamagedIndexIsNotModified(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)
	seedRepoCommit(t, s, 1, 1)
	seedRepoCommit(t, s, 1, 2)
	seedSnapshot(t, s, 1, 3)
	seedRef(t, s, 1, "refs/heads/main", 2)
	fillFreePages(t, s, 80, 0)

	s = reopen(t, s)
	_ = s.Close()
	damageTableRoot(t, s.DBPath(), "pr_comments")
	s = reopen(t, s)

	pagesBefore, freeBefore := pageStats(t, s)
	res := s.maintain(context.Background(), maintEpoch)
	if res.IntegrityOK || !errors.Is(res.IntegrityErr, ErrCorrupt) {
		t.Fatalf("OK=%v err=%v, want a corruption verdict", res.IntegrityOK, res.IntegrityErr)
	}
	if res.FileRevsPruned != 0 || res.Vacuumed {
		t.Errorf("a damaged index was modified: pruned=%d vacuumed=%v", res.FileRevsPruned, res.Vacuumed)
	}
	if got := snapshotCommits(t, s); !equalIDs(got, []int64{1}) {
		t.Errorf("snapshots = %v, want the dead one left alone", got)
	}
	if pages, free := pageStats(t, s); pages != pagesBefore || free != freeBefore {
		t.Errorf("pages/free = %d/%d, want unchanged %d/%d", pages, free, pagesBefore, freeBefore)
	}
}

// Nothing may run after the context ends: not the scan, not the prune.
func TestMaintain_CanceledContextDoesNoWork(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)
	seedRepoCommit(t, s, 1, 1)
	seedRepoCommit(t, s, 1, 2)
	seedSnapshot(t, s, 1, 3)
	seedRef(t, s, 1, "refs/heads/main", 2)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := s.maintain(ctx, maintEpoch)

	if res.IntegrityOK || !errors.Is(res.IntegrityErr, ErrIntegrityUnknown) {
		t.Errorf("OK=%v err=%v, want ErrIntegrityUnknown (a canceled check verified nothing)", res.IntegrityOK, res.IntegrityErr)
	}
	if res.FileRevsPruned != 0 {
		t.Errorf("pruned %d rows after cancellation", res.FileRevsPruned)
	}
	if got := snapshotCommits(t, s); !equalIDs(got, []int64{1}) {
		t.Errorf("snapshots = %v, want untouched [1]", got)
	}
	if _, err := os.Stat(maintStatePath(s.DBPath())); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a canceled run recorded maintenance state (stat err = %v)", err)
	}
}

// --- vacuum rule ---

func TestVacuumWorthIt(t *testing.T) {
	tests := []struct {
		name        string
		pages, free int64
		want        bool
	}{
		{"empty database", 0, 0, false},
		{"nothing free", 1000, 0, false},
		{"just under a quarter", 1000, 249, false},
		{"exactly a quarter", 1000, 250, true},
		{"just over a quarter", 1000, 251, true},
		{"mostly free", 1000, 900, true},
		{"truncating division would round this up", 99, 24, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := vacuumWorthIt(tt.pages, tt.free); got != tt.want {
				t.Errorf("vacuumWorthIt(%d, %d) = %v, want %v", tt.pages, tt.free, got, tt.want)
			}
		})
	}
}

// VACUUM rewrites every live page under an exclusive lock, so it runs only when
// it will give back a quarter of the file AND the last vacuum is over a day old.
// The freelist after Maintain is the witness, independent of the result flag.
//
// Failure prevented: a 33-minute VACUUM of the shared index on every hourly run.
func TestMaintain_VacuumRule(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	hours := func(h int) *time.Duration { d := time.Duration(h) * time.Hour; return &d }

	tests := []struct {
		name         string
		freeRows     int            // pages left on the freelist
		lastVacuum   *time.Duration // how long before now; nil = never
		wantVacuumed bool
	}{
		{"mostly free, never vacuumed", 100, nil, true},
		{"mostly free, vacuumed 25h ago", 100, hours(25), true},
		{"mostly free, vacuumed 23h ago", 100, hours(23), false},
		{"mostly free, vacuumed an hour ago", 100, hours(1), false},
		{"barely free, never vacuumed", 4, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openStore(t)
			fillFreePages(t, s, tt.freeRows, 0)
			now := maintEpoch

			// verified just now, so the verification gate does not interfere
			state := maintState{LastVerified: now}
			if tt.lastVacuum != nil {
				state.LastVacuum = now.Add(-*tt.lastVacuum)
			}
			if err := state.save(maintStatePath(s.DBPath())); err != nil {
				t.Fatalf("seed sidecar: %v", err)
			}

			pages, free := pageStats(t, s)
			if wantWorth := tt.wantVacuumed || tt.lastVacuum != nil; wantWorth != vacuumWorthIt(pages, free) {
				t.Fatalf("fixture: pages=%d free=%d, vacuumWorthIt=%v, want %v", pages, free, vacuumWorthIt(pages, free), wantWorth)
			}

			res := s.maintain(context.Background(), now)

			if res.Vacuumed != tt.wantVacuumed {
				t.Errorf("Vacuumed = %v, want %v", res.Vacuumed, tt.wantVacuumed)
			}
			_, freeAfter := pageStats(t, s)
			if tt.wantVacuumed {
				if freeAfter != 0 {
					t.Errorf("freelist after vacuum = %d, want 0", freeAfter)
				}
				if got := loadMaintState(maintStatePath(s.DBPath())).LastVacuum; !got.Equal(now) {
					t.Errorf("sidecar last_vacuum = %v, want %v", got, now)
				}
				if res.SizeAfter >= res.SizeBefore {
					t.Errorf("file did not shrink: %d -> %d", res.SizeBefore, res.SizeAfter)
				}
			} else if freeAfter != free {
				t.Errorf("freelist changed %d -> %d without a vacuum", free, freeAfter)
			}
		})
	}
}

// The row count that used to trigger VACUUM is not evidence the file has
// anything to give back; the free-page ratio is.
//
// Failure prevented: pruning 150 small rows rewrites the whole database.
func TestMaintain_LargeRowPruneAloneDoesNotVacuum(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)
	for i := 1; i <= 150; i++ {
		if _, err := s.Exec("INSERT INTO blobs (content_hash, language) VALUES (?, 'go')", fmt.Sprintf("orphan-%d", i)); err != nil {
			t.Fatalf("insert blob: %v", err)
		}
	}

	res := s.maintain(context.Background(), maintEpoch)

	if res.OrphanBlobsPruned != 150 {
		t.Fatalf("OrphanBlobsPruned = %d, want 150 (the fixture must prune over 100 rows)", res.OrphanBlobsPruned)
	}
	if res.Vacuumed {
		t.Error("a prune that freed no meaningful share of the file triggered a VACUUM")
	}
}

// VACUUM builds its rewritten copy in the temp store. The pool's connections run
// with temp_store=MEMORY, so on one of them a multi-GB index is rebuilt entirely
// in RAM; vacuum therefore switches its connection to file-backed temp storage,
// and must hand the connection back as it found it.
func TestVacuum_RestoresPoolTempStore(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)
	s.db.SetMaxOpenConns(1) // the connection vacuum used is then the one this reads
	fillFreePages(t, s, 40, 0)

	if err := s.vacuum(context.Background()); err != nil {
		t.Fatalf("vacuum: %v", err)
	}

	var tempStore int
	if err := s.QueryRow("PRAGMA temp_store").Scan(&tempStore); err != nil {
		t.Fatalf("temp_store: %v", err)
	}
	const tempStoreMemory = 2
	if tempStore != tempStoreMemory {
		t.Errorf("pool connection temp_store = %d after vacuum, want %d (MEMORY)", tempStore, tempStoreMemory)
	}
}

// --- sidecar ---

func TestMaintState_Due(t *testing.T) {
	now := maintEpoch
	tests := []struct {
		name string
		last time.Time
		want bool
	}{
		{"never", time.Time{}, true},
		{"just now", now, false},
		{"23h59m ago", now.Add(-24*time.Hour + time.Minute), false},
		{"exactly 24h ago", now.Add(-24 * time.Hour), true},
		{"two days ago", now.Add(-48 * time.Hour), true},
		{"in the future (clock stepped back)", now.Add(time.Hour), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := due(tt.last, now); got != tt.want {
				t.Errorf("due(%v, now) = %v, want %v", tt.last, got, tt.want)
			}
		})
	}
}

// A sidecar that cannot be trusted means "due", never "skip": losing it costs
// one extra scan, whereas trusting garbage could suppress verification forever.
func TestLoadMaintState_UnusableFileMeansDue(t *testing.T) {
	tests := []struct {
		name    string
		content *string
	}{
		{"missing", nil},
		{"empty", ptr("")},
		{"not json", ptr("last_verified: yesterday")},
		{"truncated json", ptr(`{"last_verified": "2026-10-04T09:`)},
		{"wrong types", ptr(`{"last_verified": 12, "last_vacuum": [1]}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "metadata.db"+maintStateSuffix)
			if tt.content != nil {
				if err := os.WriteFile(path, []byte(*tt.content), 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			st := loadMaintState(path)
			if !st.verifyDue(maintEpoch) || !st.vacuumDue(maintEpoch) {
				t.Errorf("state %+v is not due; an unusable sidecar must mean both jobs are due", st)
			}
		})
	}
}

func ptr(s string) *string { return &s }

func TestMaintState_SaveRoundTripsAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metadata.db"+maintStateSuffix)
	want := maintState{LastVerified: maintEpoch, LastVacuum: maintEpoch.Add(-time.Hour)}

	if err := want.save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	// overwrite: the rename must replace, not fail on an existing file
	want.LastVerified = maintEpoch.Add(time.Minute)
	if err := want.save(path); err != nil {
		t.Fatalf("second save: %v", err)
	}

	got := loadMaintState(path)
	if !got.LastVerified.Equal(want.LastVerified) || !got.LastVacuum.Equal(want.LastVacuum) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries after save, want only the sidecar (temp file leaked?)", len(entries))
	}
}

// --- dead snapshots ---

// Search reaches file_revs only through a join on refs, so a snapshot no ref
// points at is dead weight — and it keeps its blobs "referenced", which is why
// the orphan prune never freed anything.
//
// Failure prevented: the ledger index growing by a full tree snapshot per build.
func TestMaintain_PrunesDeadSnapshotsAcrossRepos(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)
	seedRepoCommit(t, s, 1, 1)
	seedRepoCommit(t, s, 1, 2)
	seedRepoCommit(t, s, 1, 3)
	seedRepoCommit(t, s, 2, 4)
	seedRepoCommit(t, s, 2, 5)
	rows := seedSnapshot(t, s, 1, 4) + seedSnapshot(t, s, 2, 4) + seedSnapshot(t, s, 3, 4) + seedSnapshot(t, s, 4, 3) + seedSnapshot(t, s, 5, 3)
	_ = rows
	seedRef(t, s, 1, "refs/heads/main", 3)
	seedRef(t, s, 2, "refs/heads/main", 5)

	res := s.maintain(context.Background(), maintEpoch)

	if want := int64(4 + 4 + 3); res.FileRevsPruned != want {
		t.Errorf("FileRevsPruned = %d, want %d (snapshots of commits 1, 2 and 4)", res.FileRevsPruned, want)
	}
	if got := snapshotCommits(t, s); !equalIDs(got, []int64{3, 5}) {
		t.Errorf("surviving snapshots = %v, want exactly the ref tips [3 5]", got)
	}
	// the blobs only the dead snapshots referenced are now orphans and go too
	if want := int64(4 + 4 + 3); res.OrphanBlobsPruned != want {
		t.Errorf("OrphanBlobsPruned = %d, want %d", res.OrphanBlobsPruned, want)
	}
}

type fileRevRow struct {
	ID, CommitID, BlobID int64
	Path                 string
}

func fileRevRows(t *testing.T, s *Store, commitIDs ...int64) []fileRevRow {
	t.Helper()
	var all []fileRevRow
	for _, c := range commitIDs {
		rows, err := s.Query(`SELECT id, commit_id, path, blob_id FROM file_revs WHERE commit_id = ? ORDER BY id`, c)
		if err != nil {
			t.Fatalf("query file_revs: %v", err)
		}
		for rows.Next() {
			var r fileRevRow
			if err := rows.Scan(&r.ID, &r.CommitID, &r.Path, &r.BlobID); err != nil {
				t.Fatalf("scan: %v", err)
			}
			all = append(all, r)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows: %v", err)
		}
		_ = rows.Close()
	}
	return all
}

// However large the backlog — a handful of snapshots deleted one by one, or a
// legacy index rebuilt in one transaction — the result is the same: the live
// snapshots are untouched, row for row and id for id, and the connection that
// did the work goes back to the pool exactly as it came (FK enforcement on,
// MEMORY temp store, no stash table left behind).
//
// Failure prevented: the prune that frees a 5 GB index also loses live rows, or
// leaves a pooled connection that no longer enforces foreign keys.
func TestMaintain_PrunesDeadSnapshotsAtEveryBacklogSize(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	const filesPerSnapshot = 6
	tests := []struct {
		name string
		dead int
	}{
		{"one dead snapshot", 1},
		{"just under the bulk threshold", bulkPruneSnapshots - 1},
		{"exactly the bulk threshold", bulkPruneSnapshots},
		{"far over the bulk threshold", bulkPruneSnapshots + 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openStore(t)
			s.db.SetMaxOpenConns(1) // the connection the prune used is then the one inspected below

			// repo 1: commit 1 is kept alive by a second ref, commits 2..N-1 are
			// dead, commit N is the main tip. repo 2: commit 900 is its own tip.
			n := int64(tt.dead) + 2
			for c := int64(1); c <= n; c++ {
				seedRepoCommit(t, s, 1, c)
				seedSnapshot(t, s, c, filesPerSnapshot)
			}
			seedRepoCommit(t, s, 2, 900)
			seedSnapshot(t, s, 900, filesPerSnapshot-2)
			seedRef(t, s, 1, "refs/heads/main", n)
			seedRef(t, s, 1, "refs/heads/old", 1)
			seedRef(t, s, 2, "refs/heads/main", 900)
			live := []int64{1, n, 900}
			liveBefore := fileRevRows(t, s, live...)

			res := s.maintain(context.Background(), maintEpoch)

			if want := int64(tt.dead * filesPerSnapshot); res.FileRevsPruned != want {
				t.Errorf("FileRevsPruned = %d, want %d", res.FileRevsPruned, want)
			}
			if got := snapshotCommits(t, s); !equalIDs(got, live) {
				t.Errorf("surviving snapshots = %v, want exactly the ref tips %v", got, live)
			}
			liveAfter := fileRevRows(t, s, live...)
			if len(liveAfter) != len(liveBefore) {
				t.Fatalf("live rows = %d, want %d", len(liveAfter), len(liveBefore))
			}
			for i := range liveBefore {
				if liveAfter[i] != liveBefore[i] {
					t.Errorf("live row %d changed: %+v -> %+v", i, liveBefore[i], liveAfter[i])
				}
			}

			// the pooled connection is as it was found
			var fk, tempStore, stash int
			if err := s.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
				t.Errorf("foreign_keys = %d (err %v), want 1", fk, err)
			}
			if err := s.QueryRow("PRAGMA temp_store").Scan(&tempStore); err != nil || tempStore != 2 {
				t.Errorf("temp_store = %d (err %v), want 2 (MEMORY)", tempStore, err)
			}
			if err := s.QueryRow("SELECT COUNT(*) FROM sqlite_temp_master WHERE name = 'live_file_revs'").Scan(&stash); err != nil || stash != 0 {
				t.Errorf("stash table left behind: count %d (err %v)", stash, err)
			}
			if _, err := s.Exec(`INSERT INTO file_revs (commit_id, path, blob_id) VALUES (424242, 'orphan.go', 1)`); err == nil {
				t.Error("an insert referencing a missing commit succeeded: foreign keys are not enforced on the pool connection")
			}
		})
	}
}

// A commit is shared between repos rows (commits.hash is globally unique), so
// "dead" means no ref ANYWHERE points at it, and a repo-scoped prune must not
// reach into another repo's snapshots.
func TestPruneDeadFileRevsForRepo_ScopeAndLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)
	ctx := context.Background()

	// repo 1: commits 1-5 are dead snapshots, commit 6 is the tip
	for c := int64(1); c <= 6; c++ {
		seedRepoCommit(t, s, 1, c)
		seedSnapshot(t, s, c, 2)
	}
	seedRef(t, s, 1, "refs/heads/main", 6)
	// repo 2: commit 7 is dead, commit 8 is its tip
	seedRepoCommit(t, s, 2, 7)
	seedSnapshot(t, s, 7, 2)
	seedRepoCommit(t, s, 2, 8)
	seedSnapshot(t, s, 8, 2)
	seedRef(t, s, 2, "refs/heads/main", 8)
	// repo 1's commit 5 is ALSO the tip of a repo-2 ref: alive, whoever owns it
	seedRef(t, s, 2, "refs/heads/feature", 5)

	first, err := PruneDeadFileRevsForRepo(ctx, s.queries, 1, 2)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if first.Snapshots != 2 || first.Rows != 4 {
		t.Errorf("limited prune = %+v, want 2 snapshots / 4 rows", first)
	}
	if got := snapshotCommits(t, s); !equalIDs(got, []int64{3, 4, 5, 6, 7, 8}) {
		t.Errorf("after limit-2 prune snapshots = %v, want the two lowest dead ones (1, 2) gone", got)
	}

	second, err := PruneDeadFileRevsForRepo(ctx, s.queries, 1, 100)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if second.Snapshots != 2 || second.Rows != 4 {
		t.Errorf("second prune = %+v, want 2 snapshots / 4 rows (commits 3 and 4)", second)
	}
	// 5 survives (a ref in repo 2 points at it), 7 survives (another repo's dead snapshot)
	if got := snapshotCommits(t, s); !equalIDs(got, []int64{5, 6, 7, 8}) {
		t.Errorf("final snapshots = %v, want [5 6 7 8]", got)
	}
}

func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// seedBlobWithDependents gives blob id a symbol, a reference, a comment and a
// self-edge, so a prune of the blob has something derived from it to take along.
func seedBlobWithDependents(t *testing.T, s *Store, blobID, symbolID int64) {
	t.Helper()
	for _, stmt := range []string{
		fmt.Sprintf(`INSERT INTO blobs (id, content_hash, language, parsed) VALUES (%d, 'blob-%d', 'go', 1)`, blobID, blobID),
		fmt.Sprintf(`INSERT INTO symbols (id, blob_id, name, kind, line, col) VALUES (%d, %d, 'Sym%d', 'function', 1, 0)`, symbolID, blobID, symbolID),
		fmt.Sprintf(`INSERT INTO symbol_refs (blob_id, symbol_id, ref_name, kind, line, col) VALUES (%d, %d, 'Callee', 'call', 2, 0)`, blobID, symbolID),
		fmt.Sprintf(`INSERT INTO comments (blob_id, text, kind, line, end_line, col, end_col) VALUES (%d, 'note', 'todo', 1, 1, 0, 4)`, blobID),
		fmt.Sprintf(`INSERT INTO symbol_edges (src_blob_id, src_symbol_id, dst_blob_id, dst_symbol_id, dst_name, kind, confidence, line, col)
			VALUES (%d, %d, %d, %d, 'Self', 'call', 'extracted', 3, 0)`, blobID, symbolID, blobID, symbolID),
	} {
		if _, err := s.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
}

// A blob is pruned only when no snapshot and no diff references it, and it goes
// together with everything derived from it. A diff needs its blob's content hash
// to be rendered, so a historical blob a diff points at stays — with its symbols,
// so the parsed flag and the symbols never disagree. Every historical blob on a
// ledger index is such a blob; selecting them anyway (the old "not in file_revs"
// rule) made each delete fail the diffs foreign key at the end of a ten-minute
// statement.
//
// Failure prevented: hourly maintenance burning ten CPU-minutes to fail.
func TestMaintain_OrphanBlobPruneKeepsWhatIsReferencedAndTakesDependentsAlong(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)
	seedRepoCommit(t, s, 1, 1)
	seedRepoCommit(t, s, 1, 2)
	seedRef(t, s, 1, "refs/heads/main", 1)

	// 100: live (in the ref's snapshot)
	seedBlobWithDependents(t, s, 100, 1)
	if _, err := s.Exec(`INSERT INTO file_revs (commit_id, path, blob_id) VALUES (1, 'live.go', 100)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 200 and 210: historical, but a diff references them as new and old blob
	seedBlobWithDependents(t, s, 200, 2)
	seedBlobWithDependents(t, s, 210, 3)
	if _, err := s.Exec(`INSERT INTO diffs (commit_id, path, old_blob_id, new_blob_id) VALUES (2, 'a.go', 210, 200)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 300: a true orphan
	seedBlobWithDependents(t, s, 300, 4)
	// 400: a true orphan that live blob 100 has a resolved edge into
	seedBlobWithDependents(t, s, 400, 5)
	if _, err := s.Exec(`INSERT INTO symbol_edges (src_blob_id, src_symbol_id, dst_blob_id, dst_symbol_id, dst_name, kind, confidence, line, col)
		VALUES (100, 1, 400, 5, 'Sym5', 'call', 'extracted', 9, 0)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res := s.maintain(context.Background(), maintEpoch)

	if res.OrphanBlobsPruned != 2 || res.StaleSymbolsCount != 2 {
		t.Errorf("pruned blobs=%d symbols=%d, want 2 and 2 (blobs 300 and 400)", res.OrphanBlobsPruned, res.StaleSymbolsCount)
	}
	for table, want := range map[string]int{
		"blobs":        3, // 100, 200, 210
		"symbols":      3,
		"symbol_refs":  3,
		"comments":     3,
		"symbol_edges": 4, // one self-edge each for 100, 200, 210, plus 100's edge into the pruned blob 400
	} {
		if got := countRows(t, s, table); got != want {
			t.Errorf("%s rows = %d, want %d", table, got, want)
		}
	}
	// the live blob's edge into the pruned blob survives as an unresolved reference
	var dstBlob, dstSymbol sql.NullInt64
	if err := s.QueryRow(`SELECT dst_blob_id, dst_symbol_id FROM symbol_edges WHERE dst_name = 'Sym5'`).Scan(&dstBlob, &dstSymbol); err != nil {
		t.Fatalf("edge into pruned blob: %v", err)
	}
	if dstBlob.Valid || dstSymbol.Valid {
		t.Errorf("edge still resolves into a pruned blob: blob=%v symbol=%v", dstBlob, dstSymbol)
	}

	// foreign keys were off for the job, so nothing else guarantees integrity
	rows, err := s.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		var table, parent string
		var rowid, fkid sql.NullInt64
		_ = rows.Scan(&table, &rowid, &parent, &fkid)
		t.Errorf("dangling reference after the prune: %s row %v -> %s", table, rowid.Int64, parent)
	}
}

// The prune is all-or-nothing: when any step fails, the earlier ones must not
// stick (a blob row left marked parsed with its symbols gone is skipped by the
// parser forever), and the connection must go back to the pool with foreign keys
// enforced again.
func TestMaintain_OrphanPruneIsAllOrNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)
	s.db.SetMaxOpenConns(1) // the connection the prune used is then the one inspected below
	seedBlobWithDependents(t, s, 300, 4)
	// the last step, deleting the blob, fails after the dependents are gone
	if _, err := s.Exec(`CREATE TRIGGER block_blob_delete BEFORE DELETE ON blobs BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
		t.Fatalf("trigger: %v", err)
	}

	res := s.maintain(context.Background(), maintEpoch)

	if res.StaleSymbolsCount != 0 || res.OrphanBlobsPruned != 0 {
		t.Errorf("reported symbols=%d blobs=%d pruned from a rolled-back transaction", res.StaleSymbolsCount, res.OrphanBlobsPruned)
	}
	for table, want := range map[string]int{"blobs": 1, "symbols": 1, "symbol_refs": 1, "comments": 1, "symbol_edges": 1} {
		if got := countRows(t, s, table); got != want {
			t.Errorf("%s rows = %d, want %d: half the prune stuck", table, got, want)
		}
	}
	var fk, stash int
	if err := s.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Errorf("foreign_keys = %d (err %v) after a failed prune, want 1", fk, err)
	}
	if err := s.QueryRow("SELECT COUNT(*) FROM sqlite_temp_master WHERE name = 'orphan_blobs'").Scan(&stash); err != nil || stash != 0 {
		t.Errorf("orphan set left behind: count %d (err %v)", stash, err)
	}
}
