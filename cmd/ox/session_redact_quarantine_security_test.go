package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnumerateQuarantineFindings_RejectsPathTraversal is the security
// regression for the marker-trust threat model: an attacker with write
// access to .sageox/cache/redaction-debt/<x>.json can craft a marker
// whose quarantine_paths[].to escapes the ledger. Without containment
// guards, the redact loop's subsequent os.Rename(quarantineAbs,
// inPlaceAbs) would overwrite arbitrary files reachable by the
// operator's UID.
//
// The fix lives in enumerateQuarantineFindings (rejects bad markers at
// read time) plus belt-and-suspenders safeRelativeUnder checks at the
// os.Rename / os.Open call sites. This test exercises both: a marker
// with a traversal payload must not produce any findings, AND the
// error stream must explain why so the operator notices something is
// wrong.
//
// Failure prevented: silent acceptance of a marker that, on the next
// redact run, lets a ledger-writer escalate to "arbitrary-file-writer
// at operator UID" via crafted to/from paths.
func TestEnumerateQuarantineFindings_RejectsPathTraversal(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Path-separator semantics differ; the guards still apply but
		// the test fixtures below are POSIX-style.
		t.Skip("POSIX path fixtures")
	}
	ledger := t.TempDir()
	debtDir := filepath.Join(ledger, ".sageox", "cache", "redaction-debt")
	require.NoError(t, os.MkdirAll(debtDir, 0o700))

	cases := []struct {
		name       string
		marker     redactionDebtRecord
		filename   string
		wantErrSub string
	}{
		{
			name: "to escapes ledger via dotdot",
			marker: redactionDebtRecord{
				SessionName: "evil",
				Findings:    []redactionDebtFinding{{Detector: "x", Filename: "raw.jsonl", Line: 1}},
				QuarantinePaths: []redactionDebtLocation{{
					From: "sessions/evil/raw.jsonl",
					To:   "../../../tmp/owned.jsonl",
				}},
			},
			filename:   "evil.json",
			wantErrSub: "direct child of .sageox/cache/quarantine/evil/",
		},
		{
			name: "from escapes session subtree",
			marker: redactionDebtRecord{
				SessionName: "evil",
				Findings:    []redactionDebtFinding{{Detector: "x", Filename: "raw.jsonl", Line: 1}},
				QuarantinePaths: []redactionDebtLocation{{
					From: "../../etc/passwd",
					To:   ".sageox/cache/quarantine/evil/raw.jsonl",
				}},
			},
			filename:   "evil.json",
			wantErrSub: "direct child of sessions/evil/",
		},
		{
			name: "session name has separator",
			marker: redactionDebtRecord{
				SessionName: "evil/..",
				Findings:    []redactionDebtFinding{{Detector: "x", Filename: "raw.jsonl", Line: 1}},
			},
			filename:   "evil-with-bad-name.json",
			wantErrSub: "invalid session name",
		},
		{
			name: "filename mismatch (marker filename != session_name)",
			marker: redactionDebtRecord{
				SessionName: "legitimate",
				Findings:    []redactionDebtFinding{{Detector: "x", Filename: "raw.jsonl", Line: 1}},
				QuarantinePaths: []redactionDebtLocation{{
					From: "sessions/legitimate/raw.jsonl",
					To:   ".sageox/cache/quarantine/legitimate/raw.jsonl",
				}},
			},
			filename:   "different-name.json",
			wantErrSub: "filename does not match session_name",
		},
		{
			name: "finding filename contains separator",
			marker: redactionDebtRecord{
				SessionName: "ok",
				Findings:    []redactionDebtFinding{{Detector: "x", Filename: "../escape", Line: 1}},
				QuarantinePaths: []redactionDebtLocation{{
					From: "sessions/ok/raw.jsonl",
					To:   ".sageox/cache/quarantine/ok/raw.jsonl",
				}},
			},
			filename:   "ok.json",
			wantErrSub: "path separator",
		},
		{
			name: "to has subdirectory (not direct child)",
			marker: redactionDebtRecord{
				SessionName: "ok",
				Findings:    []redactionDebtFinding{{Detector: "x", Filename: "raw.jsonl", Line: 1}},
				QuarantinePaths: []redactionDebtLocation{{
					From: "sessions/ok/raw.jsonl",
					To:   ".sageox/cache/quarantine/ok/nested/raw.jsonl",
				}},
			},
			filename:   "ok.json",
			wantErrSub: "direct child of .sageox/cache/quarantine/ok/",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Each case in its own scratch dir so the entry iteration
			// order is deterministic.
			thisLedger := t.TempDir()
			thisDebtDir := filepath.Join(thisLedger, ".sageox", "cache", "redaction-debt")
			require.NoError(t, os.MkdirAll(thisDebtDir, 0o700))
			buf, err := json.MarshalIndent(tc.marker, "", "  ")
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(thisDebtDir, tc.filename), buf, 0o600))

			findings, enumErr := enumerateQuarantineFindings(thisLedger, nil)
			assert.Empty(t, findings, "no findings should be produced from a traversal-tainted marker")
			require.Error(t, enumErr, "enumeration must surface the rejection")
			assert.Contains(t, enumErr.Error(), tc.wantErrSub,
				"error message must point at the specific rule that fired")
		})
	}
}

// TestMoveQuarantinedFileBack_RejectsTraversal verifies the
// belt-and-suspenders containment check at the os.Rename boundary.
// Even if a future refactor sneaks traversal-tainted paths past the
// marker-enumeration guard, the move itself must refuse.
func TestMoveQuarantinedFileBack_RejectsTraversal(t *testing.T) {
	ledger := t.TempDir()
	// Place a real file at <ledger>/.sageox/cache/quarantine/foo/raw.jsonl
	// so the function can't bail on "source doesn't exist" before reaching
	// the containment check.
	src := filepath.Join(ledger, ".sageox", "cache", "quarantine", "foo", "raw.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(src), 0o700))
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o600))

	// Target escapes the ledger.
	err := moveQuarantinedFileBack(ledger,
		".sageox/cache/quarantine/foo/raw.jsonl",
		"../escape/raw.jsonl")
	require.Error(t, err)
	assert.True(t,
		strings.Contains(err.Error(), "escapes") || strings.Contains(err.Error(), "absolute"),
		"error must explain containment, got: %v", err)

	// Source escapes the ledger.
	err = moveQuarantinedFileBack(ledger,
		"../etc/passwd",
		"sessions/foo/raw.jsonl")
	require.Error(t, err)
}
