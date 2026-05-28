package main

import (
	"testing"

	"github.com/sageox/ox/internal/runtime"
	"github.com/sageox/ox/internal/session"
)

func clearFinalizeEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"SAGEOX_DAEMON",
		"OX_NO_DAEMON",
		"OX_EPHEMERAL",
		"CLAUDE_CODE_REMOTE",
		"DEVIN_TASK_ID",
		"CI",
		"GITHUB_ACTIONS",
		"OX_PERSIST_DISK",
	} {
		t.Setenv(key, "")
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
