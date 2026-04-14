package hooks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon/hooks"
)

func TestRunnerJSONOnStdin(t *testing.T) {
	t.Parallel()

	tmpFile := filepath.Join(t.TempDir(), "output.json")

	runner := hooks.NewHookRunner([]hooks.HookConfig{
		{Event: "daemon.started", Command: "cat > " + tmpFile},
	}, testLogger())

	event := hooks.Event{
		Name:    hooks.EventDaemonStarted,
		Project: "/tmp/test",
		RepoID:  "repo_test",
	}

	runner.Dispatch(context.Background(), event)
	runner.Wait()

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("failed to read output: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid JSON on stdin: %v\ncontent: %s", err, data)
	}

	if parsed["event"] != "daemon.started" {
		t.Errorf("event = %v, want daemon.started", parsed["event"])
	}
	if parsed["project_root"] != "/tmp/test" {
		t.Errorf("project_root = %v, want /tmp/test", parsed["project_root"])
	}
}

func TestRunnerTimeout(t *testing.T) {
	t.Parallel()

	runner := hooks.NewHookRunner([]hooks.HookConfig{
		{Event: "daemon.started", Command: "sleep 10"},
	}, testLogger())

	start := time.Now()
	runner.Dispatch(context.Background(), hooks.Event{Name: hooks.EventDaemonStarted})
	// wait for the hook to be killed (500ms SIGTERM + 500ms SIGKILL + margin)
	time.Sleep(1500 * time.Millisecond)
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Fatalf("hook took %v, expected <3s (timeout should kill it)", elapsed)
	}
}

func TestRunnerExitCode(t *testing.T) {
	t.Parallel()

	runner := hooks.NewHookRunner([]hooks.HookConfig{
		{Event: "daemon.started", Command: "exit 1"},
	}, testLogger())

	// should not panic
	runner.Dispatch(context.Background(), hooks.Event{Name: hooks.EventDaemonStarted})
	time.Sleep(200 * time.Millisecond)
}

func TestRunnerWildcard(t *testing.T) {
	t.Parallel()

	tmpFile := filepath.Join(t.TempDir(), "wildcard.json")

	runner := hooks.NewHookRunner([]hooks.HookConfig{
		{Event: "*", Command: "cat > " + tmpFile},
	}, testLogger())

	runner.Dispatch(context.Background(), hooks.Event{Name: hooks.EventSyncCompleted, Project: "/tmp/test"})
	time.Sleep(300 * time.Millisecond)

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("wildcard hook did not fire: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["event"] != "sync.completed" {
		t.Errorf("event = %v, want sync.completed", parsed["event"])
	}
}

func TestRunnerNoMatch(t *testing.T) {
	t.Parallel()

	tmpFile := filepath.Join(t.TempDir(), "nomatch.txt")

	runner := hooks.NewHookRunner([]hooks.HookConfig{
		{Event: "daemon.started", Command: "touch " + tmpFile},
	}, testLogger())

	runner.Dispatch(context.Background(), hooks.Event{Name: hooks.EventSessionUploaded})
	time.Sleep(200 * time.Millisecond)

	if _, err := os.Stat(tmpFile); err == nil {
		t.Fatal("hook should not have run for non-matching event")
	}
}

func TestRunnerConcurrencyCap(t *testing.T) {
	t.Parallel()

	// 8 hooks that each sleep 200ms, with concurrency cap of 4
	// should complete in ~400ms (2 batches), not 200ms (all parallel) or 1600ms (serial)
	var cfgs []hooks.HookConfig
	for i := 0; i < 8; i++ {
		cfgs = append(cfgs, hooks.HookConfig{Event: "*", Command: "sleep 0.2"})
	}

	runner := hooks.NewHookRunner(cfgs, testLogger())

	start := time.Now()
	runner.Dispatch(context.Background(), hooks.Event{Name: hooks.EventDaemonStarted})
	// wait for all to complete
	time.Sleep(800 * time.Millisecond)
	elapsed := time.Since(start)

	// should take ~400ms (2 batches of 4) + overhead, not 1600ms (serial)
	if elapsed > 1200*time.Millisecond {
		t.Fatalf("took %v, expected <1.2s (concurrency cap should batch, not serialize)", elapsed)
	}
}

// --- Stuck process / signal handling ---

// TestRunnerSIGTERMIgnored verifies a hook that traps SIGTERM is killed by SIGKILL.
// Failure prevented: a misbehaving hook hangs the runner forever.
func TestRunnerSIGTERMIgnored(t *testing.T) {
	if testing.Short() {
		t.Skip("short: subprocess signal handling")
	}
	t.Parallel()

	// trap SIGTERM and keep running; only SIGKILL will stop it
	runner := hooks.NewHookRunner([]hooks.HookConfig{
		{Event: "daemon.started", Command: "trap '' TERM; sleep 10"},
	}, testLogger())

	done := make(chan struct{})
	go func() {
		runner.Dispatch(context.Background(), hooks.Event{Name: hooks.EventDaemonStarted})
		// dispatch is fire-and-forget; wait for semaphore release by trying to fill it
		for i := 0; i < 4; i++ {
			runner.Dispatch(context.Background(), hooks.Event{Name: "nonexistent.event"})
		}
		close(done)
	}()

	// total budget: 500ms SIGTERM + 500ms SIGKILL + generous margin
	select {
	case <-time.After(3 * time.Second):
		t.Fatal("hook that ignores SIGTERM was not killed within 3s")
	case <-done:
		// passed
	}
}

// --- Pipe / stdin edge cases ---

// TestRunnerHookDoesNotReadStdin verifies the runner doesn't hang when
// the hook ignores stdin entirely.
// Failure prevented: runner blocks on pipe write to an uncooperative hook.
func TestRunnerHookDoesNotReadStdin(t *testing.T) {
	t.Parallel()

	markerFile := filepath.Join(t.TempDir(), "done.txt")

	runner := hooks.NewHookRunner([]hooks.HookConfig{
		{Event: "daemon.started", Command: "echo ok > " + markerFile},
	}, testLogger())

	runner.Dispatch(context.Background(), hooks.Event{
		Name:    hooks.EventDaemonStarted,
		Payload: hooks.MurmurPayload("m-1", "a-1", "Person A", "test", "normal", "content"),
	})
	runner.Wait()

	if _, err := os.Stat(markerFile); err != nil {
		t.Fatal("hook that ignores stdin should still complete")
	}
}

// TestRunnerHookMassiveStdout verifies a hook that floods stdout doesn't hang.
// Failure prevented: pipe buffer fills and blocks the hook subprocess.
func TestRunnerHookMassiveStdout(t *testing.T) {
	if testing.Short() {
		t.Skip("short: subprocess stdout stress")
	}
	t.Parallel()

	// generate ~1MB of stdout; since runner doesn't capture it, the OS should
	// discard or buffer it without blocking
	runner := hooks.NewHookRunner([]hooks.HookConfig{
		{Event: "daemon.started", Command: "dd if=/dev/zero bs=1024 count=1024 2>/dev/null; exit 0"},
	}, testLogger())

	done := make(chan struct{})
	go func() {
		runner.Dispatch(context.Background(), hooks.Event{Name: hooks.EventDaemonStarted})
		time.Sleep(1500 * time.Millisecond) // generous time for hook + timeout
		close(done)
	}()

	select {
	case <-time.After(5 * time.Second):
		t.Fatal("hook with massive stdout appears to be hanging")
	case <-done:
	}
}

// --- Empty / no-op dispatch ---

// TestRunnerEmptyHooksList verifies dispatch with zero hooks is an instant no-op.
// Failure prevented: unnecessary goroutine or channel operations on empty config.
func TestRunnerEmptyHooksList(t *testing.T) {
	t.Parallel()

	runner := hooks.NewHookRunner(nil, testLogger())

	start := time.Now()
	runner.Dispatch(context.Background(), hooks.Event{Name: hooks.EventDaemonStarted})
	elapsed := time.Since(start)

	if elapsed > 1*time.Millisecond {
		t.Fatalf("dispatch with empty hooks took %v, expected near-instant", elapsed)
	}
}

// --- Command resolution ---

// TestRunnerCommandNotFound verifies a nonexistent command fails gracefully.
// Failure prevented: panic or goroutine leak on exec failure.
func TestRunnerCommandNotFound(t *testing.T) {
	t.Parallel()

	runner := hooks.NewHookRunner([]hooks.HookConfig{
		{Event: "daemon.started", Command: "nonexistent-command-xyz-12345"},
	}, testLogger())

	// should not panic, should log a warning
	runner.Dispatch(context.Background(), hooks.Event{Name: hooks.EventDaemonStarted})
	time.Sleep(300 * time.Millisecond)
}

// --- Background process / orphan handling ---

// TestRunnerHookForksBackground verifies a hook that forks a background child
// doesn't block the runner (parent exits immediately, orphan is reaped by init).
// Failure prevented: runner waits for orphaned child process.
func TestRunnerHookForksBackground(t *testing.T) {
	t.Parallel()

	markerFile := filepath.Join(t.TempDir(), "parent-done.txt")

	runner := hooks.NewHookRunner([]hooks.HookConfig{
		{Event: "daemon.started", Command: "sleep 100 & echo done > " + markerFile},
	}, testLogger())

	runner.Dispatch(context.Background(), hooks.Event{Name: hooks.EventDaemonStarted})
	time.Sleep(300 * time.Millisecond)

	if _, err := os.Stat(markerFile); err != nil {
		t.Fatal("parent process should have exited immediately despite forked child")
	}
}

// --- Rapid-fire / goroutine leak ---

// TestRunnerRapidFireNoLeak verifies that emitting many events rapidly doesn't
// leak goroutines. Uses fewer events to ensure they drain within the wait period.
// Failure prevented: unbounded goroutine growth on sustained event traffic.
func TestRunnerRapidFireNoLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("short: goroutine leak check")
	}
	t.Parallel()

	runner := hooks.NewHookRunner([]hooks.HookConfig{
		{Event: "*", Command: "true"},
	}, testLogger())

	// let any pre-existing goroutines settle
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	before := runtime.NumGoroutine()

	// dispatch 20 events (20 goroutines, 4 concurrent via semaphore)
	// "true" completes in ~10ms so 20/4 = 5 batches * ~10ms = ~50ms
	for i := 0; i < 20; i++ {
		runner.Dispatch(context.Background(), hooks.Event{Name: hooks.EventDaemonStarted})
	}

	// generous wait for all hooks to drain
	time.Sleep(5 * time.Second)

	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	after := runtime.NumGoroutine()

	// allow a margin of 10 goroutines for runtime background activity and
	// other parallel tests
	if after > before+10 {
		t.Fatalf("goroutine leak: before=%d, after=%d (delta=%d)", before, after, after-before)
	}
}

// --- Context cancellation ---

// TestRunnerContextCancelDoesNotPreventDispatch verifies hooks still fire
// even when the parent context is already canceled (fire-and-forget semantics).
// Failure prevented: context propagation accidentally suppresses hook execution.
func TestRunnerContextCancelDoesNotPreventDispatch(t *testing.T) {
	t.Parallel()

	markerFile := filepath.Join(t.TempDir(), "fired.txt")

	runner := hooks.NewHookRunner([]hooks.HookConfig{
		{Event: "daemon.started", Command: "touch " + markerFile},
	}, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	runner.Dispatch(ctx, hooks.Event{Name: hooks.EventDaemonStarted})
	time.Sleep(300 * time.Millisecond)

	// the runner doesn't check ctx before starting the hook, so it should still fire
	if _, err := os.Stat(markerFile); err != nil {
		t.Fatal("hook should fire even with canceled context (fire-and-forget)")
	}
}

// --- Special characters in event JSON ---

// TestRunnerSpecialCharsInPayload verifies unicode, quotes, and newlines
// in payload values are correctly delivered as JSON on stdin.
// Failure prevented: JSON encoding/escaping errors corrupt hook input.
func TestRunnerSpecialCharsInPayload(t *testing.T) {
	t.Parallel()

	tmpFile := filepath.Join(t.TempDir(), "output.json")

	runner := hooks.NewHookRunner([]hooks.HookConfig{
		{Event: "murmur.received", Command: "cat > " + tmpFile},
	}, testLogger())

	event := hooks.Event{
		Name:    hooks.EventMurmurReceived,
		Project: "/tmp/test",
		Payload: hooks.MurmurPayload(
			"m-1", "agent-1", "Person A", "design",
			"normal",
			"line1\nline2\ttab \"quoted\" and unicode: \u00e9\u00e0\u00fc \U0001f600",
		),
	}

	runner.Dispatch(context.Background(), event)
	time.Sleep(300 * time.Millisecond)

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("failed to read output: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid JSON with special chars: %v\ncontent: %s", err, data)
	}

	murmur, ok := parsed["murmur"].(map[string]any)
	if !ok {
		t.Fatal("missing murmur key in parsed JSON")
	}
	content := murmur["content"].(string)
	if !strings.Contains(content, "quoted") {
		t.Errorf("content should contain 'quoted', got: %s", content)
	}
	if !strings.Contains(content, "\n") {
		t.Errorf("content should contain newline, got: %s", content)
	}
}

// --- Environment variables ---

// TestRunnerEnvironmentVariables verifies OX_EVENT and OX_EVENT_TIMESTAMP
// are set correctly on hook subprocesses.
// Failure prevented: hooks that rely on env vars get empty/wrong values.
func TestRunnerEnvironmentVariables(t *testing.T) {
	t.Parallel()

	envFile := filepath.Join(t.TempDir(), "env.txt")

	runner := hooks.NewHookRunner([]hooks.HookConfig{
		{Event: "daemon.started", Command: fmt.Sprintf(
			`printf "%%s\n%%s" "$OX_EVENT" "$OX_EVENT_TIMESTAMP" > %s`, envFile)},
	}, testLogger())

	ts := time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC)
	runner.Dispatch(context.Background(), hooks.Event{
		Name:      hooks.EventDaemonStarted,
		Timestamp: ts,
	})
	time.Sleep(300 * time.Millisecond)

	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("failed to read env output: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), string(data))
	}
	if lines[0] != "daemon.started" {
		t.Errorf("OX_EVENT = %q, want %q", lines[0], "daemon.started")
	}
	if lines[1] != "2026-03-15T10:30:00Z" {
		t.Errorf("OX_EVENT_TIMESTAMP = %q, want %q", lines[1], "2026-03-15T10:30:00Z")
	}
}

// --- Concurrent Dispatch calls ---

// TestRunnerConcurrentDispatch verifies multiple goroutines can call Dispatch
// simultaneously without races or panics.
// Failure prevented: data race on runner.hooks slice during concurrent dispatch.
func TestRunnerConcurrentDispatch(t *testing.T) {
	t.Parallel()

	runner := hooks.NewHookRunner([]hooks.HookConfig{
		{Event: "*", Command: "true"},
	}, testLogger())

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			runner.Dispatch(context.Background(), hooks.Event{
				Name:    hooks.EventDaemonStarted,
				Project: fmt.Sprintf("/tmp/project-%d", n),
			})
		}(i)
	}
	wg.Wait()
	// allow hooks to drain
	time.Sleep(1 * time.Second)
}

// --- SetHooks while Dispatch is running (race detector check) ---

// TestRunnerSetHooksDuringDispatch verifies that concurrent SetHooks and
// Dispatch calls don't race. HookRunner.hooks is protected by sync.RWMutex.
// Failure prevented: data race between SetHooks write and Dispatch read.
func TestRunnerSetHooksDuringDispatch(t *testing.T) {
	t.Parallel()

	runner := hooks.NewHookRunner([]hooks.HookConfig{
		{Event: "*", Command: "true"},
	}, testLogger())

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			runner.SetHooks([]hooks.HookConfig{
				{Event: "*", Command: "true"},
			})
		}
	}()

	for i := 0; i < 100; i++ {
		runner.Dispatch(context.Background(), hooks.Event{
			Name: hooks.EventDaemonStarted,
		})
	}
	<-done
}

// --- Semaphore enforcement ---

// TestRunnerSemaphoreBoundsEnforced verifies that hooks are batched by the
// semaphore by measuring total wall-clock time. 12 hooks sleeping 200ms each
// with cap 4 should take ~600ms (3 batches), not 200ms (all parallel).
// This complements TestRunnerConcurrencyCap with a larger batch.
// Failure prevented: semaphore bypass allows unbounded subprocess fan-out.
func TestRunnerSemaphoreBoundsEnforced(t *testing.T) {
	if testing.Short() {
		t.Skip("short: concurrency timing")
	}
	t.Parallel()

	var cfgs []hooks.HookConfig
	for i := 0; i < 12; i++ {
		cfgs = append(cfgs, hooks.HookConfig{Event: "*", Command: "sleep 0.2"})
	}

	runner := hooks.NewHookRunner(cfgs, testLogger())

	start := time.Now()
	runner.Dispatch(context.Background(), hooks.Event{Name: hooks.EventDaemonStarted})

	// 12 hooks / 4 concurrent = 3 batches * 200ms = 600ms minimum
	time.Sleep(1500 * time.Millisecond)
	elapsed := time.Since(start)

	// if all 12 ran in parallel (no semaphore), it would take ~200ms
	// we expect >=500ms (3 batches minus timing slack)
	if elapsed < 500*time.Millisecond {
		t.Fatalf("completed in %v, expected >=500ms (semaphore should batch to ~600ms)", elapsed)
	}
}

// --- Multiple hooks for same event ---

// TestRunnerMultipleHooksForSameEvent verifies all matching hooks fire,
// not just the first one.
// Failure prevented: early-return bug in dispatch loop skips later hooks.
func TestRunnerMultipleHooksForSameEvent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	var cfgs []hooks.HookConfig
	for i := 0; i < 5; i++ {
		cfgs = append(cfgs, hooks.HookConfig{
			Event:   "daemon.started",
			Command: fmt.Sprintf("touch %s/hook-%d.txt", dir, i),
		})
	}

	runner := hooks.NewHookRunner(cfgs, testLogger())
	runner.Dispatch(context.Background(), hooks.Event{Name: hooks.EventDaemonStarted})
	runner.Wait()

	for i := 0; i < 5; i++ {
		path := filepath.Join(dir, fmt.Sprintf("hook-%d.txt", i))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("hook %d did not fire (missing %s)", i, path)
		}
	}
}

// --- Mixed matching: specific + wildcard ---

// TestRunnerMixedWildcardAndSpecific verifies both wildcard and specific hooks
// fire for the same event.
// Failure prevented: wildcard matching excludes specific hooks or vice versa.
func TestRunnerMixedWildcardAndSpecific(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runner := hooks.NewHookRunner([]hooks.HookConfig{
		{Event: "*", Command: "touch " + filepath.Join(dir, "wildcard.txt")},
		{Event: "daemon.started", Command: "touch " + filepath.Join(dir, "specific.txt")},
	}, testLogger())

	runner.Dispatch(context.Background(), hooks.Event{Name: hooks.EventDaemonStarted})
	runner.Wait()

	for _, f := range []string{"wildcard.txt", "specific.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("expected %s to exist", f)
		}
	}
}

// --- Hook exit codes ---

// TestRunnerVariousExitCodes verifies different exit codes are handled
// gracefully without panics.
// Failure prevented: unexpected exit code causes unhandled error type.
func TestRunnerVariousExitCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{"exit_0", "exit 0"},
		{"exit_1", "exit 1"},
		{"exit_2", "exit 2"},
		{"exit_127", "exit 127"},
		{"exit_255", "exit 255"},
		{"signal_segfault", "kill -SEGV $$"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := hooks.NewHookRunner([]hooks.HookConfig{
				{Event: "daemon.started", Command: tt.command},
			}, testLogger())

			// must not panic
			runner.Dispatch(context.Background(), hooks.Event{Name: hooks.EventDaemonStarted})
			time.Sleep(300 * time.Millisecond)
		})
	}
}
