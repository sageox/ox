package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// TestDaemon_MultiSubsystem_NoFDLeak is the GUARD for FD growth across new
// daemon subsystems. It composes the three subsystems that move most often
// (whisper registry, KB sync, project watcher), exercises a realistic
// lifecycle on each, then asserts the process-wide FD count returns to
// baseline.
//
// Why this shape: per-subsystem FD-baseline tests (already in place for
// project_watcher and whisper) catch regressions inside each individual
// subsystem. This test catches the OTHER failure mode — someone adds a new
// long-lived FD-holding component and forgets to add a per-subsystem
// regression test. As long as the new component is wired into one of the
// composed flows here (almost everything is), this test trips immediately.
//
// Failure prevented: a future commit that leaks a per-team / per-KB / per-
// session FD goes unnoticed because reviewers and authors forgot to add a
// dedicated test for the new surface. A single failing assertion here
// surfaces it before merge.
//
// The companion sibling tests (TestWhisperRegistry_NoFDLeak_OnIdleClose,
// TestKBSync_NoFDLeak_AtScale) remain valuable because they localize the
// regression to a specific subsystem when they fail. (The former
// TestProjectWatcher_NoFDLeak_RealWatcher_Darwin was retired with the recursive
// fsnotify watcher — GitPollWatcher holds no FDs, so its subsystem contribution
// here is structurally zero.)
func TestDaemon_MultiSubsystem_NoFDLeak(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("FD enumeration unavailable on Windows")
	}
	if testing.Short() {
		t.Skip("short: composes multi-subsystem lifecycle")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// --- 1. Whisper registry: ledger + N team stores with idle-close ---
	ledger := openTestStore(t)
	whisperReg := NewWhisperRegistry(ledger, logger)
	t.Cleanup(func() { _ = whisperReg.Close() })

	// --- 2. KB sync scheduler with N bare-repo fixtures ---
	s, projectDir := kbTestScheduler(t)
	_ = projectDir

	const numTeams = 6
	const numKBs = 10

	kbBubbles := make([]api.KB, numKBs)
	for i := 0; i < numKBs; i++ {
		bareDir := makeBareRepo(t, fmt.Sprintf("fdkb-%02d", i), "notes.md", "x\n")
		kbBubbles[i] = api.KB{
			KBID:    fmt.Sprintf("fdkb_%02d", i),
			KBType:  api.KBTypePersonal,
			Slug:    fmt.Sprintf("fdkb-%02d", i),
			RepoURL: "file://" + bareDir,
		}
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: kbBubbles}
	})

	// --- 3. GitPollWatcher against a real git repo + churn cycles ---
	watchDir := t.TempDir()
	initGitRepo(t, watchDir)
	srcDir := filepath.Join(watchDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir srcDir: %v", err)
	}
	pwAcc := NewChangeAccumulator(20 * time.Millisecond)
	pw := NewGitPollWatcher(watchDir, pwAcc, logger)
	pw.interval = 20 * time.Millisecond // fast poll for the test
	watcherCtx, cancelWatcher := context.WithCancel(context.Background())
	watcherDone := make(chan struct{})
	go func() {
		pw.Start(watcherCtx)
		close(watcherDone)
	}()
	// Let the poller establish its baseline snapshot.
	time.Sleep(100 * time.Millisecond)

	// --- Baseline: AFTER fixture setup, BEFORE workload ---
	// Warm up by exercising each subsystem once so any one-time allocations
	// (sql.DB driver init, fsnotify goroutine spinup) don't pollute the
	// baseline.
	for i := 0; i < numTeams; i++ {
		whisperReg.AddTeamStore(fmt.Sprintf("warm-team-%d", i), openTestStore(t))
	}
	// Make them idle and close immediately to force any one-time SQLite
	// driver bookkeeping FDs to materialize and release once.
	clock := time.Now()
	whisperReg.SetClock(func() time.Time { return clock.Add(time.Hour) })
	whisperReg.CloseIdleTeamStores(1 * time.Minute)
	whisperReg.SetClock(time.Now)

	baseline := countOpenFDs(t)

	// --- Workload: hit every subsystem repeatedly ---

	// Whisper: N team stores, exercise reads/writes, then idle-close.
	for i := 0; i < numTeams; i++ {
		whisperReg.AddTeamStore(fmt.Sprintf("workload-team-%d", i), openTestStore(t))
	}
	if err := whisperReg.Add("team", whisperstore.WhisperEntry{
		ID: "fd-w1", Scope: "team", Type: whisperstore.WhisperTrigger,
		Source: "fd-test", Topic: "lint", Content: "hello",
		Importance: whisperstore.ImportanceNormal, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("whisper Add team: %v", err)
	}
	if _, err := whisperReg.GetWhispers("agent-fd", whisperstore.AttentionAll, nil); err != nil {
		t.Fatalf("whisper GetWhispers: %v", err)
	}

	// KB sync: clone N bubbles (cold pass), then steady pass.
	s.syncBubbles(context.Background())
	s.syncBubbles(context.Background())

	// GitPollWatcher: churn a few subdirs so the poller runs git status across
	// many cycles, exercising the subprocess spawn/reap path that must not leak.
	for i := 0; i < 5; i++ {
		sub := filepath.Join(srcDir, fmt.Sprintf("churn-%02d", i))
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatalf("mkdir churn: %v", err)
		}
		f := filepath.Join(sub, "x.go")
		if err := os.WriteFile(f, []byte("package x\n"), 0o644); err != nil {
			t.Fatalf("write churn: %v", err)
		}
		if err := os.RemoveAll(sub); err != nil {
			t.Fatalf("rm churn: %v", err)
		}
	}
	// Let the poller observe the churn across a few poll cycles.
	time.Sleep(200 * time.Millisecond)

	// --- Cleanup phase: trigger every release path ---

	// Whisper idle-close: forces all team stores to release their SQLite FDs.
	clock = time.Now()
	whisperReg.SetClock(func() time.Time { return clock.Add(time.Hour) })
	closed := whisperReg.CloseIdleTeamStores(1 * time.Minute)
	if closed != numTeams {
		t.Errorf("expected %d teams idle-closed, got %d", numTeams, closed)
	}

	// GitPollWatcher: cancel + wait.
	cancelWatcher()
	select {
	case <-watcherDone:
	case <-time.After(2 * time.Second):
		t.Fatal("git poll watcher did not exit after cancel")
	}

	// Settle: give the runtime a beat to release any deferred FDs (sql/sqlite
	// connection-pool close, fsnotify goroutine exit).
	settled := waitForFDsToSettle(t, baseline, 1*time.Second)

	final := countOpenFDs(t)
	delta := final - baseline

	// Tolerance: we accept a small drift for sql/sqlite WAL bookkeeping
	// and Go runtime noise. The pre-fix scenario for the bugs this guards
	// against (per-team / per-KB FD leak) would produce delta well above
	// this bound — e.g. PR #619's bug was 27 FDs leaked across 9 stores.
	const tolerance = 15
	if delta >= tolerance {
		t.Errorf("daemon-wide FD baseline regressed: delta=%d (baseline=%d, final=%d, settled=%v). "+
			"Per-subsystem leaks would compound far above tolerance=%d. "+
			"Composed: %d team whisper stores, %d KBs, project watcher with 5 churn cycles.",
			delta, baseline, final, settled, tolerance, numTeams, numKBs)
	}

	t.Logf("daemon-wide FD baseline: baseline=%d, final=%d, delta=%d (settled=%v) "+
		"across %d whisper teams + %d KBs + project watcher",
		baseline, final, delta, settled, numTeams, numKBs)
}

// waitForFDsToSettle polls until either the FD count drops back to within
// tolerance of baseline or the deadline is hit. Returns true if settled.
//
// We need this because some release paths (sql.DB pool drain, goroutine
// exit-and-fsnotify-Close) happen asynchronously after the explicit close
// call returns. Without a small settle window, the test would flake on
// slow CI runners.
func waitForFDsToSettle(t *testing.T, baseline int, timeout time.Duration) bool {
	t.Helper()
	const tolerance = 5
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if countOpenFDs(t)-baseline < tolerance {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// countOpenFDs returns the number of open file descriptors for the current
// process via /dev/fd. Shared by daemon FD-baseline and regression tests.
func countOpenFDs(t *testing.T) int {
	t.Helper()
	d, err := os.Open("/dev/fd")
	if err != nil {
		t.Fatalf("open /dev/fd: %v", err)
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		t.Fatalf("enumerate /dev/fd: %v", err)
	}
	// Subtract 1 for the FD held by 'd' itself, closed before the next sample.
	n := len(names) - 1
	if n < 0 {
		n = 0
	}
	return n
}
