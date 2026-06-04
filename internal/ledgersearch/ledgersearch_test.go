package ledgersearch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

func TestSearch_SessionMatch(t *testing.T) {
	t.Parallel()
	dir := makeLedger(t)
	now := time.Now().UTC()
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
	now := time.Now().UTC()
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
	now := time.Now().UTC()
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
	now := time.Now().UTC()
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
	now := time.Now().UTC()
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
	now := time.Now().UTC()
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
	now := time.Now().UTC()
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
	now := time.Now().UTC()
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
	now := time.Now().UTC()
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
	now := time.Now().UTC()
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
	now := time.Now().UTC()
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
