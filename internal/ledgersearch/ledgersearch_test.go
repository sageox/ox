package ledgersearch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var ledgerSearchTestNow = time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)

func TestSearch_EmptyLedgerPath(t *testing.T) {
	t.Parallel()
	results, err := Search(Options{Query: "anything"})
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results, got %d", len(results))
	}
}

func TestSearch_NonexistentLedger(t *testing.T) {
	t.Parallel()
	results, err := Search(Options{
		LedgerPath: "/nonexistent/path/to/ledger",
		Query:      "anything",
	})
	if err != nil {
		t.Fatalf("expected fail-open, got err %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results, got %d", len(results))
	}
}

func TestSearch_EmptyQuery(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	results, err := Search(Options{LedgerPath: dir, Query: "  "})
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results for empty query")
	}
}

func TestSearch_StrictReadReportsCorruptMurmur(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := ledgerSearchTestNow
	murmurDir := filepath.Join(dir, "data", "murmurs", now.Format("2006-01-02"), now.Format("15"))
	if err := os.MkdirAll(murmurDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(murmurDir, "broken.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSession(t, dir, "2026-05-20T10-15-ryan-OxABCD", "oauth oauth oauth oauth oauth")

	if _, err := Search(Options{LedgerPath: dir, Query: "anything", Now: now}); err != nil {
		t.Fatalf("default search must remain fail-open: %v", err)
	}
	results, err := Search(Options{LedgerPath: dir, Query: "oauth", Now: now, StrictRead: true})
	if err == nil {
		t.Fatal("strict search must report corrupt source data")
	}
	if len(results) != 1 || results[0].DocType != "session" {
		t.Fatalf("strict search must preserve valid partial hits: %+v", results)
	}
	if _, err := Search(Options{LedgerPath: dir, Query: "oauth", Now: now, StrictRead: true, SkipMurmurs: true}); err != nil {
		t.Fatalf("durable-only search must ignore a corrupt murmur owned by another retriever: %v", err)
	}
}

func TestStrictScannersContinueAfterEntryFailure(t *testing.T) {
	t.Parallel()
	now := ledgerSearchTestNow
	tests := []struct {
		name         string
		wantSourceID string
		setup        func(*testing.T, string)
		scan         func(string) ([]Result, error)
	}{
		{
			name:         "sessions",
			wantSourceID: "2026-05-22T10-15-ryan-OxVALID",
			setup: func(t *testing.T, root string) {
				broken := filepath.Join(root, "sessions", "2026-05-21T10-15-ryan-OxBROKEN", "summary.md")
				if err := os.MkdirAll(broken, 0o755); err != nil {
					t.Fatal(err)
				}
				writeSession(t, root, "2026-05-22T10-15-ryan-OxVALID", "oauth oauth oauth")
			},
			scan: func(root string) ([]Result, error) {
				return scanSessions(root, tokenize("oauth"), now, true)
			},
		},
		{
			name:         "murmurs",
			wantSourceID: "z-valid",
			setup: func(t *testing.T, root string) {
				hourDir := filepath.Join(root, "data", "murmurs", now.Format("2006-01-02"), now.Format("15"))
				if err := os.MkdirAll(hourDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(hourDir, "a-broken.json"), []byte("{"), 0o644); err != nil {
					t.Fatal(err)
				}
				writeMurmur(t, root, now, "z-valid", "auth", "oauth oauth oauth")
			},
			scan: func(root string) ([]Result, error) {
				return scanMurmurs(root, tokenize("oauth"), now, true)
			},
		},
		{
			name:         "plans",
			wantSourceID: "2026-05-22-z-valid",
			setup: func(t *testing.T, root string) {
				broken := filepath.Join(root, "data", "plans", "2026-05-21-a-broken", "plan.md")
				if err := os.MkdirAll(broken, 0o755); err != nil {
					t.Fatal(err)
				}
				writePlan(t, root, "2026-05-22-z-valid", now, "auth", "ryan", "oauth oauth oauth")
			},
			scan: func(root string) ([]Result, error) {
				return scanPlans(root, tokenize("oauth"), now, true)
			},
		},
		{
			name:         "plan feedback",
			wantSourceID: "2026-05-22-auth-plan",
			setup: func(t *testing.T, root string) {
				fbDir := filepath.Join(root, "data", "plans", "2026-05-22-auth-plan", "feedback")
				if err := os.MkdirAll(fbDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(fbDir, "round-a-broken.json"), []byte("{"), 0o644); err != nil {
					t.Fatal(err)
				}
				round, err := json.Marshal(map[string]any{
					"reviewer":   "sam",
					"created_at": now,
					"items": []map[string]string{{
						"note": "oauth oauth oauth",
					}},
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(fbDir, "round-z-valid.json"), round, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			scan: func(root string) ([]Result, error) {
				return scanPlanFeedback(root, tokenize("oauth"), now, true)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			tc.setup(t, root)
			results, err := tc.scan(root)
			if err == nil {
				t.Fatal("strict scan must report the corrupt entry")
			}
			found := false
			for _, result := range results {
				found = found || result.SourceID == tc.wantSourceID
			}
			if !found {
				t.Fatalf("strict scan lost a valid sibling after the corrupt entry: %+v (err=%v)", results, err)
			}
		})
	}
}

func TestSearch_SessionMatch(t *testing.T) {
	t.Parallel()
	dir := makeLedger(t)
	now := ledgerSearchTestNow
	writeSession(t, dir, "2026-05-20T10-15-ryan-OxABCD", "# AuthN flow\n\nWe decided to use OAuth for the deployment pipeline.")

	results, err := Search(Options{LedgerPath: dir, Query: "oauth", Now: now})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.DocType != "session" {
		t.Errorf("doc_type = %q, want session", r.DocType)
	}
	if r.SourceType != "ledger" {
		t.Errorf("source_type = %q, want ledger", r.SourceType)
	}
	if r.SourceID != "2026-05-20T10-15-ryan-OxABCD" {
		t.Errorf("source_id mismatch: %q", r.SourceID)
	}
	if r.Score <= 0 {
		t.Errorf("expected positive score, got %f", r.Score)
	}
}

func TestSearch_NoMatches(t *testing.T) {
	t.Parallel()
	dir := makeLedger(t)
	writeSession(t, dir, "2026-05-20T10-15-ryan-OxABCD", "# Just a note about kubernetes")
	results, err := Search(Options{LedgerPath: dir, Query: "totally-absent-term"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestSearch_MultiResultRanking(t *testing.T) {
	t.Parallel()
	dir := makeLedger(t)
	now := ledgerSearchTestNow
	// session A: single mention
	writeSession(t, dir, "2026-05-19T10-15-ryan-OxAAAA", "We discussed cache.")
	// session B: many mentions — should rank higher
	writeSession(t, dir, "2026-05-20T10-15-ryan-OxBBBB", "cache cache cache. The cache is the cache of caches.")

	results, err := Search(Options{LedgerPath: dir, Query: "cache", Now: now, Limit: 10})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].SourceID != "2026-05-20T10-15-ryan-OxBBBB" {
		t.Errorf("expected higher-tf session first, got %q", results[0].SourceID)
	}
}

func TestSearch_RespectsLimit(t *testing.T) {
	t.Parallel()
	dir := makeLedger(t)
	now := ledgerSearchTestNow
	for i := 0; i < 5; i++ {
		writeSession(t, dir, "2026-05-2"+string(rune('0'+i))+"T10-15-ryan-OxX"+string(rune('A'+i)), "matches the term widget")
	}
	results, err := Search(Options{LedgerPath: dir, Query: "widget", Limit: 2, Now: now})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results (limit), got %d", len(results))
	}
}

func TestSearch_AgeCutoff(t *testing.T) {
	t.Parallel()
	dir := makeLedger(t)
	now := ledgerSearchTestNow
	// way in the past
	writeSession(t, dir, "2020-01-01T10-15-ryan-OxOLDD", "ancient topic widget mention here")
	results, err := Search(Options{LedgerPath: dir, Query: "widget", Now: now})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected age-cutoff to exclude, got %d results", len(results))
	}
}

func TestSearch_MurmurMatch(t *testing.T) {
	t.Parallel()
	dir := makeLedger(t)
	now := ledgerSearchTestNow
	writeMurmur(t, dir, now.Add(-1*time.Hour), "murmur-1", "wip", "Working on the local query path for ledger search.")

	results, err := Search(Options{LedgerPath: dir, Query: "ledger", Now: now})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].DocType != "murmur" {
		t.Errorf("doc_type = %q, want murmur", results[0].DocType)
	}
	if results[0].SourceID != "murmur-1" {
		t.Errorf("source_id = %q", results[0].SourceID)
	}
}

func TestSearch_MultiTermANDSemantics(t *testing.T) {
	t.Parallel()
	dir := makeLedger(t)
	now := ledgerSearchTestNow
	writeSession(t, dir, "2026-05-20T10-15-ryan-OxOnly1", "only the first term: cache.")
	writeSession(t, dir, "2026-05-20T11-15-ryan-OxBothTT", "Both cache and oauth appear here.")

	results, err := Search(Options{LedgerPath: dir, Query: "cache oauth", Now: now})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 (AND) result, got %d", len(results))
	}
	if results[0].SourceID != "2026-05-20T11-15-ryan-OxBothTT" {
		t.Errorf("wrong result: %q", results[0].SourceID)
	}
}

// --- Plans: the prior-art flywheel ---
// A plan saved by `ox plan` MUST resurface as prior art in future plans.
// Failure prevented: plans saved to data/plans/ are invisible to the prior-art
// detector, so the flywheel never closes and every plan looks novel.

func TestSearch_PlanMatch(t *testing.T) {
	t.Parallel()
	dir := makeLedger(t)
	now := ledgerSearchTestNow
	writePlan(t, dir, "2026-05-21-cache-layer", now.Add(-24*time.Hour),
		"Add cache layer", "ryan",
		"# Add cache layer\n\nWe will add an in-memory cache to the query path.")

	results, err := Search(Options{LedgerPath: dir, Query: "cache", Now: now})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 plan result, got %d", len(results))
	}
	r := results[0]
	if r.DocType != "plan" {
		t.Errorf("doc_type = %q, want plan", r.DocType)
	}
	if r.SourceType != "ledger" {
		t.Errorf("source_type = %q, want ledger", r.SourceType)
	}
	// SourceID must be the dated-slug dir so a reader can `ox plan view` it.
	if r.SourceID != "2026-05-21-cache-layer" {
		t.Errorf("source_id = %q, want dated slug", r.SourceID)
	}
	if r.Score <= 0 {
		t.Errorf("expected positive score, got %f", r.Score)
	}
	// created_at must come from meta.json (the day before now), not zero.
	if r.CreatedAt == "" {
		t.Errorf("expected created_at from meta.json, got empty")
	}
}

func TestSearch_PlanFailOpenNoPlansDir(t *testing.T) {
	t.Parallel()
	// ledger with sessions/ but no data/plans/ — scanning plans must not error.
	dir := makeLedger(t)
	now := ledgerSearchTestNow
	writeSession(t, dir, "2026-05-20T10-15-ryan-OxABCD", "a session about widgets")

	results, err := Search(Options{LedgerPath: dir, Query: "widgets", Now: now})
	if err != nil {
		t.Fatalf("expected fail-open with no plans dir, got err %v", err)
	}
	// the session still matches; the absence of data/plans/ must not break it.
	if len(results) != 1 || results[0].DocType != "session" {
		t.Fatalf("expected the session result to survive, got %+v", results)
	}
}

func TestSearch_PlanVsSessionDistinguishable(t *testing.T) {
	t.Parallel()
	dir := makeLedger(t)
	now := ledgerSearchTestNow
	// same term in both a session and a plan: results must be tagged distinctly
	// so the prior-art detector can phrase "planned" vs "worked on".
	writeSession(t, dir, "2026-05-20T10-15-ryan-OxSESS", "deployment pipeline notes")
	writePlan(t, dir, "2026-05-22-deployment-rework", now.Add(-2*time.Hour),
		"Deployment rework", "ajit",
		"# Deployment rework\n\nRebuild the deployment pipeline end to end.")

	results, err := Search(Options{LedgerPath: dir, Query: "deployment", Now: now, Limit: 10})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	byType := map[string]Result{}
	for _, r := range results {
		byType[r.DocType] = r
	}
	if _, ok := byType["session"]; !ok {
		t.Errorf("expected a session result, got types %v", keys(byType))
	}
	plan, ok := byType["plan"]
	if !ok {
		t.Fatalf("expected a plan result, got types %v", keys(byType))
	}
	if plan.SourceID != "2026-05-22-deployment-rework" {
		t.Errorf("plan source_id = %q, want dated slug", plan.SourceID)
	}
}

func TestSearch_PlanAgeCutoff(t *testing.T) {
	t.Parallel()
	dir := makeLedger(t)
	now := ledgerSearchTestNow
	// meta.json created_at far in the past — must be excluded by MaxPlanAge.
	writePlan(t, dir, "2020-01-01-ancient-plan", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		"Ancient plan", "ryan", "# Ancient plan\n\nwidget overhaul")

	results, err := Search(Options{LedgerPath: dir, Query: "widget", Now: now})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected age-cutoff to exclude old plan, got %d", len(results))
	}
}

func TestSearch_PlanDateFallsBackToDirName(t *testing.T) {
	t.Parallel()
	// plan dir with no meta.json: age filter + created_at fall back to the
	// YYYY-MM-DD dir prefix so a partial plan dir is still searchable/datable.
	dir := makeLedger(t)
	now := ledgerSearchTestNow
	planDir := filepath.Join(dir, "data", "plans", "2026-05-23-no-meta-plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir plan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "plan.md"),
		[]byte("# No meta\n\ntelemetry rollout"), 0o644); err != nil {
		t.Fatalf("write plan.md: %v", err)
	}

	results, err := Search(Options{LedgerPath: dir, Query: "telemetry", Now: now})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result from meta-less plan, got %d", len(results))
	}
	if results[0].CreatedAt == "" {
		t.Errorf("expected created_at derived from dir name, got empty")
	}
}

func TestParseSessionTimestamp(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"2026-02-13T14-56-ajit-OxmoZK": true,
		"not-a-session":                false,
		"2026-02-13X14-56-ajit-OxmoZK": false,
		"":                             false,
		"2026-02-13T14:56-ryan-Oxabcd": false, // colon present, our format uses dashes
	}
	for name, shouldParse := range cases {
		ts := parseSessionTimestamp(name)
		if shouldParse && ts.IsZero() {
			t.Errorf("expected %q to parse, got zero", name)
		}
		if !shouldParse && !ts.IsZero() {
			t.Errorf("expected %q to fail, got %v", name, ts)
		}
	}
}

func TestTokenize(t *testing.T) {
	t.Parallel()
	got := tokenize("Hello, world! foo-bar 2026")
	want := []string{"hello", "world", "foo-bar", "2026"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("token[%d] = %q want %q", i, got[i], want[i])
		}
	}
}

// --- helpers ---

func makeLedger(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	return dir
}

func writeSession(t *testing.T, ledgerPath, sessionName, summaryMD string) {
	t.Helper()
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "summary.md"), []byte(summaryMD), 0o644); err != nil {
		t.Fatalf("write summary: %v", err)
	}
}

// writePlan materializes a captured-plan dir mirroring internal/plan/store.go's
// layout: data/plans/<dated-slug>/{plan.md,meta.json}. plan.md is the canonical
// searchable text; meta.json carries the authoritative created_at.
func writePlan(t *testing.T, ledgerPath, dirName string, createdAt time.Time, topic, author, planMD string) {
	t.Helper()
	planDir := filepath.Join(ledgerPath, "data", "plans", dirName)
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir plan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "plan.md"), []byte(planMD), 0o644); err != nil {
		t.Fatalf("write plan.md: %v", err)
	}
	meta := map[string]any{
		"topic":      topic,
		"slug":       dirName,
		"authors":    []string{author},
		"created_at": createdAt,
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(planDir, "meta.json"), data, 0o644); err != nil {
		t.Fatalf("write meta.json: %v", err)
	}
}

// keys returns the map keys for a clearer test failure message.
func keys(m map[string]Result) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func writeMurmur(t *testing.T, ledgerPath string, ts time.Time, id, topic, content string) {
	t.Helper()
	hourDir := filepath.Join(
		ledgerPath, "data", "murmurs",
		ts.UTC().Format("2006-01-02"),
		ts.UTC().Format("15"),
	)
	if err := os.MkdirAll(hourDir, 0o755); err != nil {
		t.Fatalf("mkdir murmur: %v", err)
	}
	m := map[string]any{
		"schema_version": "1",
		"id":             id,
		"timestamp":      ts,
		"topic":          topic,
		"content":        content,
	}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(hourDir, id+".json"), data, 0o644); err != nil {
		t.Fatalf("write murmur: %v", err)
	}
}

// --- Snippet word-boundary extraction (A3) ---

// TestSnippetAround_NoMidWordTruncation verifies the snippet window snaps to
// whole-word boundaries so it never begins or ends mid-word.
// Failure prevented: the byte-window slicing that produced citations like
// "...HLS retry semantics" / "...ive intelligence_1" — fragments clipped mid-word
// at both ends that read as noise in the rendered plan.
func TestSnippetAround_NoMidWordTruncation(t *testing.T) {
	// match "retry" mid-sentence; a small window would otherwise clip "...HLS"
	// on the left and a word on the right.
	content := "the firmware handles HLS retry semantics carefully before upload begins"
	idx := strings.Index(content, "retry")
	got := snippetAround(content, idx, 24)

	trimmed := strings.Trim(got, ".")
	if trimmed == "" {
		t.Fatalf("empty snippet for idx=%d", idx)
	}
	// every word in the snippet body must be a whole word from the source.
	srcWords := make(map[string]struct{})
	for _, w := range strings.Fields(content) {
		srcWords[w] = struct{}{}
	}
	for _, w := range strings.Fields(trimmed) {
		if _, ok := srcWords[w]; !ok {
			t.Errorf("snippet word %q is not a whole source word (got %q)", w, got)
		}
	}
}

// TestSnippetAround_UTF8Safe verifies multi-byte runes are never split.
// Failure prevented: byte slicing through a multi-byte rune yielding a broken
// "" replacement character in the snippet.
func TestSnippetAround_UTF8Safe(t *testing.T) {
	content := "café résumé naïve façade déjà vu coöperate piñata über"
	for idx := 0; idx < len(content); idx++ {
		got := snippetAround(content, idx, 16)
		if !utf8.ValidString(got) {
			t.Fatalf("invalid UTF-8 snippet at idx=%d: %q", idx, got)
		}
	}
}

// TestSnippetAround_Markers verifies elision markers appear only where text was
// actually cut.
func TestSnippetAround_Markers(t *testing.T) {
	content := "alpha beta gamma delta epsilon zeta eta theta iota"
	// match at the very start — no left elision.
	got := snippetAround(content, 0, 16)
	if strings.HasPrefix(got, "...") {
		t.Errorf("unexpected left marker when matching at start: %q", got)
	}
	// whole string fits — no markers at all.
	full := snippetAround(content, 0, len(content)+10)
	if strings.Contains(full, "...") {
		t.Errorf("unexpected marker when window covers whole string: %q", full)
	}
}

// TestScoreContent_DensityNotLength is the regression for the length bias that
// made `ox plan enrich` surface unrelated sessions at 0.95: a long document
// containing every query term scattered incidentally must NOT out-score (or
// even approach) a short document that is actually about those terms.
//
// Failure prevented: restoring the un-normalized totalHits/100 bonus. Both
// fixtures below saturate the maxHitsPerTerm scan cap, so under the old formula
// they score identically (0.800) and the threshold assertion goes red — the
// earlier version of this test used a scattered doc with only 6 total hits,
// which the old formula scored 0.56 and which therefore passed either way.
func TestScoreContent_DensityNotLength(t *testing.T) {
	terms := []string{"mcp", "doctrine", "priming"}

	// short and on-topic: repeats the terms in ~1 KB of content
	focused := strings.Repeat("mcp doctrine priming shapes the mcp surface. ", 22)

	// long and incidental: each term appears maxHitsPerTerm times, dispersed
	// through ~100 KB, so the scan cap saturates exactly as it does for focused.
	filler := strings.Repeat("unrelated transcript chatter about run jobs and browsers. ", 170)
	scattered := strings.Repeat("mcp doctrine priming "+filler, maxHitsPerTerm)

	fs, fsn := scoreContent(focused, terms)
	ss, ssn := scoreContent(scattered, terms)
	if fsn == "" || ssn == "" {
		t.Fatal("expected snippets for matching content")
	}
	// Checked first: this is the assertion the contract above is about, and it
	// fires on the old formula before the ordering comparison can mask it.
	if ss >= 0.6 {
		t.Fatalf("scattered long doc score = %.3f, want < 0.6 (the caller threshold it previously blew through)", ss)
	}
	if fs <= ss {
		t.Fatalf("focused doc must out-score scattered long doc: focused=%.3f scattered=%.3f", fs, ss)
	}
	if fs < 0.6 {
		t.Fatalf("focused doc score = %.3f, want >= 0.6", fs)
	}
}

// TestScoreContent_LargeOnTopicDocStillSurfaces guards the other side of the
// density fix. Normalizing by document size interacts with the maxHitsPerTerm
// scan cap to impose a hard size ceiling: extraHits is bounded, so past some
// byte count NO document can clear a caller's threshold however on-topic it is.
//
// Failure prevented: raising tfDensityDivisor back toward 20, which put that
// ceiling at ~13.5 KB for a 3-term query — below the p90 of this repo's own
// saved plans, so 14% of them became permanently unfindable as prior art.
func TestScoreContent_LargeOnTopicDocStillSurfaces(t *testing.T) {
	terms := []string{"mcp", "doctrine", "priming"}

	// ~20 KB and densely on-topic: the terms recur throughout, not just once.
	unit := "mcp doctrine priming drive the design here. " +
		strings.Repeat("supporting prose that carries the argument forward. ", 8)
	dense := strings.Repeat(unit, 40)
	if len(dense) < 15*1024 {
		t.Fatalf("fixture too small to exercise the ceiling: %d bytes", len(dense))
	}

	// same size, but the terms appear once each — genuinely tangential.
	tangential := "mcp doctrine priming\n" +
		strings.Repeat("prose with no bearing on the query at all. ", 480)

	ds, _ := scoreContent(dense, terms)
	ts, _ := scoreContent(tangential, terms)

	if ds < 0.6 {
		t.Fatalf("dense %d-byte on-topic doc scored %.3f, want >= 0.6 — the density divisor has re-introduced a size ceiling", len(dense), ds)
	}
	if ts >= 0.6 {
		t.Fatalf("tangential %d-byte doc scored %.3f, want < 0.6", len(tangential), ts)
	}
	if ds <= ts {
		t.Fatalf("dense doc must out-score tangential doc of the same size: dense=%.3f tangential=%.3f", ds, ts)
	}
}
