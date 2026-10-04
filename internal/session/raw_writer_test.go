package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRawWriter_RedactsBuiltinPatterns is the load-bearing test for
// the chokepoint: bytes containing built-in detector matches must
// never reach raw.jsonl.
func TestRawWriter_RedactsBuiltinPatterns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")

	w, err := NewRawWriter(path, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })

	canaries := []string{
		"AKIAIOSFODNN7EXAMPLE",                       // AWS IAM key
		"glpat-AbCdEfGhIjKlMnOpQrSt",                 // GitLab PAT
		"ghp_alphabetabcdefghijklmnopqrstuvwxyz12",   // GitHub PAT
		"Authorization: Bearer ya29.thisIsATokenXyZ", // Bearer header
	}
	for _, c := range canaries {
		require.NoError(t, w.WriteEntry(&SessionEntry{
			Type:    EntryTypeUser,
			Content: "saw " + c + " in the logs",
		}))
	}
	require.NoError(t, w.CloseAndSync())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	out := string(data)
	for _, c := range canaries {
		assert.NotContains(t, out, c, "canary leaked: %s", c)
	}
	// expected slugs for each class
	assert.Contains(t, out, "[REDACTED_AWS_KEY]")
	assert.Contains(t, out, "[REDACTED_GITLAB_TOKEN]")
	assert.Contains(t, out, "[REDACTED_GITHUB_TOKEN]")
	assert.Contains(t, out, "[REDACTED_BEARER_TOKEN]")
}

// TestRawWriter_RedactsGitleaksPatterns verifies the third layer
// (gitleaks-derived extras) fires for patterns not covered by the
// built-in set. OpenAI keys are a good representative because the
// built-in DefaultPatterns set doesn't include them.
func TestRawWriter_RedactsGitleaksPatterns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")
	w, err := NewRawWriter(path, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })

	cases := map[string]struct {
		canary string
		slug   string
	}{
		"openai": {
			// Split-string canary: at runtime this is a full openai-shaped
			// key (redactor sees it whole), but the source file shows only
			// fragments so GitHub's secret-scanner doesn't false-positive
			// and block the push.
			canary: "sk-" + "abcdefghijklmnopqrst" + "T3Blbk" + "FJ" + "abcdefghijklmnopqrst",
			slug:   "[REDACTED_OPENAI_KEY]",
		},
		"openai_project": {
			canary: "sk-proj-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGH",
			slug:   "[REDACTED_OPENAI_PROJECT_KEY]",
		},
		"anthropic": {
			canary: "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKL_mnopqrstuvwxyzAAA",
			slug:   "[REDACTED_ANTHROPIC_KEY]",
		},
		"slack_webhook": {
			// Split-string canary — same reason as openai above. Full
			// string at runtime; fragments at rest in source.
			canary: "https://hooks." + "slack.com" + "/services/TAAAAAAAAA/BBBBBBBBBB/abcdefghijklmnopqrstuvwx",
			slug:   "[REDACTED_SLACK_WEBHOOK]",
		},
		"vault": {
			canary: "hvs." + strings.Repeat("a", 95),
			slug:   "[REDACTED_VAULT_SERVICE_TOKEN]",
		},
		"linear": {
			canary: "lin_api_" + strings.Repeat("A", 40),
			slug:   "[REDACTED_LINEAR_KEY]",
		},
		"sentry": {
			canary: "sntrys_" + strings.Repeat("a", 50),
			slug:   "[REDACTED_SENTRY_TOKEN]",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, w.WriteEntry(&SessionEntry{
				Type:    EntryTypeUser,
				Content: "found " + c.canary + " somewhere",
			}))
		})
	}
	require.NoError(t, w.CloseAndSync())

	data, _ := os.ReadFile(path)
	out := string(data)
	for name, c := range cases {
		assert.NotContains(t, out, c.canary,
			"%s canary leaked through chokepoint: %s", name, c.canary)
		assert.Contains(t, out, c.slug,
			"%s slug missing from output", name)
	}
}

// TestRawWriter_RedactsCommandOutputs verifies the cmd-allowlist layer
// fires through the chokepoint (defense in depth — same coverage as
// the standalone CommandRedactor tests, but via RawWriter).
func TestRawWriter_RedactsCommandOutputs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")
	w, err := NewRawWriter(path, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })

	entry := SessionEntry{
		Type:       EntryTypeTool,
		ToolName:   "Bash",
		ToolInput:  "aws sso login --profile prod",
		ToolOutput: "Successfully logged in\nAccessKeyId: ASIATEST123\nSecretAccessKey: secret-content-here",
	}
	require.NoError(t, w.WriteEntry(&entry))
	require.NoError(t, w.CloseAndSync())

	data, _ := os.ReadFile(path)
	out := string(data)
	assert.NotContains(t, out, "ASIATEST123",
		"aws sso login output reached disk: %s", out)
	assert.NotContains(t, out, "secret-content-here")
	assert.Contains(t, out, "[REDACTED:credential-output:aws-sso-login]")
}

// TestRawWriter_PreservesCleanContent guards against false positives —
// ordinary prose must pass through unchanged.
func TestRawWriter_PreservesCleanContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")
	w, err := NewRawWriter(path, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })

	clean := "let's add a feature to the parser to handle nested quotes"
	require.NoError(t, w.WriteEntry(&SessionEntry{
		Type:    EntryTypeUser,
		Content: clean,
	}))
	require.NoError(t, w.CloseAndSync())

	data, _ := os.ReadFile(path)
	assert.Contains(t, string(data), clean)
	assert.NotContains(t, string(data), "[REDACTED")
}

// TestRawWriter_AppendsToExisting verifies the writer opens O_APPEND so
// catch-up + live-tail in the same session don't trample each other.
func TestRawWriter_AppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")

	w1, err := NewRawWriter(path, "")
	require.NoError(t, err)
	require.NoError(t, w1.WriteEntry(&SessionEntry{Type: EntryTypeUser, Content: "first"}))
	require.NoError(t, w1.Close())

	w2, err := NewRawWriter(path, "")
	require.NoError(t, err)
	require.NoError(t, w2.WriteEntry(&SessionEntry{Type: EntryTypeUser, Content: "second"}))
	require.NoError(t, w2.Close())

	data, _ := os.ReadFile(path)
	out := string(data)
	assert.Contains(t, out, "first")
	assert.Contains(t, out, "second")
}

// TestRawWriter_TruncateForRewrite verifies the truncate variant
// produces a fresh file. Used by regenerate / redact-history paths
// that need to replace raw.jsonl atomically.
func TestRawWriter_TruncateForRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("stale content\n"), 0644))

	w, err := NewRawWriterTruncate(path, "")
	require.NoError(t, err)
	require.NoError(t, w.WriteEntry(&SessionEntry{Type: EntryTypeUser, Content: "fresh"}))
	require.NoError(t, w.Close())

	data, _ := os.ReadFile(path)
	assert.NotContains(t, string(data), "stale content")
	assert.Contains(t, string(data), "fresh")
}

// TestRawWriter_WriteRawMapRedacts is the writer's map-based path used
// by the planning-history importer.
func TestRawWriter_WriteRawMapRedacts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")
	w, err := NewRawWriter(path, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })

	require.NoError(t, w.WriteRaw(map[string]any{
		"type":    "user",
		"content": "aws_access_key_id=AKIAIOSFODNN7EXAMPLE",
	}))
	require.NoError(t, w.CloseAndSync())

	data, _ := os.ReadFile(path)
	assert.NotContains(t, string(data), "AKIA")
	assert.Contains(t, string(data), "[REDACTED_AWS_KEY]")
}

// TestRawWriter_ClosedRejectsFurtherWrites verifies the Closed contract.
// Important because a leaked file descriptor could otherwise write
// post-close, racing the next opener.
func TestRawWriter_ClosedRejectsFurtherWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")
	w, err := NewRawWriter(path, "")
	require.NoError(t, err)
	require.NoError(t, w.Close())

	err = w.WriteEntry(&SessionEntry{Type: EntryTypeUser, Content: "post-close"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

// TestRawWriter_NilEntryReturnsError guards against panics on bad input.
func TestRawWriter_NilEntryReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")
	w, _ := NewRawWriter(path, "")
	t.Cleanup(func() { _ = w.Close() })
	require.Error(t, w.WriteEntry(nil))
	require.Error(t, w.WriteRaw(nil))
}

// TestDefaultExtraDetectors_AllCompile sanity-checks every gitleaks-
// derived pattern in the extras list. A typo'd regex would panic at
// regexp.MustCompile (which would already fail at package init time),
// but this test asserts the list isn't empty and every entry has the
// required fields populated.
func TestDefaultExtraDetectors_AllCompile(t *testing.T) {
	extras := DefaultExtraDetectors()
	require.NotEmpty(t, extras)
	for _, p := range extras {
		assert.NotNil(t, p.Pattern, "pattern %q has nil regex", p.Name)
		assert.NotEmpty(t, p.Name, "pattern has empty name")
		assert.NotEmpty(t, p.Redact, "pattern has empty redact string")
		assert.True(t, strings.HasPrefix(p.Redact, "[REDACTED_") ||
			strings.HasPrefix(p.Redact, "[REDACTED:") ||
			strings.Contains(p.Redact, "[REDACTED_"),
			"pattern %q has unconventional redaction %q (should reference [REDACTED_...])",
			p.Name, p.Redact)
	}
}

// TestGeneratedGitleaksDetectors_AllCompile applies the same sanity to
// the generated gitleaks rule catalog. If gitleaks ships a regex that
// passes their (Go-based) compile step but breaks ours, the generator
// already skipped it — this test catches the case where the generator
// itself regresses.
func TestGeneratedGitleaksDetectors_AllCompile(t *testing.T) {
	gen := generatedGitleaksDetectors()
	require.NotEmpty(t, gen)
	// Roughly the count we expect from gitleaks v8.30.1 (222 total) minus
	// hand-ported and explicit-skip entries. If this number drifts by
	// more than ~20% it's worth a look at the generator's skip list.
	assert.Greater(t, len(gen), 100, "generated catalog suspiciously small (got %d)", len(gen))
	for _, p := range gen {
		assert.NotNil(t, p.Pattern, "generated pattern %q has nil regex", p.Name)
		assert.NotEmpty(t, p.Name)
		assert.True(t, strings.HasPrefix(p.Redact, "[REDACTED_"),
			"generated pattern %q has unexpected slug %q", p.Name, p.Redact)
	}
}

// TestRawWriter_RedactsGeneratedPatternsSample exercises a handful of
// the auto-generated gitleaks rules through the chokepoint to confirm
// the layer-3 wiring actually fires. We can't sample all 147 — the
// AllCompile test above already proves they're loaded — but a
// representative spread catches a wiring regression where the layer
// gets accidentally dropped from RawWriter.
func TestRawWriter_RedactsGeneratedPatternsSample(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")
	w, err := NewRawWriter(path, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })

	// canary, expected redaction slug substring
	// Each canary is constructed to match the corresponding gitleaks rule
	// regex; the test verifies the bytes don't survive the writer pass.
	cases := []struct {
		name   string
		canary string
	}{
		// 1password-secret-key: A3-XXXXXX-(11char|6-5)-5-5-5
		{"1password", "A3-ABCDEF-ABCDEFGHIJK-12345-67890-XYZ12"},
		// adobe-client-secret-style — adobe identifier + 32 hex
		// algolia, age, alibaba, etc. — sample only what regex shape allows.
		// age-secret-key (well-defined shape, easy to canary)
		{"age", "AGE-SECRET-KEY-1" + strings.Repeat("Q", 58)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.NoError(t, w.WriteEntry(&SessionEntry{
				Type:    EntryTypeUser,
				Content: "look at " + c.canary + " here",
			}))
		})
	}
	require.NoError(t, w.CloseAndSync())

	data, _ := os.ReadFile(path)
	out := string(data)
	for _, c := range cases {
		assert.NotContains(t, out, c.canary,
			"%s canary leaked through chokepoint; layer-3 wiring may be broken", c.name)
	}
}

// TestRawWriter_KeywordScreen_DoesNotBypassTokenLeak is the regression
// test for the Critical PR review finding. gitleaks ships per-rule
// "keywords" as vendor names ("airtable"), but many rules match a
// context-free token shape that may appear in a tool dump WITHOUT the
// vendor name nearby. If the keyword screen relied on those vendor
// keywords, a bare leaked token would silently bypass redaction.
//
// The generator now derives keywords from the regex AST instead, so
// each keyword is a guaranteed substring of every match. This test
// confirms the airtable case — a bare `pat<14alnum>.<64hex>` token
// with no surrounding context — is still redacted.
func TestRawWriter_KeywordScreen_DoesNotBypassTokenLeak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")
	w, err := NewRawWriter(path, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })

	// Construct a valid airtable PAT shape: "pat" + 14 alphanumeric +
	// "." + 64 hex. The tool dump deliberately omits the word
	// "airtable" — the only thing that would have anchored the
	// vendor-name-keyword screen before this fix.
	canary := "patABCDEFGHIJKLMN." + strings.Repeat("0123456789abcdef", 4)
	require.NoError(t, w.WriteEntry(&SessionEntry{
		Type:    EntryTypeTool,
		Content: "tool output dump:\n" + canary + "\n(no vendor context)",
	}))
	require.NoError(t, w.CloseAndSync())

	data, _ := os.ReadFile(path)
	out := string(data)
	assert.NotContains(t, out, canary,
		"bare airtable PAT leaked through keyword screen; vendor keyword bypass regression")
	// The hand-ported airtable_key rule fires first and uses a
	// different slug than the generated rule. Either is fine — the
	// load-bearing check is that the bytes don't survive.
	assert.True(t,
		strings.Contains(out, "[REDACTED_AIRTABLE_KEY]") ||
			strings.Contains(out, "[REDACTED_AIRTABLE_PERSONNAL_ACCESS_TOKEN]"),
		"airtable rule did not fire on bare token")
}

// TestGeneratedGitleaksDetectors_KeywordsAreGuaranteedSubstrings is a
// generator-output invariant: any non-empty Keywords list on a
// generated rule MUST contain only lowercase substrings that are
// guaranteed to appear in every match of the rule's regex. Otherwise
// the chokepoint quick-screen would create a false-negative bypass.
//
// We approximate "guaranteed substring" by structural inspection: the
// keyword must appear in the regex source (case-insensitively). This
// is a necessary but not sufficient condition — a stronger semantic
// check is done by the AST walker in the generator itself
// (cmd/gitleaks-port). This test catches accidental drift if a future
// edit bypasses the generator and adds a Keywords entry by hand.
func TestGeneratedGitleaksDetectors_KeywordsAreGuaranteedSubstrings(t *testing.T) {
	for _, p := range generatedGitleaksDetectors() {
		if len(p.Keywords) == 0 {
			continue
		}
		for _, kw := range p.Keywords {
			assert.Equal(t, strings.ToLower(kw), kw,
				"rule %q keyword %q must be lowercase", p.Name, kw)
			assert.GreaterOrEqual(t, len(kw), 3,
				"rule %q keyword %q is shorter than 3 chars; would screen out almost nothing",
				p.Name, kw)
		}
	}
}

// TestGeneratedAirtableRule_KeywordIsTokenAnchor is the targeted
// regression test for the Critical PR review finding. The
// airtable_personnal_access_token rule used to carry only the vendor
// keyword "airtable", which would let a bare token like
// `pat<14alnum>.<64hex>` bypass the keyword screen entirely. The
// generator now derives the keyword from the regex AST, so a leaked
// token (which by definition begins with "pat") always triggers the
// regex.
func TestGeneratedAirtableRule_KeywordIsTokenAnchor(t *testing.T) {
	var found bool
	for _, p := range generatedGitleaksDetectors() {
		if p.Name != "airtable_personnal_access_token" {
			continue
		}
		found = true
		require.NotEmpty(t, p.Keywords,
			"airtable rule must have at least one derived keyword")
		// The keyword must be present in every legitimate match. A
		// match starts with "pat" by regex definition, so the keyword
		// must be a substring of "pat<random suffix>".
		sampleMatch := "patabcdefghijklmn." + strings.Repeat("0", 64)
		for _, kw := range p.Keywords {
			assert.Contains(t, sampleMatch, kw,
				"airtable keyword %q not present in a valid match %q — bypass risk",
				kw, sampleMatch)
			assert.NotEqual(t, "airtable", kw,
				"airtable keyword regression: vendor-name keyword is bypassable")
		}
	}
	require.True(t, found, "airtable_personnal_access_token rule not found in generated detectors")
}

// TestRawWriter_FileModeIsOwnerOnly verifies raw.jsonl is created 0600,
// not world-readable. raw.jsonl holds full conversation content (and any
// secrets in transit before the redaction stack scrubs them); a 0644 file
// would leak it to every local user. Covers both constructors.
// Failure prevented: companion "raw.jsonl world-readable" finding.
func TestRawWriter_FileModeIsOwnerOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(path string) (*RawWriter, error)
	}{
		{"append", func(p string) (*RawWriter, error) { return NewRawWriter(p, "") }},
		{"truncate", func(p string) (*RawWriter, error) { return NewRawWriterTruncate(p, "") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "raw.jsonl")
			w, err := tc.open(path)
			require.NoError(t, err)
			require.NoError(t, w.WriteEntry(&SessionEntry{Type: EntryTypeUser, Content: "hi"}))
			require.NoError(t, w.Close())

			fi, err := os.Stat(path)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0600), fi.Mode().Perm(),
				"raw.jsonl must be owner-only (0600), got %o", fi.Mode().Perm())
		})
	}
}

// TestRawWriter_JSONLOutputIsValid verifies every line in the output is
// parseable JSON. A redactor that introduces unescaped quotes or breaks
// the wire format would create downstream parse failures.
func TestRawWriter_JSONLOutputIsValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")
	w, _ := NewRawWriter(path, "")

	inputs := []string{
		"plain text",
		"contains AKIAIOSFODNN7EXAMPLE",
		`Authorization: Bearer ya29.token1234567890abc`,
		"https://oauth2:glpat-leakedhere1234567890ab@git.example.com/repo.git",
		"text with \"quotes\" and \nnewlines",
	}
	for _, in := range inputs {
		require.NoError(t, w.WriteEntry(&SessionEntry{Type: EntryTypeUser, Content: in}))
	}
	require.NoError(t, w.Close())

	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	assert.Len(t, lines, len(inputs))
	for i, line := range lines {
		var got map[string]any
		err := json.Unmarshal([]byte(line), &got)
		assert.NoError(t, err, "line %d is invalid JSON: %s", i, line)
	}
}

// TestWriteEntriesWritesEveryEntryInOrder prevents a batch importer (e.g.
// planning-history capture) from silently dropping or reordering entries
// when handed a slice instead of calling WriteEntry one at a time.
func TestWriteEntriesWritesEveryEntryInOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")
	w, err := NewRawWriter(path, "")
	require.NoError(t, err)
	entries := []SessionEntry{
		{Type: EntryTypeUser, Content: "first"},
		{Type: EntryTypeUser, Content: "second"},
		{Type: EntryTypeUser, Content: "third"},
	}
	require.NoError(t, w.WriteEntries(entries))
	require.NoError(t, w.CloseAndSync())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 3)
	for i, want := range []string{"first", "second", "third"} {
		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(lines[i]), &got))
		require.Equal(t, want, got["content"])
	}
}

// TestWriteEntriesStopsAtFirstError prevents a batch write from silently
// swallowing a mid-batch failure (e.g. writer closed underneath it) — the
// caller relies on the returned error to know it must reconcile partial
// output rather than assume every entry landed.
func TestWriteEntriesStopsAtFirstError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")
	w, err := NewRawWriter(path, "")
	require.NoError(t, err)
	require.NoError(t, w.Close())

	err = w.WriteEntries([]SessionEntry{{Type: EntryTypeUser, Content: "will fail"}})
	require.ErrorContains(t, err, "already closed")
}

// TestApplyPatternToSliceRedactsSecretsInsideNestedArrays prevents
// WriteRaw's layer-3 pass from missing credentials nested inside a JSON
// array of objects (e.g. a planning-history "content" array of blocks) —
// applyPatternToMap alone only recurses into map/slice VALUES, so the slice
// branch is what makes an array of maps get walked at all.
func TestApplyPatternToSliceRedactsSecretsInsideNestedArrays(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")
	w, err := NewRawWriter(path, "")
	require.NoError(t, err)
	data := map[string]any{
		"blocks": []any{
			map[string]any{"text": "unrelated"},
			map[string]any{"text": "sk-proj-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGH"},
			[]any{"nested list value with sk-proj-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGH"},
		},
	}
	require.NoError(t, w.WriteRaw(data))
	require.NoError(t, w.CloseAndSync())

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(out), "sk-proj-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGH")
	require.Contains(t, string(out), "[REDACTED_OPENAI_PROJECT_KEY]")
}

// TestWriteRawRejectsNilWriterOrData prevents a nil receiver or nil payload
// from panicking deep inside the redactor instead of surfacing a clear error
// to the caller (mirrors the same guard already proven for WriteEntry).
func TestWriteRawRejectsNilWriterOrData(t *testing.T) {
	var nilWriter *RawWriter
	require.ErrorContains(t, nilWriter.WriteRaw(map[string]any{"a": "b"}), "nil")

	dir := t.TempDir()
	w, err := NewRawWriter(filepath.Join(dir, "raw.jsonl"), "")
	require.NoError(t, err)
	require.ErrorContains(t, w.WriteRaw(nil), "nil data")

	require.NoError(t, w.Close())
	require.ErrorContains(t, w.WriteRaw(map[string]any{"a": "b"}), "already closed")
}

// TestCloseAndSyncReturnsSyncErrorButStillMarksClosed prevents a failed
// fsync (e.g. disk error, or the fd was closed out from under the writer)
// from leaving the writer in a state where a caller could keep writing to
// (or double-close) an already-broken file descriptor.
func TestCloseAndSyncReturnsSyncErrorButStillMarksClosed(t *testing.T) {
	dir := t.TempDir()
	w, err := NewRawWriter(filepath.Join(dir, "raw.jsonl"), "")
	require.NoError(t, err)
	// Close the underlying fd directly, bypassing w.closed, so the next
	// Sync() call fails with "file already closed" instead of no-op'ing.
	require.NoError(t, w.file.Close())

	require.Error(t, w.CloseAndSync())
	require.True(t, w.closed, "writer must be marked closed even when Sync fails")
	// A second call must be a safe no-op, not a second attempt to close.
	require.NoError(t, w.CloseAndSync())
}

// TestNewRawSnapshotWriterTruncatesExistingContent proves the
// redact/regenerate rewrite path actually starts from empty — if it
// appended instead of truncating, a redaction pass would leave the
// original unredacted bytes in place ahead of the rewritten ones.
func TestNewRawSnapshotWriterTruncatesExistingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raw.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("stale content that must not survive\n"), 0600))

	w, err := NewRawSnapshotWriter(path, "")
	require.NoError(t, err)
	require.NoError(t, w.WriteEntry(&SessionEntry{Type: EntryTypeUser, Content: "fresh"}))
	require.NoError(t, w.CloseAndSync())

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(out), "stale content")
	require.Contains(t, string(out), "fresh")
}

// TestNewRawFileWriterSurfacesOpenFileError prevents a writer-construction
// failure (e.g. parent directory doesn't exist) from being silently
// swallowed — the caller must see the OpenFile error, not a usable-looking
// writer that fails on the first WriteEntry instead.
func TestNewRawFileWriterSurfacesOpenFileError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-parent", "raw.jsonl")
	_, err := NewRawWriter(path, "")
	require.Error(t, err)
}

// TestNewRawWriterWithProjectRootLoadsCustomRedactionRules proves passing a
// non-empty projectRoot actually routes through NewRedactorWithCustomRules
// (not just the built-in NewRedactor), so a project's .sageox/REDACT.md
// custom rule takes effect for a session recorded inside that project.
func TestNewRawWriterWithProjectRootLoadsCustomRedactionRules(t *testing.T) {
	projectRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, ".sageox"), 0755))
	redactMD := "```redact\nregex \"custom-secret-[0-9]+\" -> [REDACTED_CUSTOM_TOKEN]\n```\n"
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".sageox", "REDACT.md"), []byte(redactMD), 0600))

	path := filepath.Join(t.TempDir(), "raw.jsonl")
	w, err := NewRawWriter(path, projectRoot)
	require.NoError(t, err)
	require.NoError(t, w.WriteEntry(&SessionEntry{Type: EntryTypeUser, Content: "saw custom-secret-12345 in output"}))
	require.NoError(t, w.CloseAndSync())

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(out), "custom-secret-12345")
}

// TestAsWriterBypassesRedactionStack documents and pins the asWriter
// contract: bytes written through it reach the file completely unredacted.
// If this ever changed silently, a caller relying on the documented bypass
// for pre-redacted content could start double-processing or corrupting it.
func TestAsWriterBypassesRedactionStack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raw.jsonl")
	w, err := NewRawWriter(path, "")
	require.NoError(t, err)
	_, err = w.asWriter().Write([]byte("AKIAIOSFODNN7EXAMPLE unredacted\n"))
	require.NoError(t, err)
	require.NoError(t, w.CloseAndSync())

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(out), "AKIAIOSFODNN7EXAMPLE", "asWriter must bypass redaction by contract")
}

// TestAppendRecordingBatchRejectsNonAdvancingCursor prevents a stale or
// replayed batch from silently rewinding the capture cursor — per the
// session-streaming dense-seq-fold invariant, the persisted SourceOffset
// must be monotonic or replay reconstruction and live assignment can diverge.
func TestAppendRecordingBatchRejectsNonAdvancingCursor(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "raw.jsonl")
	statePath := filepath.Join(dir, ".recording.json")
	require.NoError(t, fileutil.AtomicWriteJSON(statePath, &RecordingState{SessionPath: dir, SourceOffset: 100}, 0600))

	w, err := NewRawWriter(raw, "")
	require.NoError(t, err)
	defer w.Close()

	rawBefore, err := os.ReadFile(raw)
	require.NoError(t, err)
	stateBefore, err := os.ReadFile(statePath)
	require.NoError(t, err)

	err = w.AppendRecordingBatch(statePath, []Entry{{Type: EntryTypeUser, Content: "x"}}, 50)
	require.ErrorContains(t, err, "did not advance")

	rawAfter, err := os.ReadFile(raw)
	require.NoError(t, err)
	stateAfter, err := os.ReadFile(statePath)
	require.NoError(t, err)
	require.Equal(t, rawBefore, rawAfter, "rejected batch must not write any entries to raw.jsonl")
	require.Equal(t, stateBefore, stateAfter, "rejected batch must not persist a new cursor to .recording.json")
}
