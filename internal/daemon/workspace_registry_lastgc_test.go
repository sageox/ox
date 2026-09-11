package daemon

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
)

// TestUpdateLastGC_UninitializedProjectLogsNoUnactionableAdvice pins that GC
// bookkeeping never logs an instruction the daemon cannot follow.
//
// SaveLocalConfig refuses to write when there is no .sageox/, returning
// "project not initialized: run 'ox init' first". The daemon called it once per
// workspace per GC cycle and warned on the error, so daemon logs filled up with
// a message telling the daemon to run `ox init` — advice no process reading
// that log can act on, and invisible to users besides, since neither
// `ox status` nor `ox doctor` surfaces daemon logs.
//
// There is genuinely nothing to persist in that state, so the right behavior is
// to skip quietly while still keeping the in-memory timestamp.
func TestUpdateLastGC_UninitializedProjectLogsNoUnactionableAdvice(t *testing.T) {
	root := t.TempDir() // deliberately no .sageox/
	if config.IsInitialized(root) {
		t.Fatal("fixture must be an uninitialized project")
	}

	// capture what the daemon actually writes to its log
	var logbuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	r := NewWorkspaceRegistry(root, "repo-under-test")
	r.localConfigCache = &config.LocalConfig{}
	r.workspaces["ws1"] = &WorkspaceState{Type: WorkspaceTypeLedger}

	before := time.Now()
	r.UpdateLastGC("ws1")

	logged := logbuf.String()
	if strings.Contains(logged, "ox init") {
		t.Errorf("daemon must not tell itself to run `ox init` — unactionable for the process reading this log\n--- logged ---\n%s", logged)
	}
	if strings.Contains(logged, "failed to persist last_gc") {
		t.Errorf("an uninitialized project has nothing to persist; this is not a failure\n--- logged ---\n%s", logged)
	}

	// the in-memory half must still happen — that is what stops GC re-running
	// every cycle within a single daemon lifetime
	if got := r.GetLastGCTime("ws1"); got.Before(before) {
		t.Errorf("in-memory LastGCTime not updated: got %v, want >= %v", got, before)
	}
}

// TestUpdateLastGC_RealFailureStillWarns is the other half: silencing the
// uninitialized case must not silence genuine persistence failures, or the fix
// would trade log noise for a blind spot.
func TestUpdateLastGC_RealFailureStillWarns(t *testing.T) {
	root := t.TempDir()
	// initialized, so the skip does NOT apply...
	mustInitSageoxDir(t, root)
	if !config.IsInitialized(root) {
		t.Skip("could not construct an initialized fixture in this environment")
	}

	var logbuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	r := NewWorkspaceRegistry(root, "repo-under-test")
	r.localConfigCache = &config.LocalConfig{}
	r.workspaces["ws1"] = &WorkspaceState{Type: WorkspaceTypeLedger}

	// ...but make the write itself impossible
	makeSageoxDirUnwritable(t, root)

	r.UpdateLastGC("ws1")

	if !strings.Contains(logbuf.String(), "failed to persist last_gc") {
		t.Errorf("a real persistence failure must still be reported\n--- logged ---\n%s", logbuf.String())
	}
}

func mustInitSageoxDir(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".sageox"), 0o755); err != nil {
		t.Fatalf("create .sageox: %v", err)
	}
}

// makeSageoxDirUnwritable forces SaveLocalConfig's write to fail for a reason
// that is NOT "uninitialized", so the warn path is exercised.
func makeSageoxDirUnwritable(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, ".sageox")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skipf("cannot make dir read-only in this environment: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}
