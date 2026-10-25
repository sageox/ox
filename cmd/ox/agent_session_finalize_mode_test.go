package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/runtime"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/require"
)

func clearFinalizeEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"SAGEOX_DAEMON",
		"OX_NO_DAEMON",
		"OX_EPHEMERAL",
		"CLAUDE_CODE_REMOTE",
		"DEVIN_TASK_ID",
		"AMP_ORB",
		"CI",
		"GITHUB_ACTIONS",
		"OX_PERSIST_DISK",
	} {
		t.Setenv(key, "")
	}
	// cmd/ox's TestMain hands tests an empty HOME. The persistent-disk probe
	// needs the config dir's parent to exist, as it does on any laptop;
	// without it the daemon reads as unavailable and every case here
	// collapses to sync, whichever order the tests run in.
	for _, dir := range []string{paths.ConfigDir(), paths.SageoxDir()} {
		if dir != "" {
			require.NoError(t, os.MkdirAll(filepath.Dir(dir), 0o755))
		}
	}
	runtime.Reset()
	t.Cleanup(runtime.Reset)
}

// TestFinalizeModeForSessionStop_RespectsDaemonDisableSwitch ensures the
// session-stop dispatcher obeys the explicit daemon off-switch. Failure
// prevented: delegated summarization chooses the async path even when
// SAGEOX_DAEMON=false guarantees no daemon will service the signal.
func TestFinalizeModeForSessionStop_RespectsDaemonDisableSwitch(t *testing.T) {
	clearFinalizeEnv(t)
	t.Setenv("SAGEOX_DAEMON", "false")

	got := finalizeModeForSessionStop(true)
	if got != session.FinalizeSync {
		t.Fatalf("SAGEOX_DAEMON=false must force sync finalize, got %v", got)
	}
}

// TestFinalizeModeForSessionStop_DefaultAsync ensures the explicit daemon
// off-switch fix doesn't regress the normal delegated path on a laptop.
func TestFinalizeModeForSessionStop_DefaultAsync(t *testing.T) {
	clearFinalizeEnv(t)

	got := finalizeModeForSessionStop(true)
	if got != session.FinalizeAsyncDaemon {
		t.Fatalf("default delegated session stop should remain async, got %v", got)
	}
}

// TestFinalizeModeForSessionStop_AmpOrbUploadsBeforeReturning: Avery runs Amp
// in an orb with agent.summarizer=delegated. The orb's daemon is up, but it is
// frozen when the orb pauses minutes after the agent goes idle, so a stop that
// signals the daemon and returns leaves the session in the orb. Failure
// prevented (sageox/ox#1314): delegated stop in an orb never reaches the Ledger.
func TestFinalizeModeForSessionStop_AmpOrbUploadsBeforeReturning(t *testing.T) {
	clearFinalizeEnv(t)
	require.True(t, runtime.Probe().DaemonViable, "precondition: a laptop-like host, or sync proves nothing")
	t.Setenv("AMP_ORB", "1")

	got := finalizeModeForSessionStop(true)
	if got != session.FinalizeSync {
		t.Fatalf("delegated stop in an Amp orb must upload synchronously, got %v", got)
	}
}
