package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionScope_BareInvocationRejected guards the bare-form
// rejection rail. Before #608, a no-flag invocation silently hydrated
// + scanned the entire ledger (multi-minute LFS Batch fetch the
// operator never consented to). Failure prevented: someone removes
// the IsEmpty / Validate check and bare invocation silently scans
// everything.
func TestSessionScope_BareInvocationRejected(t *testing.T) {
	work := makeLedgerWithCommitAndOrigin(t, map[string]string{
		"sessions/leaky/raw.jsonl": "AKIAIOSFODNN7EXAMPLE\n",
	})
	backupDir := t.TempDir()
	out := &bytes.Buffer{}

	// nil scope: workflow must reject before any hydration/snapshot
	err := runRedactHistoryWorkflow(redactHistoryOptions{
		LedgerPath: work,
		DryRun:     true,
		BackupDir:  backupDir,
		Scope:      nil,
		Stdin:      strings.NewReader(""),
		Stdout:     out,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "specify --all")
	entries, _ := os.ReadDir(backupDir)
	assert.Empty(t, entries, "rejection must fire before any snapshot")

	// empty scope (all fields zero-valued): same rail
	err = runRedactHistoryWorkflow(redactHistoryOptions{
		LedgerPath: work,
		DryRun:     true,
		BackupDir:  backupDir,
		Scope:      &sessionScope{},
		Stdin:      strings.NewReader(""),
		Stdout:     out,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "specify --all")
}

// TestSessionScope_SessionFilter_ScopesToMatching verifies the load-
// bearing claim that --session narrows hydration + scan + the
// RedactionPass audit trail to a single session. Failure prevented:
// filter silently broadens past the requested session, redacting
// content the operator did not ask about.
func TestSessionScope_SessionFilter_ScopesToMatching(t *testing.T) {
	work := makeLedgerWithCommitAndOrigin(t, map[string]string{
		"sessions/leaky-a/raw.jsonl": "AKIAIOSFODNN7EXAMPLE\n",
		"sessions/leaky-a/meta.json": minimalMetaJSON("leaky-a", "OxLEAKA"),
		"sessions/leaky-b/raw.jsonl": "AKIAIOSFODNN7EXAMPLE\n",
		"sessions/leaky-b/meta.json": minimalMetaJSON("leaky-b", "OxLEAKB"),
	})
	bPath := filepath.Join(work, "sessions/leaky-b/raw.jsonl")
	originalB, err := os.ReadFile(bPath)
	require.NoError(t, err)
	out := &bytes.Buffer{}

	err = runRedactHistoryWorkflow(redactHistoryOptions{
		LedgerPath: work,
		BackupDir:  t.TempDir(),
		Scope:      &sessionScope{Names: []string{"leaky-a"}},
		Stdin:      strings.NewReader("y\n"),
		Stdout:     out,
	})
	require.NoError(t, err)

	aAfter, err := os.ReadFile(filepath.Join(work, "sessions/leaky-a/raw.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(aAfter), "[REDACTED_AWS_KEY]", "leaky-a must be redacted")

	bAfter, err := os.ReadFile(bPath)
	require.NoError(t, err)
	assert.Equal(t, originalB, bAfter, "leaky-b must be byte-equal to original (out of scope)")

	// the workflow output should not even mention leaky-b
	assert.NotContains(t, out.String(), "leaky-b", "out-of-scope session leaked into output")
}

// TestSessionScope_SessionFilter_UnknownErrorsBeforeHydration is the
// "no LFS fetch for typos" guarantee. A misspelled --session name must
// error BEFORE any hydration or snapshot work begins. Failure
// prevented: typo'd name triggers a minutes-long LFS Batch fetch then
// errors at the end.
func TestSessionScope_SessionFilter_UnknownErrorsBeforeHydration(t *testing.T) {
	work := makeLedgerWithCommitAndOrigin(t, map[string]string{
		"sessions/leaky/raw.jsonl": "AKIAIOSFODNN7EXAMPLE\n",
	})
	backupDir := t.TempDir()
	out := &bytes.Buffer{}

	err := runRedactHistoryWorkflow(redactHistoryOptions{
		LedgerPath: work,
		BackupDir:  backupDir,
		Scope:      &sessionScope{Names: []string{"ghost"}},
		Stdin:      strings.NewReader(""),
		Stdout:     out,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ghost")

	entries, _ := os.ReadDir(backupDir)
	assert.Empty(t, entries, "snapshot must NOT fire for unknown session")
	// cache dir for the unknown name must not exist
	_, statErr := os.Stat(filepath.Join(work, ".sageox", "cache", "sessions", "ghost"))
	assert.True(t, os.IsNotExist(statErr), "no hydration attempt for unknown session")
}

// TestSessionScope_SessionFilter_RepeatedIsUnion checks that
// --session a --session c picks up a and c (skipping b). Failure
// prevented: repeated flag silently overwrites instead of
// accumulating.
func TestSessionScope_SessionFilter_RepeatedIsUnion(t *testing.T) {
	work := makeLedgerWithCommitAndOrigin(t, map[string]string{
		"sessions/sess-a/raw.jsonl": "AKIAIOSFODNN7EXAMPLE\n",
		"sessions/sess-a/meta.json": minimalMetaJSON("sess-a", "OxA"),
		"sessions/sess-b/raw.jsonl": "AKIAIOSFODNN7EXAMPLE\n",
		"sessions/sess-b/meta.json": minimalMetaJSON("sess-b", "OxB"),
		"sessions/sess-c/raw.jsonl": "AKIAIOSFODNN7EXAMPLE\n",
		"sessions/sess-c/meta.json": minimalMetaJSON("sess-c", "OxC"),
	})
	bPath := filepath.Join(work, "sessions/sess-b/raw.jsonl")
	originalB, err := os.ReadFile(bPath)
	require.NoError(t, err)
	out := &bytes.Buffer{}

	err = runRedactHistoryWorkflow(redactHistoryOptions{
		LedgerPath: work,
		BackupDir:  t.TempDir(),
		Scope:      &sessionScope{Names: []string{"sess-a", "sess-c"}},
		Stdin:      strings.NewReader("y\ny\n"),
		Stdout:     out,
	})
	require.NoError(t, err)

	aAfter, _ := os.ReadFile(filepath.Join(work, "sessions/sess-a/raw.jsonl"))
	assert.Contains(t, string(aAfter), "[REDACTED_AWS_KEY]", "sess-a redacted")
	cAfter, _ := os.ReadFile(filepath.Join(work, "sessions/sess-c/raw.jsonl"))
	assert.Contains(t, string(cAfter), "[REDACTED_AWS_KEY]", "sess-c redacted")
	bAfter, _ := os.ReadFile(bPath)
	assert.Equal(t, originalB, bAfter, "sess-b untouched")
}

// TestSessionScope_DateRange_LexicographicWindow exercises --since /
// --until. Session names are ISO-prefixed so string compare is the
// correct semantics. Failure prevented: window math drifts
// (off-by-one, inclusive/exclusive, or implicit date parsing changes
// the bound).
func TestSessionScope_DateRange_LexicographicWindow(t *testing.T) {
	work := makeLedgerWithCommitAndOrigin(t, map[string]string{
		"sessions/2026-03-01T10-00-x/raw.jsonl": "AKIAIOSFODNN7EXAMPLE\n",
		"sessions/2026-03-01T10-00-x/meta.json": minimalMetaJSON("2026-03-01T10-00-x", "OxA"),
		"sessions/2026-04-15T12-00-x/raw.jsonl": "AKIAIOSFODNN7EXAMPLE\n",
		"sessions/2026-04-15T12-00-x/meta.json": minimalMetaJSON("2026-04-15T12-00-x", "OxB"),
		"sessions/2026-05-10T14-00-x/raw.jsonl": "AKIAIOSFODNN7EXAMPLE\n",
		"sessions/2026-05-10T14-00-x/meta.json": minimalMetaJSON("2026-05-10T14-00-x", "OxC"),
	})
	marchPath := filepath.Join(work, "sessions/2026-03-01T10-00-x/raw.jsonl")
	originalMarch, _ := os.ReadFile(marchPath)
	out := &bytes.Buffer{}

	// half-open [2026-04-01, 2026-05-01) → April only
	err := runRedactHistoryWorkflow(redactHistoryOptions{
		LedgerPath: work,
		BackupDir:  t.TempDir(),
		Scope:      &sessionScope{Since: "2026-04-01", Until: "2026-05-01"},
		Stdin:      strings.NewReader("y\n"),
		Stdout:     out,
	})
	require.NoError(t, err)

	aprAfter, _ := os.ReadFile(filepath.Join(work, "sessions/2026-04-15T12-00-x/raw.jsonl"))
	assert.Contains(t, string(aprAfter), "[REDACTED_AWS_KEY]", "April session redacted")
	mayAfter, _ := os.ReadFile(filepath.Join(work, "sessions/2026-05-10T14-00-x/raw.jsonl"))
	assert.NotContains(t, string(mayAfter), "[REDACTED_AWS_KEY]", "May excluded by --until")
	marchAfter, _ := os.ReadFile(marchPath)
	assert.Equal(t, originalMarch, marchAfter, "March excluded by --since")
}

// TestSessionScope_QuarantineIntegration_JSONL is the load-bearing test
// for Path Y: a session whose JSONL was carved out by the pre-push gate
// into .sageox/cache/quarantine/<name>/raw.jsonl must be recoverable
// via `redact --session <name>`. After approve, bytes move back to
// sessions/<name>/raw.jsonl AND the debt marker is removed (signaling
// to `ox doctor` that the debt is paid).
//
// Failure prevented: doctor warning points at a command that doesn't
// help (the original #608 symptom — surface mismatch and marker
// orphan).
func TestSessionScope_QuarantineIntegration_JSONL(t *testing.T) {
	work := makeLedgerWithCommitAndOrigin(t, map[string]string{
		"sessions/dirty/meta.json": minimalMetaJSON("dirty", "OxDIRTY"),
	})

	// Hand-construct quarantine state: bytes at the quarantine path +
	// a marker pointing at them. This is what the forward path
	// (prepush_autoredact.quarantineUnredactableFindings) would leave
	// behind for a JSONL-with-rewrite-error case.
	quarantineDir := filepath.Join(work, ".sageox", "cache", "quarantine", "dirty")
	require.NoError(t, os.MkdirAll(quarantineDir, 0o700))
	quarantineFile := filepath.Join(quarantineDir, "raw.jsonl")
	require.NoError(t, os.WriteFile(quarantineFile, []byte(`{"text":"AKIAIOSFODNN7EXAMPLE"}`+"\n"), 0o600))

	markerDir := filepath.Join(work, ".sageox", "cache", "redaction-debt")
	require.NoError(t, os.MkdirAll(markerDir, 0o700))
	rec := redactionDebtRecord{
		SessionName:   "dirty",
		QuarantinedAt: time.Now().UTC(),
		Reason:        "test fixture",
		Findings: []redactionDebtFinding{
			{Detector: "aws_access_key", Filename: "raw.jsonl", Line: 1},
		},
		QuarantinePaths: []redactionDebtLocation{
			{From: "sessions/dirty/raw.jsonl", To: ".sageox/cache/quarantine/dirty/raw.jsonl"},
		},
	}
	buf, err := json.MarshalIndent(rec, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(markerDir, "dirty.json"), buf, 0o600))

	out := &bytes.Buffer{}
	err = runRedactHistoryWorkflow(redactHistoryOptions{
		LedgerPath: work,
		BackupDir:  t.TempDir(),
		Scope:      &sessionScope{Names: []string{"dirty"}},
		Stdin:      strings.NewReader("y\n"),
		Stdout:     out,
	})
	require.NoError(t, err)

	// quarantine source must be gone
	_, statErr := os.Stat(quarantineFile)
	assert.True(t, os.IsNotExist(statErr), "quarantine source must be moved")

	// destination must hold redacted bytes
	inPlace, err := os.ReadFile(filepath.Join(work, "sessions/dirty/raw.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(inPlace), "[REDACTED_AWS_KEY]", "moved-back file is redacted")
	assert.NotContains(t, string(inPlace), "AKIAIOSFODNN7EXAMPLE", "secret bytes gone")

	// debt marker must be cleaned up
	_, markerErr := os.Stat(filepath.Join(markerDir, "dirty.json"))
	assert.True(t, os.IsNotExist(markerErr), "drained debt marker must be removed")
}

// TestSessionScope_Match_DateRangeUnit unit-tests the matcher in
// isolation. Cheap regression guard for the lex compare logic — no
// disk IO. Failure prevented: subtle off-by-one or inclusive bound
// changes the meaning of --since / --until.
func TestSessionScope_Match_DateRangeUnit(t *testing.T) {
	cases := []struct {
		name  string
		scope sessionScope
		input string
		want  bool
	}{
		{"allowAll", sessionScope{AllowAll: true}, "anything", true},
		{"name hit", sessionScope{Names: []string{"a"}}, "a", true},
		{"name miss", sessionScope{Names: []string{"a"}}, "b", false},
		{"since lower-bound inclusive", sessionScope{Since: "2026-04-01"}, "2026-04-01T00-00-x", true},
		{"since below excluded", sessionScope{Since: "2026-04-01"}, "2026-03-31T23-59-x", false},
		{"until upper-bound exclusive", sessionScope{Until: "2026-05-01"}, "2026-05-01T00-00-x", false},
		{"until below included", sessionScope{Until: "2026-05-01"}, "2026-04-30T23-59-x", true},
		{"name+date is union", sessionScope{Names: []string{"keep-me"}, Since: "2026-05-01"}, "keep-me", true},
		{"empty matches nothing", sessionScope{}, "any", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.scope.Match(tc.input))
		})
	}
}
