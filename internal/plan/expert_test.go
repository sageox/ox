package plan

import (
	"context"
	"strings"
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return tm.UTC()
}

func TestRankExperts(t *testing.T) {
	may := mustTime(t, "2026-05-10")
	apr := mustTime(t, "2026-04-01")
	jun := mustTime(t, "2026-06-01")

	tests := []struct {
		name  string
		stats []authorStat
		// expectations keyed by expert name
		wantExperts map[string]struct {
			files     []string
			source    string
			whyHas    []string // substrings required in Why
			whyMisses []string
		}
		wantCount int
	}{
		{
			name:        "empty input routes nobody",
			stats:       nil,
			wantExperts: nil,
			wantCount:   0,
		},
		{
			name: "single dominant author routes with PR citation",
			stats: []authorStat{
				{Author: "ajit", Path: "internal/auth/token.go", Commits: 9, LastTouched: may, CiteHash: "abc123", CitePRURL: "https://git/pr/42"},
				{Author: "dana", Path: "internal/auth/token.go", Commits: 2, LastTouched: apr, CiteHash: "def456"},
			},
			wantExperts: map[string]struct {
				files     []string
				source    string
				whyHas    []string
				whyMisses []string
			}{
				"ajit": {
					files:  []string{"internal/auth/token.go"},
					source: "https://git/pr/42",
					whyHas: []string{"ajit owns", "9 commits", "May 2026"},
				},
			},
			wantCount: 1,
		},
		{
			name: "commit-hash citation when no PR url",
			stats: []authorStat{
				{Author: "rupak", Path: "internal/codedb/store.go", Commits: 5, LastTouched: jun, CiteHash: "deadbeef"},
			},
			wantExperts: map[string]struct {
				files     []string
				source    string
				whyHas    []string
				whyMisses []string
			}{
				"rupak": {
					files:  []string{"internal/codedb/store.go"},
					source: "commit:deadbeef",
					whyHas: []string{"rupak owns", "5 commits"},
				},
			},
			wantCount: 1,
		},
		{
			name: "no citable artifact omits SourceURL but still routes",
			stats: []authorStat{
				{Author: "sam", Path: "internal/x/y.go", Commits: 4, LastTouched: may},
			},
			wantExperts: map[string]struct {
				files     []string
				source    string
				whyHas    []string
				whyMisses []string
			}{
				"sam": {
					files:  []string{"internal/x/y.go"},
					source: "",
					whyHas: []string{"sam owns"},
				},
			},
			wantCount: 1,
		},
		{
			name: "below-threshold ownership is dropped",
			stats: []authorStat{
				{Author: "drive-by", Path: "internal/z/q.go", Commits: 1, LastTouched: may, CiteHash: "h"},
			},
			wantExperts: nil,
			wantCount:   0,
		},
		{
			name: "multiple files under one expert share a common area",
			stats: []authorStat{
				{Author: "ajit", Path: "internal/auth/token.go", Commits: 9, LastTouched: may, CiteHash: "a1"},
				{Author: "ajit", Path: "internal/auth/refresh.go", Commits: 6, LastTouched: jun, CiteHash: "a2", CitePRURL: "https://git/pr/77"},
			},
			wantExperts: map[string]struct {
				files     []string
				source    string
				whyHas    []string
				whyMisses []string
			}{
				"ajit": {
					files:  []string{"internal/auth/refresh.go", "internal/auth/token.go"},
					source: "https://git/pr/77", // from the most-recently-touched owned path (jun)
					whyHas: []string{"internal/auth/", "15 commits", "Jun 2026"},
				},
			},
			wantCount: 1,
		},
		{
			name: "two experts each route their own area, sorted by name",
			stats: []authorStat{
				{Author: "rupak", Path: "internal/codedb/store.go", Commits: 8, LastTouched: jun, CiteHash: "r1"},
				{Author: "ajit", Path: "internal/auth/token.go", Commits: 5, LastTouched: may, CiteHash: "a1"},
			},
			wantExperts: map[string]struct {
				files     []string
				source    string
				whyHas    []string
				whyMisses []string
			}{
				"ajit":  {files: []string{"internal/auth/token.go"}, source: "commit:a1", whyHas: []string{"ajit owns"}},
				"rupak": {files: []string{"internal/codedb/store.go"}, source: "commit:r1", whyHas: []string{"rupak owns"}},
			},
			wantCount: 2,
		},
		{
			name: "dominant author wins on commit count, not recency",
			stats: []authorStat{
				{Author: "veteran", Path: "internal/core/engine.go", Commits: 20, LastTouched: apr, CiteHash: "v1"},
				{Author: "newcomer", Path: "internal/core/engine.go", Commits: 3, LastTouched: jun, CiteHash: "n1"},
			},
			wantExperts: map[string]struct {
				files     []string
				source    string
				whyHas    []string
				whyMisses []string
			}{
				"veteran": {files: []string{"internal/core/engine.go"}, source: "commit:v1", whyHas: []string{"veteran owns", "20 commits"}},
			},
			wantCount: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := rankExperts(tc.stats)
			if len(got) != tc.wantCount {
				t.Fatalf("rankExperts returned %d annotations, want %d: %+v", len(got), tc.wantCount, got)
			}
			gotByExpert := make(map[string]Annotation, len(got))
			for _, a := range got {
				if a.Kind != BadgeDeterministic {
					t.Errorf("expert %q: Kind=%q, want %q", a.Expert, a.Kind, BadgeDeterministic)
				}
				if a.Type != BadgeExpertRoute {
					t.Errorf("expert %q: Type=%q, want %q", a.Expert, a.Type, BadgeExpertRoute)
				}
				gotByExpert[a.Expert] = a
			}

			// outputs are sorted by expert name
			for i := 1; i < len(got); i++ {
				if got[i-1].Expert > got[i].Expert {
					t.Errorf("annotations not sorted by expert: %q before %q", got[i-1].Expert, got[i].Expert)
				}
			}

			for name, want := range tc.wantExperts {
				a, ok := gotByExpert[name]
				if !ok {
					t.Fatalf("expected expert %q routed, missing", name)
				}
				if !equalStrings(a.Files, want.files) {
					t.Errorf("expert %q: Files=%v, want %v", name, a.Files, want.files)
				}
				if a.SourceURL != want.source {
					t.Errorf("expert %q: SourceURL=%q, want %q", name, a.SourceURL, want.source)
				}
				for _, sub := range want.whyHas {
					if !strings.Contains(a.Why, sub) {
						t.Errorf("expert %q: Why=%q missing %q", name, a.Why, sub)
					}
				}
				for _, sub := range want.whyMisses {
					if strings.Contains(a.Why, sub) {
						t.Errorf("expert %q: Why=%q must not contain %q", name, a.Why, sub)
					}
				}
			}
		})
	}
}

func TestCitationNeverFabricates(t *testing.T) {
	// no real artifact -> empty, caller omits SourceURL
	if got := citation("", ""); got != "" {
		t.Errorf("citation with no artifact = %q, want empty", got)
	}
	// PR url wins over commit hash
	if got := citation("https://git/pr/1", "abc"); got != "https://git/pr/1" {
		t.Errorf("citation = %q, want PR url", got)
	}
	if got := citation("", "abc"); got != "commit:abc" {
		t.Errorf("citation = %q, want commit:abc", got)
	}
}

func TestCommonArea(t *testing.T) {
	tests := []struct {
		files []string
		want  string
	}{
		{nil, "these files"},
		{[]string{"internal/auth/token.go"}, "internal/auth/token.go"},
		{[]string{"internal/auth/a.go", "internal/auth/b.go"}, "internal/auth/"},
		{[]string{"internal/auth/a.go", "internal/codedb/b.go"}, "internal/"},
		{[]string{"cmd/ox/a.go", "internal/auth/b.go"}, "these files"},
		{[]string{"README.md", "CHANGELOG.md"}, "these files"},
	}
	for _, tc := range tests {
		if got := commonArea(tc.files); got != tc.want {
			t.Errorf("commonArea(%v) = %q, want %q", tc.files, got, tc.want)
		}
	}
}

func TestBetterOwner(t *testing.T) {
	may := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	jun := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// higher commit count wins
	if !betterOwner(authorStat{Author: "a", Commits: 5, LastTouched: may}, 3, jun, "b") {
		t.Error("higher commit count should win")
	}
	// lower commit count loses even if more recent
	if betterOwner(authorStat{Author: "a", Commits: 2, LastTouched: jun}, 5, may, "b") {
		t.Error("lower commit count should lose")
	}
	// tie on commits -> recency wins
	if !betterOwner(authorStat{Author: "a", Commits: 5, LastTouched: jun}, 5, may, "b") {
		t.Error("on tie, more recent should win")
	}
	// tie on commits and recency -> lexicographically smaller author wins (stable)
	if !betterOwner(authorStat{Author: "aaa", Commits: 5, LastTouched: may}, 5, may, "bbb") {
		t.Error("on full tie, smaller author name should win")
	}
}

func TestPlanFiles(t *testing.T) {
	in := Input{
		Sections: []Section{
			{Files: []string{"internal/auth/token.go", "internal/auth/token.go:42"}},
			{Files: []string{"cmd/ox/plan.go", "  "}},
			{Files: nil},
		},
	}
	got := expertPlanFiles(in)
	want := []string{"cmd/ox/plan.go", "internal/auth/token.go"}
	if !equalStrings(got, want) {
		t.Errorf("expertPlanFiles = %v, want %v", got, want)
	}
}

func TestDetectFailsOpenOnEmptyPlan(t *testing.T) {
	d := &expertDetector{}
	// empty plan, no files -> (nil, nil), never an error
	anns, err := d.Detect(context.Background(), Input{}, "/nonexistent/root")
	if err != nil {
		t.Fatalf("Detect returned error on empty plan: %v", err)
	}
	if anns != nil {
		t.Errorf("Detect returned annotations on empty plan: %v", anns)
	}
}

func TestDetectFailsOpenOnMissingIndex(t *testing.T) {
	d := &expertDetector{}
	in := Input{
		Sections: []Section{{Files: []string{"internal/auth/token.go"}}},
	}
	// gitRoot that resolves to no codedb index -> fail open, no error
	anns, err := d.Detect(context.Background(), in, t.TempDir())
	if err != nil {
		t.Fatalf("Detect returned error on missing index: %v", err)
	}
	if anns != nil {
		t.Errorf("Detect returned annotations with no index: %v", anns)
	}
}

func TestExpertDetectorRegistered(t *testing.T) {
	ds, _ := snapshotRegistry()
	found := false
	for _, d := range ds {
		if d.Name() == "expert-routing" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expertDetector not registered via init()")
	}
}

func equalStrings(a, b []string) bool {
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
