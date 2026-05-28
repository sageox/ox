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
