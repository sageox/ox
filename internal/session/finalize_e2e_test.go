package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/runtime"
)

// TestEphemeralFinalizeRouting_EndToEnd proves the load-bearing claim
// behind the capability-based finalize dispatch: in a sandbox-shaped
// environment (CLAUDE_CODE_REMOTE=1, HOME=mktemp), the runtime collapses
// DaemonViable, and the dispatcher routes BOTH user preferences (sync
// and async/delegated) to FinalizeSync — there is no path that
// fire-and-forgets to a non-running daemon.
//
// This is the integration-shaped guarantee the senior-principal review
// asked for at the dispatch layer. Verifying the full upload byte-for-
// byte requires standing up a fake LFS server + fake ledger git remote
// + auth fixtures, which is unrelated to whether the dispatch is
// correct. The dispatch is the part that goes wrong silently; the
// sync upload path (cmd/ox/agent_session.go::uploadSessionToLedger)
// runs inline in the CLI process with no daemon dependency, exercised
// by every non-delegated session-stop today.
func TestEphemeralFinalizeRouting_EndToEnd(t *testing.T) {
	// shape the env: cloud agent + isolated empty HOME (no ~/.sageox)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("CLAUDE_CODE_REMOTE", "task-test-12345")
	// scrub anything that might leak in from the test runner's env
	for _, v := range []string{
		"OX_EPHEMERAL",
		"DEVIN_TASK_ID",
		"CODESPACES",
		"CI",
		"GITHUB_ACTIONS",
		"OX_PERSIST_DISK",
		"OX_NO_DAEMON",
		"OX_BROWSER",
	} {
		t.Setenv(v, "")
	}
	runtime.Reset()
	t.Cleanup(runtime.Reset)

	caps := runtime.Caps()

	// (1) sanity: in this env, the daemon must not be viable. If this
	// flips, the rest of the test is meaningless because the dispatch
	// would correctly go async — to a daemon that exists.
	if caps.DaemonViable {
		t.Fatalf("CLAUDE_CODE_REMOTE=1 + empty HOME must collapse DaemonViable, got caps=%+v", caps)
	}
	if caps.PersistDisk {
		t.Fatalf("CLAUDE_CODE_REMOTE=1 + empty HOME must collapse PersistDisk, got caps=%+v", caps)
	}

	// (2) routing: regardless of the user's summarizer preference, the
	// dispatcher MUST return FinalizeSync. The "user prefers async"
	// case is the one that used to silently signal a non-running
	// daemon and lose the upload.
	for _, prefersAsync := range []bool{false, true} {
		mode := ChooseFinalizeMode(runtime.Caps().DaemonViable, prefersAsync)
		if mode != FinalizeSync {
			t.Errorf("FinalizeModeForCaller(prefersAsync=%v) = %v, want FinalizeSync (daemon not viable)",
				prefersAsync, mode)
		}
	}

	// (3) state-leak: nothing about choosing the finalize mode may
	// write to HOME. We tolerate ANY file the test runner's process
	// already wrote (none should exist in a fresh t.TempDir, but
	// belt-and-suspenders), so we only assert that no .sageox subtree
	// was created.
	entries, err := os.ReadDir(tmpHome)
	if err != nil {
		t.Fatalf("read tmp HOME: %v", err)
	}
	for _, e := range entries {
		if e.Name() == ".sageox" {
			t.Errorf("dispatch unexpectedly created %s — finalize routing must be side-effect-free",
				filepath.Join(tmpHome, ".sageox"))
		}
	}
}

// TestDaemonViable_PathStillRoutesAsync — sanity check the inverse of
// the bug-fix cell: when the runtime IS daemon-viable and the user
// prefers async, the dispatcher still returns FinalizeAsyncDaemon.
// Catches a future refactor that over-corrects by always forcing sync.
func TestDaemonViable_PathStillRoutesAsync(t *testing.T) {
	// laptop-shaped env: clear all venue / override signals
	for _, v := range []string{
		"OX_EPHEMERAL",
		"CLAUDE_CODE_REMOTE",
		"DEVIN_TASK_ID",
		"CODESPACES",
		"CI",
		"GITHUB_ACTIONS",
		"OX_PERSIST_DISK",
		"OX_NO_DAEMON",
		"OX_BROWSER",
	} {
		t.Setenv(v, "")
	}
	runtime.Reset()
	t.Cleanup(runtime.Reset)

	caps := runtime.Caps()
	if !caps.DaemonViable {
		t.Skipf("test host probe reports DaemonViable=false (caps=%+v) — skip; sandbox CI runners hit this", caps)
	}
	if mode := ChooseFinalizeMode(runtime.Caps().DaemonViable, true); mode != FinalizeAsyncDaemon {
		t.Errorf("ChooseFinalizeMode(runtime.Caps().DaemonViable, true) on viable-daemon host = %v, want FinalizeAsyncDaemon", mode)
	}
	if mode := ChooseFinalizeMode(runtime.Caps().DaemonViable, false); mode != FinalizeSync {
		t.Errorf("ChooseFinalizeMode(runtime.Caps().DaemonViable, false) on viable-daemon host = %v, want FinalizeSync", mode)
	}
}
