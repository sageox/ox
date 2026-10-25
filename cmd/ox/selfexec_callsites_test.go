package main

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/selfexec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every ox code path that re-runs ox as a subprocess goes through
// selfexec.Path(). Inside a test binary that call refuses, which is the whole
// point: os.Executable() would hand back .../ox.test, and
// `ox.test daemon start` re-runs this entire suite forever (Go's flag package
// swallows the subcommand as a positional arg, and an exec'd test binary has
// no default -test.timeout).
//
// These tests are the end-to-end proof of that refusal. They drive the REAL
// call sites — not selfexec.Path() in isolation — and assert two things per
// site: the outcome the site surfaces to its caller, and that no self-exec
// child was left behind.
//
// Red-first proof: delete the testing.Testing() branch in selfexec.Path and
// every case below fails, most of them by hanging until the suite times out —
// which is exactly the production symptom.

// wantsErr distinguishes the two legitimate reactions to ErrUnderTest.
type selfexecOutcome int

const (
	// failOpen: the site swallows the refusal and continues. Correct where the
	// subprocess is an enhancement (hook enrichment, recall) rather than the
	// user's actual request.
	failOpen selfexecOutcome = iota
	// propagates: the site returns the refusal. Correct where spawning the
	// subprocess IS the request (`ox daemon start`), so silence would be a lie.
	propagates
)

func TestSelfExecCallSites_RefuseToReexecTestBinary(t *testing.T) {
	tests := []struct {
		name string
		// call drives the production call site and returns the error it
		// surfaced (nil when the site fail-opens).
		call func(t *testing.T) error
		want selfexecOutcome
	}{
		{
			name: "runPrimeForHook skips the prime subprocess",
			call: func(t *testing.T) error {
				return runPrimeForHook("", &HookContext{Phase: phaseStart, ProjectRoot: t.TempDir()})
			},
			want: failOpen,
		},
		{
			name: "runPlanSubprocess skips plan enrichment",
			call: func(t *testing.T) error {
				out, ok := runPlanSubprocess("# plan", planEnrichArgs()...)
				assert.False(t, ok, "plan-exit steps are fail-open; ok must be false")
				assert.Nil(t, out, "no subprocess ran, so there is no output")
				return nil
			},
			want: failOpen,
		},
		{
			name: "shellLocalQueryRunner reports it cannot locate ox",
			call: func(t *testing.T) error {
				results, err := (&shellLocalQueryRunner{}).Query(context.Background(), "why is sync slow", 5)
				assert.Nil(t, results)
				return err
			},
			want: propagates,
		},
		{
			name: "startDaemonBackground refuses to spawn",
			call: func(t *testing.T) error {
				return startDaemonBackground(t.TempDir())
			},
			want: propagates,
		},
		{
			name: "autoStartDaemon refuses to spawn",
			call: func(t *testing.T) error {
				// not the OX_NO_DAEMON early return — that branch is covered by
				// TestAutoStartDaemon_DisabledByEnv. This must reach selfexec.
				t.Setenv("OX_NO_DAEMON", "")
				return autoStartDaemon()
			},
			want: propagates,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(t)

			switch tt.want {
			case failOpen:
				require.NoError(t, err, "fail-open call site must not surface the refusal")
			case propagates:
				require.Error(t, err)
				assert.ErrorIs(t, err, selfexec.ErrUnderTest,
					"the refusal must stay identifiable through the wrap")
			}
			assertNoSelfExecChild(t)
		})
	}
}

// assertNoSelfExecChild fails if any live process was started from this test
// binary's own path. That is the exact signature of the fork bomb — a
// `.../ox.test daemon start` generation spawning the next — and it cannot be
// confused with the ordinary git and ps subprocesses the suite runs.
func assertNoSelfExecChild(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("ps"); err != nil {
		t.Log("ps(1) unavailable; skipping self-exec child scan")
		return
	}
	self, err := os.Executable()
	require.NoError(t, err)

	out, err := exec.Command("ps", "-A", "-o", "pid=,args=").Output()
	if err != nil {
		t.Logf("ps(1) failed (%v); skipping self-exec child scan", err)
		return
	}

	me := strconv.Itoa(os.Getpid())
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] == me {
			continue
		}
		assert.NotEqual(t, self, fields[1], "self-exec child leaked: %s", strings.TrimSpace(line))
	}
}
