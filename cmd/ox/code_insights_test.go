package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/store"
	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

func setupInsightsDB(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	// insert test repos
	_, err = s.Exec(`INSERT INTO repos (id, name, path) VALUES (1, 'workspace-a', '/a'), (2, 'workspace-b', '/b'), (3, 'workspace-c', '/c')`)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().Unix()
	day := int64(86400)

	// insert commits across workspaces
	_, err = s.Exec(`
		INSERT INTO commits (id, repo_id, hash, author, message, timestamp) VALUES
		(1, 1, 'aaa1111', 'alice', 'add auth middleware', ?),
		(2, 1, 'aaa2222', 'alice', 'fix auth token refresh', ?),
		(3, 2, 'bbb1111', 'bob', 'refactor auth flow', ?),
		(4, 2, 'bbb2222', 'bob', 'update login handler', ?),
		(5, 3, 'ccc1111', 'carol', 'fix auth regression', ?),
		(6, 1, 'aaa3333', 'alice', 'add rate limiting', ?)`,
		now-day, now-2*day, now-day, now-3*day, now-day, now-5*day)
	if err != nil {
		t.Fatal(err)
	}

	// insert diffs — auth.go touched by all 3 workspaces (contention)
	// sync.go touched by workspace-a only (hotspot, no contention)
	_, err = s.Exec(`
		INSERT INTO diffs (id, commit_id, path) VALUES
		(1, 1, 'internal/auth/auth.go'),
		(2, 2, 'internal/auth/auth.go'),
		(3, 3, 'internal/auth/auth.go'),
		(4, 4, 'cmd/server/main.go'),
		(5, 5, 'internal/auth/auth.go'),
		(6, 6, 'internal/daemon/sync.go'),
		(7, 1, 'internal/daemon/sync.go'),
		(8, 2, 'internal/daemon/sync.go')`)
	if err != nil {
		t.Fatal(err)
	}

	// insert open PRs
	_, err = s.Exec(`
		INSERT INTO pull_requests (id, number, title, author, state, labels) VALUES
		(1, 100, 'feat: add OAuth2 support', 'alice', 'open', 'enhancement'),
		(2, 99, 'fix: token expiry bug', 'bob', 'open', 'bug,urgent'),
		(3, 98, 'docs: update API guide', 'carol', 'merged', '')`)
	if err != nil {
		t.Fatal(err)
	}

	// insert open issues
	_, err = s.Exec(`
		INSERT INTO issues (id, number, title, author, state, labels) VALUES
		(1, 50, 'Auth fails on refresh', 'dave', 'open', 'bug'),
		(2, 49, 'Add rate limiting', 'eve', 'open', 'enhancement'),
		(3, 48, 'Old bug fixed', 'frank', 'closed', 'bug')`)
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func TestQueryHotspots(t *testing.T) {
	s := setupInsightsDB(t)
	results, err := queryHotspots(s, 14, 10)
	if err != nil {
		t.Fatalf("queryHotspots: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected hotspots, got none")
	}
	// auth.go should be top hotspot (4 changes from 3 workspaces)
	if results[0].Path != "internal/auth/auth.go" {
		t.Errorf("expected top hotspot to be auth.go, got %s", results[0].Path)
	}
	if results[0].Changes != 4 {
		t.Errorf("expected 4 changes, got %d", results[0].Changes)
	}
	if len(results[0].RecentCommits) == 0 {
		t.Error("expected recent commit messages in hotspot")
	}
}

func TestQueryContention(t *testing.T) {
	s := setupInsightsDB(t)
	results, err := queryContention(s, 14, 10)
	if err != nil {
		t.Fatalf("queryContention: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected contention results, got none")
	}
	// auth.go is touched by 3 workspaces
	if results[0].Path != "internal/auth/auth.go" {
		t.Errorf("expected auth.go contention, got %s", results[0].Path)
	}
	if results[0].WorkspaceCount != 3 {
		t.Errorf("expected 3 workspaces, got %d", results[0].WorkspaceCount)
	}
	if len(results[0].Workspaces) != 3 {
		t.Errorf("expected 3 workspace names, got %d", len(results[0].Workspaces))
	}
}

func TestQueryRecentCommits(t *testing.T) {
	s := setupInsightsDB(t)
	results, err := queryRecentCommits(s, 14, 10)
	if err != nil {
		t.Fatalf("queryRecentCommits: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected recent commits, got none")
	}
	// should be ordered by timestamp desc
	for i := 1; i < len(results); i++ {
		if results[i].Timestamp > results[i-1].Timestamp {
			t.Errorf("commits not in descending order at index %d", i)
		}
	}
	// hashes should be truncated to 7 chars
	for _, c := range results {
		if len(c.Hash) != 7 {
			t.Errorf("expected 7-char hash, got %q (%d chars)", c.Hash, len(c.Hash))
		}
	}
}

func TestQueryOpenPRs(t *testing.T) {
	s := setupInsightsDB(t)
	results, err := queryOpenPRs(s, 10)
	if err != nil {
		t.Fatalf("queryOpenPRs: %v", err)
	}
	// should only return open PRs (2 of 3)
	if len(results) != 2 {
		t.Fatalf("expected 2 open PRs, got %d", len(results))
	}
	// ordered by number desc
	if results[0].Number != 100 {
		t.Errorf("expected PR #100 first, got #%d", results[0].Number)
	}
}

func TestQueryOpenIssues(t *testing.T) {
	s := setupInsightsDB(t)
	results, err := queryOpenIssues(s, 10)
	if err != nil {
		t.Fatalf("queryOpenIssues: %v", err)
	}
	// should only return open issues (2 of 3)
	if len(results) != 2 {
		t.Fatalf("expected 2 open issues, got %d", len(results))
	}
}

func TestQueryHotspots_Limit(t *testing.T) {
	s := setupInsightsDB(t)
	results, err := queryHotspots(s, 14, 1)
	if err != nil {
		t.Fatalf("queryHotspots: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result with limit=1, got %d", len(results))
	}
}

func TestQueryHotspots_DaysFilter(t *testing.T) {
	s := setupInsightsDB(t)
	// with 0 days window, nothing should match
	results, err := queryHotspots(s, 0, 10)
	if err != nil {
		t.Fatalf("queryHotspots: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results with days=0, got %d", len(results))
	}
}

func TestGenerateGuidance(t *testing.T) {
	tests := []struct {
		name       string
		hotspots   []insightHotspot
		contention []insightContention
		wantEmpty  bool
		wantSubstr string
	}{
		{
			name:      "no data",
			wantEmpty: true,
		},
		{
			name:       "hotspot only",
			hotspots:   []insightHotspot{{Path: "foo.go", Changes: 10}},
			wantSubstr: "foo.go",
		},
		{
			name:       "contention with 3+ workspaces",
			contention: []insightContention{{Path: "bar.go", WorkspaceCount: 3, Workspaces: []string{"a", "b", "c"}}},
			wantSubstr: "3+ workspaces",
		},
		{
			name:       "hotspot AND contended — highest risk",
			hotspots:   []insightHotspot{{Path: "sync.go", Changes: 15}},
			contention: []insightContention{{Path: "sync.go", WorkspaceCount: 3, Workspaces: []string{"a", "b", "c"}}},
			wantSubstr: "hotspot",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := generateGuidance(tt.hotspots, tt.contention)
			if tt.wantEmpty && g != "" {
				t.Errorf("expected empty guidance, got %q", g)
			}
			if !tt.wantEmpty && g == "" {
				t.Error("expected non-empty guidance")
			}
			if tt.wantSubstr != "" && !strings.Contains(g, tt.wantSubstr) {
				t.Errorf("expected guidance to contain %q, got %q", tt.wantSubstr, g)
			}
		})
	}
}

func TestRenderInsightsHuman_Empty(t *testing.T) {
	out := insightsOutput{}
	result := renderInsightsHuman(out, 14)
	if !strings.Contains(result, "No data") {
		t.Error("expected 'No data' message for empty output")
	}
}

func TestRenderInsightsHuman_WithData(t *testing.T) {
	out := insightsOutput{
		Hotspots: []insightHotspot{
			{Path: "sync.go", Changes: 10, RecentCommits: []string{"fix race"}},
		},
		OpenPRs: []insightPR{
			{Number: 42, Title: "Add feature", Author: "alice"},
		},
	}
	result := renderInsightsHuman(out, 7)
	if !strings.Contains(result, "Hotspots") {
		t.Error("expected Hotspots section")
	}
	if !strings.Contains(result, "sync.go") {
		t.Error("expected sync.go in output")
	}
	if !strings.Contains(result, "#42") {
		t.Error("expected PR #42 in output")
	}
}

func TestGenerateHints(t *testing.T) {
	tests := []struct {
		name       string
		setupExtra string // additional SQL to run
		openPRs    []insightPR
		openIssues []insightIssue
		wantNil    bool
		wantPR     bool
		wantIssue  bool
	}{
		{
			name:       "no github data at all",
			setupExtra: "DELETE FROM pull_requests; DELETE FROM issues",
			wantNil:    true,
		},
		{
			name:       "open PRs and issues present",
			openPRs:    []insightPR{{Number: 1}},
			openIssues: []insightIssue{{Number: 1}},
			wantPR:     true,
			wantIssue:  true,
		},
		{
			name:       "only closed/merged PRs in DB, no open",
			setupExtra: "DELETE FROM pull_requests; INSERT INTO pull_requests (id, number, title, state) VALUES (99, 200, 'merged pr', 'merged')",
			wantPR:     true,
			wantIssue:  true, // test DB still has issues from setupInsightsDB
		},
		{
			name:       "only closed issues in DB, no open",
			setupExtra: "DELETE FROM issues; DELETE FROM pull_requests; INSERT INTO issues (id, number, title, state) VALUES (99, 300, 'closed issue', 'closed')",
			wantPR:     false,
			wantIssue:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := setupInsightsDB(t)
			if tt.setupExtra != "" {
				if _, err := s.Exec(tt.setupExtra); err != nil {
					t.Fatal(err)
				}
			}
			hints := generateHints(s, tt.openPRs, tt.openIssues)
			if tt.wantNil {
				if hints != nil {
					t.Errorf("expected nil hints, got %+v", hints)
				}
				return
			}
			if hints == nil {
				t.Fatal("expected non-nil hints")
			}
			if tt.wantPR && hints.PRDetails == "" {
				t.Error("expected PR hint")
			}
			if !tt.wantPR && hints.PRDetails != "" {
				t.Error("unexpected PR hint")
			}
			if tt.wantIssue && hints.IssueDetails == "" {
				t.Error("expected issue hint")
			}
			if !tt.wantIssue && hints.IssueDetails != "" {
				t.Error("unexpected issue hint")
			}
			// verify hint text mentions the search command
			if hints.PRDetails != "" && !strings.Contains(hints.PRDetails, "type:pr") {
				t.Error("PR hint should mention type:pr search syntax")
			}
			if hints.IssueDetails != "" && !strings.Contains(hints.IssueDetails, "type:issue") {
				t.Error("issue hint should mention type:issue search syntax")
			}
		})
	}
}

func TestSplitAndTrim(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"a,b,c", 3},
		{"a, b, c", 3},
		{"", 0},
		{"single", 1},
	}
	for _, tt := range tests {
		result := splitAndTrim(tt.input, ",")
		if len(result) != tt.want {
			t.Errorf("splitAndTrim(%q) = %d items, want %d", tt.input, len(result), tt.want)
		}
	}
}

// TestInsights_SurvivesCorruptBleveMapping is the regression test for the
// "I keep seeing this periodically" bug: `ox code insights` failing with
//
//	Error: open codedb: open codedb store: open code index:
//	  bleve index appears to be in use (lock contention):
//	  error parsing mapping JSON: unexpected end of JSON input
//
// observed across workspaces after an indexer was killed mid-flush (Conductor
// cycling, daemon shutdown, OOM). The fix has two layers:
//
//  1. store.openOrCreateBleveIndex self-heals a structurally-corrupt
//     `_mapping` doc by nuking + recreating the sub-index + writing a
//     `.needs_reindex_<name>` marker (covered by store/store_test.go).
//  2. `ox code insights` uses codedb.OpenSQLOnly so it doesn't depend on
//     bleve at all — every insights query is pure SQL.
//
// This test exercises layer (2): seed insights data, corrupt EVERY bleve
// sub-index's mapping, then verify the SQL queries that back `ox code
// insights` still return results via OpenSQLOnly.
//
// Failure prevented: future refactor accidentally re-introduces bleve
// dependency in the insights read path, and the cryptic error returns.
func TestInsights_SurvivesCorruptBleveMapping(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}

	tmp := t.TempDir()

	// Seed data through a full Store so all schema + indexes exist.
	s := setupInsightsDBAt(t, tmp)
	require.NoError(t, s.Close())

	// Corrupt every bleve sub-index's _mapping doc — the exact on-disk state
	// observed in the field after an interrupted writer.
	for _, sub := range []string{"code", "diff", "comment"} {
		boltPath := filepath.Join(tmp, "bleve", sub, "store", "root.bolt")
		corruptMappingForSubIndex(t, boltPath)
	}

	// Open SQL-only — should succeed without touching bleve.
	db, err := codedb.OpenSQLOnly(tmp)
	require.NoError(t, err, "OpenSQLOnly must succeed even when every bleve sub-index is corrupt")
	defer db.Close()

	// All five insights queries must return data — these are the exact calls
	// `ox code insights` makes in cmd/ox/code_insights.go:110-114.
	store := db.Store()
	hotspots, err := queryHotspots(store, 14, 10)
	require.NoError(t, err, "queryHotspots")
	require.NotEmpty(t, hotspots, "hotspots must surface seeded data")

	contention, err := queryContention(store, 14, 10)
	require.NoError(t, err, "queryContention")
	require.NotEmpty(t, contention, "contention must surface auth.go touched by 3 workspaces")

	commits, err := queryRecentCommits(store, 14, 10)
	require.NoError(t, err, "queryRecentCommits")
	require.NotEmpty(t, commits, "recent commits must surface seeded data")

	prs, err := queryOpenPRs(store, 10)
	require.NoError(t, err, "queryOpenPRs")
	require.NotEmpty(t, prs, "open PRs must surface seeded data")

	issues, err := queryOpenIssues(store, 10)
	require.NoError(t, err, "queryOpenIssues")
	require.NotEmpty(t, issues, "open issues must surface seeded data")
}

// setupInsightsDBAt is setupInsightsDB pinned to a caller-specified directory,
// so a regression test can corrupt the on-disk bleve sub-indexes after
// seeding completes.
func setupInsightsDBAt(t *testing.T, root string) *store.Store {
	t.Helper()
	s, err := store.Open(root)
	require.NoError(t, err)

	_, err = s.Exec(`INSERT INTO repos (id, name, path) VALUES (1, 'workspace-a', '/a'), (2, 'workspace-b', '/b'), (3, 'workspace-c', '/c')`)
	require.NoError(t, err)

	now := time.Now().Unix()
	day := int64(86400)
	_, err = s.Exec(`
		INSERT INTO commits (id, repo_id, hash, author, message, timestamp) VALUES
		(1, 1, 'aaa1111', 'alice', 'add auth middleware', ?),
		(2, 1, 'aaa2222', 'alice', 'fix auth token refresh', ?),
		(3, 2, 'bbb1111', 'bob', 'refactor auth flow', ?),
		(4, 2, 'bbb2222', 'bob', 'update login handler', ?),
		(5, 3, 'ccc1111', 'carol', 'fix auth regression', ?),
		(6, 1, 'aaa3333', 'alice', 'add rate limiting', ?)`,
		now-day, now-2*day, now-day, now-3*day, now-day, now-5*day)
	require.NoError(t, err)

	_, err = s.Exec(`
		INSERT INTO diffs (id, commit_id, path) VALUES
		(1, 1, 'internal/auth/auth.go'),
		(2, 2, 'internal/auth/auth.go'),
		(3, 3, 'internal/auth/auth.go'),
		(4, 4, 'cmd/server/main.go'),
		(5, 5, 'internal/auth/auth.go'),
		(6, 6, 'internal/daemon/sync.go'),
		(7, 1, 'internal/daemon/sync.go'),
		(8, 2, 'internal/daemon/sync.go')`)
	require.NoError(t, err)

	_, err = s.Exec(`
		INSERT INTO pull_requests (id, number, title, author, state, labels) VALUES
		(1, 100, 'feat: add OAuth2 support', 'alice', 'open', 'enhancement'),
		(2, 99, 'fix: token expiry bug', 'bob', 'open', 'bug,urgent')`)
	require.NoError(t, err)

	_, err = s.Exec(`
		INSERT INTO issues (id, number, title, author, state, labels) VALUES
		(1, 50, 'Auth fails on refresh', 'dave', 'open', 'bug'),
		(2, 49, 'Add rate limiting', 'eve', 'open', 'enhancement')`)
	require.NoError(t, err)

	return s
}

// corruptMappingForSubIndex overwrites the latest snapshot's `_mapping` value
// with zero bytes — the exact on-disk state observed in the field.
func corruptMappingForSubIndex(t *testing.T, boltPath string) {
	t.Helper()
	db, err := bbolt.Open(boltPath, 0o600, &bbolt.Options{Timeout: 2 * time.Second})
	require.NoError(t, err, "open bolt for mapping corruption: %s", boltPath)
	require.NoError(t, db.Update(func(tx *bbolt.Tx) error {
		snaps := tx.Bucket([]byte{'s'})
		require.NotNil(t, snaps)
		var lastKey []byte
		require.NoError(t, snaps.ForEach(func(k, _ []byte) error {
			lastKey = append(lastKey[:0], k...)
			return nil
		}))
		snap := snaps.Bucket(lastKey)
		require.NotNil(t, snap)
		internal := snap.Bucket([]byte{'i'})
		require.NotNil(t, internal)
		return internal.Put([]byte("_mapping"), []byte{})
	}))
	require.NoError(t, db.Close())
}
