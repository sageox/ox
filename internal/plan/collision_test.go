package plan

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/ledger"
)

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "internal/auth/token.go", "internal/auth/token.go"},
		{"line suffix", "internal/auth/token.go:42", "internal/auth/token.go"},
		{"dot-slash prefix", "./cmd/ox/plan.go", "cmd/ox/plan.go"},
		{"whitespace", "  internal/x.go  ", "internal/x.go"},
		{"colon non-numeric kept", "pkg:thing.go", "pkg:thing.go"},
		{"dir trailing slash kept", "internal/auth/", "internal/auth/"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizePath(tc.in); got != tc.want {
				t.Errorf("normalizePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPlanFiles_DedupeNormalizeSort(t *testing.T) {
	in := Input{Sections: []Section{
		{Heading: "A", Files: []string{"internal/b.go:10", "internal/a.go"}},
		{Heading: "B", Files: []string{"internal/a.go", "./internal/c.go"}},
	}}
	got := planFiles(in)
	want := []string{"internal/a.go", "internal/b.go", "internal/c.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("planFiles = %v, want %v", got, want)
	}
}

func TestMatchPlanFile(t *testing.T) {
	want := map[string]struct{}{
		"internal/auth/token.go": {},
		"cmd/ox/plan.go":         {},
	}
	tests := []struct {
		name      string
		murmurRef string
		wantFile  string
		wantOK    bool
	}{
		{"exact", "internal/auth/token.go", "internal/auth/token.go", true},
		{"dir with slash", "internal/auth/", "internal/auth/token.go", true},
		{"dir without slash", "internal/auth", "internal/auth/token.go", true},
		{"line suffix", "internal/auth/token.go:7", "internal/auth/token.go", true},
		{"suffix path", "src/cmd/ox/plan.go", "cmd/ox/plan.go", true},
		{"no match", "internal/other/x.go", "", false},
		{"empty", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file, ok := matchPlanFile(tc.murmurRef, want)
			if ok != tc.wantOK || file != tc.wantFile {
				t.Errorf("matchPlanFile(%q) = (%q,%v), want (%q,%v)",
					tc.murmurRef, file, ok, tc.wantFile, tc.wantOK)
			}
		})
	}
}

func TestTopicMatchesHeading(t *testing.T) {
	headings := []string{"Refactor authentication flow", "Add caching layer"}
	tests := []struct {
		name  string
		topic string
		want  bool
	}{
		{"shared long token", "authentication-tokens", true},
		{"shared caching", "caching", true},
		{"only short tokens", "the api io", false},
		{"unrelated", "unrelated stuff here today", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := topicMatchesHeading(tc.topic, headings); got != tc.want {
				t.Errorf("topicMatchesHeading(%q) = %v, want %v", tc.topic, got, tc.want)
			}
		})
	}
}

func TestHumanizeAgo(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"just now", 10 * time.Second, "just now"},
		{"minutes", 5 * time.Minute, "5m ago"},
		{"hours", 3 * time.Hour, "3h ago"},
		{"days", 50 * time.Hour, "2d ago"},
		{"negative clamps", -time.Hour, "just now"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := humanizeAgo(tc.d); got != tc.want {
				t.Errorf("humanizeAgo(%v) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}

func TestMurmurFiles(t *testing.T) {
	tests := []struct {
		name string
		meta map[string]string
		want []string
	}{
		{"comma separated", map[string]string{"files": "a.go, b.go"}, []string{"a.go", "b.go"}},
		{"newline separated", map[string]string{"files": "a.go\nb.go"}, []string{"a.go", "b.go"}},
		{"missing key", map[string]string{}, nil},
		{"empty value", map[string]string{"files": "  "}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := murmurFiles(ledger.MurmurFile{Metadata: tc.meta})
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("murmurFiles = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMatchMurmurs(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	files := []string{"internal/auth/token.go", "cmd/ox/plan.go"}
	headings := []string{"Refactor authentication flow"}

	tests := []struct {
		name        string
		murmurs     []ledger.MurmurFile
		wantCount   int
		wantSubstr  string
		wantExperts []string
	}{
		{
			name: "explicit file match",
			murmurs: []ledger.MurmurFile{{
				ID:          "m1",
				PrincipalID: "bob",
				Timestamp:   now.Add(-3 * time.Hour),
				Metadata:    map[string]string{"files": "internal/auth/token.go"},
			}},
			wantCount:   1,
			wantSubstr:  "bob murmured editing internal/auth/token.go 3h ago",
			wantExperts: []string{"bob"},
		},
		{
			name: "directory murmur matches file under it",
			murmurs: []ledger.MurmurFile{{
				ID:          "m2",
				PrincipalID: "alice",
				Timestamp:   now.Add(-30 * time.Minute),
				Metadata:    map[string]string{"files": "internal/auth/"},
			}},
			wantCount:  1,
			wantSubstr: "alice murmured editing internal/auth/token.go 30m ago",
		},
		{
			name: "topic-heading overlap with no file list",
			murmurs: []ledger.MurmurFile{{
				ID:          "m3",
				PrincipalID: "carol",
				Timestamp:   now.Add(-1 * time.Hour),
				Topic:       "authentication-rewrite",
			}},
			wantCount:  1,
			wantSubstr: "carol murmured on topic",
		},
		{
			name: "no match — unrelated file and topic",
			murmurs: []ledger.MurmurFile{{
				ID:          "m4",
				PrincipalID: "dave",
				Timestamp:   now.Add(-1 * time.Hour),
				Topic:       "unrelated work",
				Metadata:    map[string]string{"files": "internal/other/x.go"},
			}},
			wantCount: 0,
		},
		{
			name: "explicit match suppresses topic double-report",
			murmurs: []ledger.MurmurFile{{
				ID:          "m5",
				PrincipalID: "erin",
				Timestamp:   now.Add(-2 * time.Hour),
				Topic:       "authentication-rewrite",
				Metadata:    map[string]string{"files": "internal/auth/token.go"},
			}},
			wantCount:  1,
			wantSubstr: "erin murmured editing internal/auth/token.go 2h ago",
		},
		{
			name: "agent_id fallback when no principal",
			murmurs: []ledger.MurmurFile{{
				ID:        "m6",
				AgentID:   "agent-xyz",
				Timestamp: now.Add(-5 * time.Hour),
				Metadata:  map[string]string{"files": "cmd/ox/plan.go"},
			}},
			wantCount:   1,
			wantSubstr:  "agent-xyz murmured editing cmd/ox/plan.go 5h ago",
			wantExperts: []string{"agent-xyz"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := matchMurmurs(tc.murmurs, files, headings, now)
			if len(got) != tc.wantCount {
				t.Fatalf("matchMurmurs returned %d annotations, want %d: %+v", len(got), tc.wantCount, got)
			}
			for _, a := range got {
				if a.Kind != BadgeDeterministic || a.Type != BadgeCollision {
					t.Errorf("wrong badge kind/type: %+v", a)
				}
			}
			if tc.wantSubstr != "" {
				found := false
				for _, a := range got {
					if strings.Contains(a.Why, tc.wantSubstr) {
						found = true
					}
				}
				if !found {
					t.Errorf("no annotation Why contained %q; got %+v", tc.wantSubstr, got)
				}
			}
			for _, exp := range tc.wantExperts {
				found := false
				for _, a := range got {
					if a.Expert == exp {
						found = true
					}
				}
				if !found {
					t.Errorf("expected expert %q in annotations; got %+v", exp, got)
				}
			}
		})
	}
}

// --- codedb integration: tiny in-place SQLite fixture ---

func TestPRAndContentionCollisions(t *testing.T) {
	dir := t.TempDir()
	db, err := codedb.OpenSQLOnly(dir)
	if err != nil {
		t.Fatalf("OpenSQLOnly: %v", err)
	}
	defer func() { _ = db.Close() }()
	s := db.Store()

	seedCodeDBFixture(t, s)

	want := map[string]struct{}{
		"internal/auth/token.go": {},
		"cmd/ox/plan.go":         {}, // contended file
		"internal/unrelated.go":  {}, // present in plan but not in any signal
	}

	prAnns := prCollisions(s, want)
	if len(prAnns) != 1 {
		t.Fatalf("prCollisions returned %d, want 1: %+v", len(prAnns), prAnns)
	}
	pr := prAnns[0]
	if pr.Type != BadgeCollision || pr.Kind != BadgeDeterministic {
		t.Errorf("wrong badge: %+v", pr)
	}
	if !strings.Contains(pr.Why, "open PR #412") || !strings.Contains(pr.Why, "alice") {
		t.Errorf("unexpected PR Why: %q", pr.Why)
	}
	if pr.SourceURL != "https://example.test/pr/412" {
		t.Errorf("expected PR URL, got %q", pr.SourceURL)
	}
	if pr.Expert != "alice" {
		t.Errorf("expected expert alice, got %q", pr.Expert)
	}
	if len(pr.Files) != 1 || pr.Files[0] != "internal/auth/token.go" {
		t.Errorf("unexpected Files: %v", pr.Files)
	}

	contAnns := contentionCollisions(s, want)
	if len(contAnns) != 1 {
		t.Fatalf("contentionCollisions returned %d, want 1: %+v", len(contAnns), contAnns)
	}
	c := contAnns[0]
	if !strings.Contains(c.Why, "cmd/ox/plan.go") || !strings.Contains(c.Why, "contended") {
		t.Errorf("unexpected contention Why: %q", c.Why)
	}
}

func TestPRCollisions_NoMatchReturnsNil(t *testing.T) {
	dir := t.TempDir()
	db, err := codedb.OpenSQLOnly(dir)
	if err != nil {
		t.Fatalf("OpenSQLOnly: %v", err)
	}
	defer func() { _ = db.Close() }()
	s := db.Store()
	seedCodeDBFixture(t, s)

	// plan references a file no open PR touches
	want := map[string]struct{}{"docs/readme.md": {}}
	if got := prCollisions(s, want); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

// TestDetect_FailOpenNoIndex verifies Detect returns (nil,nil) when there is no
// codedb index and no ledger — the fail-open contract.
func TestDetect_FailOpenNoIndex(t *testing.T) {
	d := &collisionDetector{}
	in := Input{Sections: []Section{{Heading: "X", Files: []string{"internal/a.go"}}}}
	// a temp dir with no .sageox config resolves to an empty/absent codedb +
	// ledger, so both legs fail open.
	anns, err := d.Detect(context.Background(), in, t.TempDir())
	if err != nil {
		t.Fatalf("Detect returned error (must fail-open): %v", err)
	}
	if anns != nil {
		t.Errorf("expected nil annotations with no data, got %+v", anns)
	}
}

func TestDetect_NoFilesReturnsNil(t *testing.T) {
	d := &collisionDetector{}
	anns, err := d.Detect(context.Background(), Input{}, t.TempDir())
	if err != nil || anns != nil {
		t.Fatalf("expected (nil,nil), got (%+v,%v)", anns, err)
	}
}

// seedCodeDBFixture inserts a minimal but realistic graph:
//   - repo "ws-a" with a commit touching internal/auth/token.go, on open PR #412
//   - two repos (workspaces) both touching cmd/ox/plan.go recently -> contention
func seedCodeDBFixture(t *testing.T, s *store.Store) {
	t.Helper()
	now := time.Now().Unix()

	exec := func(q string, args ...any) {
		if _, err := s.Exec(q, args...); err != nil {
			t.Fatalf("seed exec failed: %v\nquery: %s", err, q)
		}
	}

	// repos / workspaces
	exec(`INSERT INTO repos(id, name, path) VALUES (1,'ws-a','/tmp/ws-a'),(2,'ws-b','/tmp/ws-b')`)

	// commits
	exec(`INSERT INTO commits(id, repo_id, hash, author, message, timestamp)
	      VALUES (1,1,'aaa111','alice','auth work', ?)`, now)
	exec(`INSERT INTO commits(id, repo_id, hash, author, message, timestamp)
	      VALUES (2,1,'bbb222','alice','plan in ws-a', ?)`, now)
	exec(`INSERT INTO commits(id, repo_id, hash, author, message, timestamp)
	      VALUES (3,2,'ccc333','bob','plan in ws-b', ?)`, now)

	// diffs: auth file (PR), plan.go touched by both workspaces (contention)
	exec(`INSERT INTO diffs(id, commit_id, path) VALUES (1,1,'internal/auth/token.go')`)
	exec(`INSERT INTO diffs(id, commit_id, path) VALUES (2,2,'cmd/ox/plan.go')`)
	exec(`INSERT INTO diffs(id, commit_id, path) VALUES (3,3,'cmd/ox/plan.go')`)

	// open PR #412 by alice, links to commit aaa111 via pr_commits
	exec(`INSERT INTO pull_requests(id, number, title, author, state, url)
	      VALUES (1,412,'auth refactor','alice','open','https://example.test/pr/412')`)
	exec(`INSERT INTO pr_commits(id, pr_id, sha) VALUES (1,1,'aaa111')`)
}
