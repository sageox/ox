package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSplitSessionPath verifies the relative-path parser used to map
// "sessions/<name>/<file>" into its components. Wrong attribution here
// means a session's redaction pass lands in the wrong meta.json.
func TestSplitSessionPath(t *testing.T) {
	tests := []struct {
		in      string
		session string
		file    string
		ok      bool
	}{
		{"sessions/2026-05-11-abc/raw.jsonl", "2026-05-11-abc", "raw.jsonl", true},
		{"sessions/X/meta.json", "X", "meta.json", true},
		{"sessions/X/sub/nested.jsonl", "", "", false},   // nested unsupported
		{"data/github/pr/1.json", "", "", false},         // wrong prefix
		{"sessions//raw.jsonl", "", "", false},           // empty session name
		{"sessions/onlysession/", "", "", false},         // trailing slash, no file
		{"sessions/onlysession", "", "", false},          // no filename
	}
	for _, tt := range tests {
		s, f, ok := splitSessionPath(tt.in)
		assert.Equal(t, tt.ok, ok, "ok mismatch for %q", tt.in)
		if tt.ok {
			assert.Equal(t, tt.session, s)
			assert.Equal(t, tt.file, f)
		}
	}
}

// TestSummarizeRedactionEntries verifies the per-detector aggregation
// shape that lands in meta.json (and in agent-visible terminal output).
func TestSummarizeRedactionEntries(t *testing.T) {
	entries := []lfs.RedactionEntry{
		{File: "raw.jsonl", Line: 1, Detector: "github_personal_access_token"},
		{File: "raw.jsonl", Line: 42, Detector: "url_with_password"},
		{File: "raw.jsonl", Line: 99, Detector: "url_with_password"},
		{File: "summary.md", Line: 3, Detector: "aws_access_key"},
	}
	s := summarizeRedactionEntries(entries)
	assert.Equal(t, 4, s.Total)
	assert.Equal(t, map[string]int{
		"github_personal_access_token": 1,
		"url_with_password":            2,
		"aws_access_key":               1,
	}, s.ByDetector)
}

// TestWriteRedactionPassesPerSession_AppendsToMeta verifies the
// load-bearing ox-8bfh property: a redaction pass appends a new entry
// to meta.json.Redactions rather than overwriting prior history.
//
// Failure prevented: a second redact-history run replacing the first
// pass's record — destroying the audit trail.
func TestWriteRedactionPassesPerSession_AppendsToMeta(t *testing.T) {
	ledger := t.TempDir()
	sessName := "2026-05-11-test-OxABCD"
	sessDir := filepath.Join(ledger, "sessions", sessName)
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	// Seed a minimum-valid meta.json. The Validate() guard in
	// WriteSessionMetaOnly insists on a session id + agent type; mock
	// enough fields to pass.
	seed := &lfs.SessionMeta{
		Version:     "1.0",
		SessionName: sessName,
		Username:    "test-user",
		AgentID:     "OxABCD",
		AgentType:   "claude-code",
		CreatedAt:   time.Now().UTC(),
		Files:       map[string]lfs.FileRef{},
	}
	require.NoError(t, lfs.WriteSessionMetaOnly(sessDir, seed))

	// First pass.
	bySession := map[string][]lfs.RedactionEntry{
		sessName: {
			{File: "raw.jsonl", Line: 10, Detector: "github_personal_access_token"},
			{File: "raw.jsonl", Line: 42, Detector: "url_with_password"},
		},
	}
	metaPaths, err := writeRedactionPassesPerSession(context.Background(), ledger,
		bySession, "ox-secrets-N9-cafebabe", "deadbeefcafebabe...")
	require.NoError(t, err)
	require.Len(t, metaPaths, 1)
	assert.Equal(t, "sessions/"+sessName+"/meta.json", metaPaths[0])

	got1, err := lfs.ReadSessionMeta(sessDir)
	require.NoError(t, err)
	require.Len(t, got1.Redactions, 1)
	assert.Equal(t, "ox-secrets-N9-cafebabe", got1.Redactions[0].CatalogVersion)
	assert.Equal(t, 2, got1.Redactions[0].Summary.Total)
	assert.Equal(t, "ox session redact", got1.Redactions[0].AppliedBy)
	firstPassID := got1.Redactions[0].PassID
	assert.NotEmpty(t, firstPassID, "pass id must be populated")

	// Critical: never store matched bytes. Use reflection so a future
	// `Original`/`Match`/`Bytes` field added to lfs.RedactionEntry can't
	// silently start carrying secret material. Pair this with a JSON
	// round-trip check on the serialized payload — if anything secret-y
	// makes it into the bytes that hit disk, we want to know.
	bannedFieldNames := map[string]bool{
		"Original":  true, // legacy session.RedactionResult shape
		"Match":     true,
		"Matched":   true,
		"Bytes":     true,
		"RawBytes":  true,
		"Plaintext": true,
		"Secret":    true,
		"Value":     true,
	}
	entryType := reflect.TypeOf(lfs.RedactionEntry{})
	for i := 0; i < entryType.NumField(); i++ {
		fname := entryType.Field(i).Name
		assert.Falsef(t, bannedFieldNames[fname],
			"RedactionEntry must NEVER add a field named %q (would risk leaking matched bytes — ox-zyg7)", fname)
	}
	// Also verify the field count stays minimal so a stealth `Original`
	// under a different name (Capture, Span, etc.) gets caught by the
	// reviewer. If a legitimate new field needs to land, bump this
	// number AND explain in the assertion message why it's safe.
	assert.LessOrEqual(t, entryType.NumField(), 3,
		"RedactionEntry should stay {File, Line, Detector}; adding fields requires audit")

	// Belt-and-braces: serialize the persisted meta.json and assert no
	// canary bytes leaked into the JSON payload.
	persistedBytes, err := os.ReadFile(filepath.Join(sessDir, "meta.json"))
	require.NoError(t, err)
	for _, canary := range []string{"AKIA", "ghp_", "ghs_", "github_pat_"} {
		assert.NotContainsf(t, string(persistedBytes), canary,
			"persisted meta.json must not contain detector canary %q", canary)
	}

	// Second pass — must APPEND, not overwrite.
	bySession2 := map[string][]lfs.RedactionEntry{
		sessName: {
			{File: "summary.md", Line: 3, Detector: "aws_access_key"},
		},
	}
	_, err = writeRedactionPassesPerSession(context.Background(), ledger,
		bySession2, "ox-secrets-N10-feedface", "feedface...")
	require.NoError(t, err)

	got2, err := lfs.ReadSessionMeta(sessDir)
	require.NoError(t, err)
	require.Len(t, got2.Redactions, 2, "second pass must APPEND, not replace")
	assert.Equal(t, firstPassID, got2.Redactions[0].PassID,
		"earlier pass id must survive untouched")
	assert.NotEqual(t, firstPassID, got2.Redactions[1].PassID,
		"new pass must have a fresh id")
	assert.Equal(t, "ox-secrets-N10-feedface", got2.Redactions[1].CatalogVersion)
}

// TestWriteRedactionPassesPerSession_RefusesMissingMeta verifies the
// safety stance: if meta.json is gone, we don't synthesize a new one
// (that would mask a different bug). The redaction should already have
// been refused upstream — this is defense in depth.
func TestWriteRedactionPassesPerSession_RefusesMissingMeta(t *testing.T) {
	ledger := t.TempDir()
	bySession := map[string][]lfs.RedactionEntry{
		"sess-without-meta": {{File: "raw.jsonl", Line: 1, Detector: "x"}},
	}
	_, err := writeRedactionPassesPerSession(context.Background(), ledger,
		bySession, "v", "h")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no meta.json")
}
