package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
