package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// isolateDetectEnv pins HOME away from any real ~/.pi and clears PATH so
// exec.LookPath("pi") deterministically fails. Without this, handleDetect's
// later fallbacks (~/.pi present, pi binary in PATH) can mask the env-var
// detection logic these tests are trying to verify.
func isolateDetectEnv(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("PATH", filepath.Join(tmp, "no-such-bin"))
	t.Setenv("AGENT_ENV", "")
	t.Setenv("PI_CODING_AGENT", "")
}

// TestDetect_HandleDetectSignals exercises every signal handleDetect knows
// about, in priority order. The original motivating failure: ox CLI invoked
// from inside pi (which sets process.env.PI_CODING_AGENT="true" at cli.ts:13)
// reports "must be run from within a coding agent" because detection misses
// the only env signal pi actually exports.
//
// Each case below names the specific regression it guards against — losing
// one of these branches must surface as a failed subtest, not a silent drift.
func TestDetect_HandleDetectSignals(t *testing.T) {
	tests := []struct {
		name          string
		agentEnv      string
		piCodingAgent string
		wantDetected  bool
		wantReason    string
		// failure prevented: short note on what real-world breakage this case
		// would let through if removed. Documented per .claude/rules/testing.md.
		failurePrevented string
	}{
		{
			name:             "PI_CODING_AGENT=true",
			piCodingAgent:    "true",
			wantDetected:     true,
			wantReason:       "PI_CODING_AGENT=true",
			failurePrevented: "ox unable to detect pi sessions on default pi-mono installs",
		},
		{
			name:             "PI_CODING_AGENT=1",
			piCodingAgent:    "1",
			wantDetected:     true,
			wantReason:       "PI_CODING_AGENT=1",
			failurePrevented: "callers using the boolean-1 convention silently fail detection",
		},
		{
			name:             "PI_CODING_AGENT=false",
			piCodingAgent:    "false",
			wantDetected:     false,
			failurePrevented: "treating any non-empty value as truthy false-positives PI_CODING_AGENT=false",
		},
		{
			name:             "AGENT_ENV=pi wins over PI_CODING_AGENT",
			agentEnv:         "pi",
			piCodingAgent:    "true",
			wantDetected:     true,
			wantReason:       "AGENT_ENV=pi",
			failurePrevented: "downstream callers keying off Reason break if priority order shifts",
		},
		{
			name:             "no signals",
			wantDetected:     false,
			wantReason:       "~/.pi/ not found and pi not in PATH",
			failurePrevented: "default-true detection forces pi adapter onto Claude Code/Cursor sessions",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateDetectEnv(t)
			if tc.agentEnv != "" {
				t.Setenv("AGENT_ENV", tc.agentEnv)
			}
			if tc.piCodingAgent != "" {
				t.Setenv("PI_CODING_AGENT", tc.piCodingAgent)
			}

			resp, err := handleDetect()
			require.NoError(t, err)
			assert.Equal(t, tc.wantDetected, resp.Detected, tc.failurePrevented)
			if tc.wantReason != "" {
				assert.Equal(t, tc.wantReason, resp.Reason, tc.failurePrevented)
			}
		})
	}
}

// --- Diagnose: AGENTS.md hook repair safety ---

// TestHandleDiagnose_MissingAgentsMD_OffersSafeFixViaOx verifies a missing
// AGENTS.md is reported as repairable via the "ox" dispatch path, not the
// external adapter binary — ox-adapter-pi as argv[0] is rejected by
// adapterFixArgvAllowlist in cmd/ox/doctor_adapters.go, so a FixArgv naming
// it would silently downgrade to display-only under `ox doctor --fix`.
// Failure prevented: FixSafe=true with an argv[0] the auto-fix path refuses,
// making the "safe, automatic" repair never actually run.
func TestHandleDiagnose_MissingAgentsMD_OffersSafeFixViaOx(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // never read the real ~/.pi in a test
	repoRoot := t.TempDir()

	res, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: repoRoot})
	require.NoError(t, err)

	issue := issueBySlugPi(res.Issues, "pi:hooks-missing")
	if issue == nil {
		t.Fatalf("expected pi:hooks-missing, got %+v", res.Issues)
	}
	assert.True(t, issue.FixSafe)
	assert.Equal(t, []string{"ox", "integrate", "install", "--pi"}, issue.FixArgv,
		"argv[0] must be \"ox\" — ox-adapter-pi is not in the auto-fix allowlist")
}

// TestHandleDiagnose_MarkerAbsentButFileReadable_OffersSafeFix covers the
// other repairable case: AGENTS.md exists and is readable but doesn't carry
// the prime marker yet.
func TestHandleDiagnose_MarkerAbsentButFileReadable_OffersSafeFix(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // never read the real ~/.pi in a test
	repoRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "AGENTS.md"), []byte("# no marker here\n"), 0o644))

	res, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: repoRoot})
	require.NoError(t, err)

	issue := issueBySlugPi(res.Issues, "pi:hooks-missing")
	if issue == nil {
		t.Fatalf("expected pi:hooks-missing, got %+v", res.Issues)
	}
	assert.True(t, issue.FixSafe)
	assert.Equal(t, []string{"ox", "integrate", "install", "--pi"}, issue.FixArgv)
}

// TestHandleDiagnose_UnreadableAgentsMD_NotOfferedAsSafeFix is the regression
// gate: a read failure that is NOT "file does not exist" (permission denied,
// a directory in place of the file, ...) must be reported as unreadable, with
// FixSafe=false and no FixArgv — never silently offered the same "just
// install over it" repair as a genuinely missing file, since we cannot even
// verify what's there.
func TestHandleDiagnose_UnreadableAgentsMD_NotOfferedAsSafeFix(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // never read the real ~/.pi in a test
	repoRoot := t.TempDir()
	// a directory in place of AGENTS.md makes os.ReadFile fail with an error
	// that is NOT os.ErrNotExist, portably and without relying on chmod
	// (which a sandboxed/root test runner may ignore).
	require.NoError(t, os.Mkdir(filepath.Join(repoRoot, "AGENTS.md"), 0o755))

	res, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: repoRoot})
	require.NoError(t, err)

	if issue := issueBySlugPi(res.Issues, "pi:hooks-missing"); issue != nil {
		t.Fatalf("an unreadable AGENTS.md must not be reported as the safely-repairable hooks-missing case: %+v", issue)
	}

	issue := issueBySlugPi(res.Issues, "pi:agents-md-unreadable")
	if issue == nil {
		t.Fatalf("expected pi:agents-md-unreadable, got %+v", res.Issues)
	}
	assert.False(t, issue.FixSafe)
	assert.Empty(t, issue.FixArgv)
}

func issueBySlugPi(issues []adapterprotocol.DiagnoseIssue, slug string) *adapterprotocol.DiagnoseIssue {
	for i := range issues {
		if issues[i].Slug == slug {
			return &issues[i]
		}
	}
	return nil
}
