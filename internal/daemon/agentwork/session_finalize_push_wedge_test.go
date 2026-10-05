package agentwork

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rejectingRemote installs a pre-receive hook on bare that declines every push
// the way GitLab declines a pack whose LFS objects it lacks, and counts the
// pushes that reach it.
func rejectingRemote(t *testing.T, bare string) (hits func() int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("pre-receive hook fixture needs a POSIX shell")
	}
	hitLog := filepath.Join(t.TempDir(), "hits")
	hook := "#!/bin/sh\necho hit >> '" + hitLog + "'\n" +
		"echo 'remote: GitLab: LFS objects are missing. Ensure LFS is properly set up or try a manual \"git lfs push --all\".' >&2\n" +
		"exit 1\n"
	require.NoError(t, os.MkdirAll(filepath.Join(bare, "hooks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bare, "hooks", "pre-receive"), []byte(hook), 0o755))
	return func() int {
		data, err := os.ReadFile(hitLog)
		if os.IsNotExist(err) {
			return 0
		}
		require.NoError(t, err)
		return strings.Count(string(data), "hit")
	}
}

func uploadOnlyWorkItem(t *testing.T, ledger, name string) *WorkItem {
	t.Helper()
	cacheDir := writeProductionShapedSession(t, ledger, name)
	return &WorkItem{
		ID:       "wedge-" + name,
		Type:     sessionFinalizeType,
		DedupKey: sessionFinalizeType + ":" + name,
		Payload: &SessionFinalizePayload{
			SessionDir: cacheDir,
			RawPath:    filepath.Join(cacheDir, "raw.jsonl"),
			LedgerPath: ledger,
			UploadOnly: true,
		},
	}
}

// ledgerSession writes a session straight into <ledger>/sessions/<name> (no
// cache hop) and returns its payload plus the pointer refs a real upload would
// have produced for it.
func ledgerSession(t *testing.T, ledger, name string) (*SessionFinalizePayload, map[string]lfs.FileRef) {
	t.Helper()
	dir := filepath.Join(ledger, "sessions", name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"title":"clean"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(testRawContent), 0o644))
	return &SessionFinalizePayload{SessionDir: dir, RawPath: filepath.Join(dir, "raw.jsonl"), LedgerPath: ledger},
		map[string]lfs.FileRef{"raw.jsonl": lfs.NewFileRef([]byte(testRawContent))}
}

// Failure prevented: a ledger the remote keeps rejecting made every queued
// finalize item repeat the same rejected push and the same failed LFS repair
// (~250 repairs a day, ~50 s each, under the ledger lock). #1145's pause only
// fired for unmerged index entries, which this ledger did not have.
//
// The remote's own push counter is the observable: after the first item trips
// the breaker, the manager pauses the whole type, and even an item that runs
// anyway commits locally but never reaches the remote again.
func TestFinalize_WedgedPushPausesTypeAndNothingElsePushes(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git remote and mocked LFS")
	}
	bare, ledger := setupBareAndCloneLedger(t)
	hits := rejectingRemote(t, bare)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	handler := newGitBackedHandler()
	handler.logger = logger
	// the fake LFS store has the uploaded blobs but 404s the raw OID on the
	// download check the repair makes; the cache still holds the real bytes, so
	// the repair refuses to blank them and errors — the shape of a wedge the
	// repair cannot fix
	rawOID := lfs.ComputeOID([]byte(testRawContent))
	enableLocalFinalizeLFS(t, handler, ledger, rawOID)

	m := NewManager(NewMockRunner(true), logger, func() *config.AgentWorkerConfig { return enabledConfigWith(1, 1000) }, make(chan struct{}, 1), ledger, "")
	m.RegisterHandler(handler)

	first := uploadOnlyWorkItem(t, ledger, "2026-01-15T10-00-testuser-OxWEDGEA")
	err := handler.ProcessResult(first, &RunResult{})
	require.ErrorIs(t, err, ErrLedgerPushWedged, "the finalize that trips the breaker must say so")
	require.ErrorIs(t, err, gitutil.ErrPushWedged)
	assert.Equal(t, 1, hits(), "the rejected push reached the remote once")
	until, wedged := gitutil.PushWedgedUntil(ledger)
	require.True(t, wedged)
	assert.WithinDuration(t, time.Now().Add(5*time.Minute), until, 15*time.Second)

	// a second item processed directly: it still commits locally, and no push
	// is attempted while the breaker is open
	headBefore := gitOutput(t, ledger, "rev-parse", "HEAD")
	second := uploadOnlyWorkItem(t, ledger, "2026-01-15T11-00-testuser-OxWEDGEB")
	err = handler.ProcessResult(second, &RunResult{})
	require.ErrorIs(t, err, ErrLedgerPushWedged)
	assert.Equal(t, 1, hits(), "no push while wedged")
	assert.NotEqual(t, headBefore, gitOutput(t, ledger, "rev-parse", "HEAD"), "the session is still committed locally")
	assert.Equal(t, 1, strings.Count(logBuf.String(), "ledger push wedged: LFS objects missing"), "one warning when the breaker opened")

	// through the manager: the first failure pauses the type, so queued items
	// are dropped without touching the handler or the ledger
	third := uploadOnlyWorkItem(t, ledger, "2026-01-15T12-00-testuser-OxWEDGEC")
	fourth := uploadOnlyWorkItem(t, ledger, "2026-01-15T13-00-testuser-OxWEDGED")
	headBefore = gitOutput(t, ledger, "rev-parse", "HEAD")
	m.executeItem(context.Background(), third)
	require.True(t, m.isTypePaused(sessionFinalizeType, m.now()), "a wedged push pauses the whole work type")
	headAfterThird := gitOutput(t, ledger, "rev-parse", "HEAD")
	m.executeItem(context.Background(), fourth)
	assert.Equal(t, headAfterThird, gitOutput(t, ledger, "rev-parse", "HEAD"), "paused: the next item must not run at all")
	assert.NotEqual(t, headBefore, headAfterThird, "the item that discovered the wedge did commit locally")
	assert.Equal(t, 1, hits())
	assert.Equal(t, 1, strings.Count(logBuf.String(), "ledger push is wedged by LFS objects missing"), "one pause warning per window")
}

// Failure prevented: processUploadOnly had two `return nil` failure exits, so a
// session that could never be uploaded counted as a SUCCESS: the manager reset
// its failure count and #1145's retry cap never engaged. 146 such sessions
// retried forever.
//
// The observable difference is the manager's own verdict: after maxRetries runs
// the dedup key is parked.
func TestProcessUploadOnly_FailureExitsReachTheRetryCap(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	tests := []struct {
		name    string
		prepare func(t *testing.T, handler *SessionFinalizeHandler, ledger string, item *WorkItem)
		wantErr string
	}{
		{
			name: "session cannot be staged into the ledger",
			prepare: func(t *testing.T, _ *SessionFinalizeHandler, ledger string, item *WorkItem) {
				// a regular file where the session directory must go
				dest := filepath.Join(ledger, "sessions", filepath.Base(item.Payload.(*SessionFinalizePayload).SessionDir))
				require.NoError(t, os.WriteFile(dest, []byte("in the way"), 0o644))
			},
			wantErr: "stage session",
		},
		{
			name: "pointer stub references a blob the remote lacks",
			prepare: func(t *testing.T, handler *SessionFinalizeHandler, ledger string, item *WorkItem) {
				dir := item.Payload.(*SessionFinalizePayload).SessionDir
				missingOID := strings.Repeat("e", 64)
				require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(lfs.FormatPointer("sha256:"+missingOID, 100)), 0o644))
				enableLocalFinalizeLFS(t, handler, ledger, missingOID)
			},
			wantErr: "missing from the remote",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ledger := setupBareAndCloneLedger(t)
			handler := newGitBackedHandler()
			m := NewManager(NewMockRunner(true), nil, func() *config.AgentWorkerConfig { return enabledConfigWith(1, 1000) }, make(chan struct{}, 1), ledger, "")
			m.RegisterHandler(handler)
			name := "2026-01-15T10-00-testuser-OxFAILEXIT"
			item := uploadOnlyWorkItem(t, ledger, name)
			tt.prepare(t, handler, ledger, item)

			err := handler.ProcessResult(item, &RunResult{})
			require.Error(t, err, "a session that cannot be uploaded is a failure, not a success")
			require.ErrorContains(t, err, tt.wantErr)

			// drive the real manager: nil would clear the failure count every time
			for i := 0; i < maxRetries; i++ {
				m.executeItem(context.Background(), uploadOnlyWorkItem2(item))
			}
			assert.True(t, m.isSuppressed(item, m.now()), "after %d failures the key is parked", maxRetries)
		})
	}
}

// uploadOnlyWorkItem2 is a fresh queue entry for the same session, the way the
// next detect scan hands it out.
func uploadOnlyWorkItem2(item *WorkItem) *WorkItem {
	payload := *item.Payload.(*SessionFinalizePayload)
	return &WorkItem{ID: item.ID, Type: item.Type, DedupKey: item.DedupKey, Payload: &payload}
}

// Failure prevented: gitCommitAndPush ran every git step under
// context.Background(), so a daemon shutdown could not interrupt a push, and the
// untracked goroutine kept holding the ledger lock until the 5 s goroutine-wait
// deadline forced an unclean exit.
//
// The remote here never answers: a helper that blocks until its parent git
// process dies. Canceling the daemon context must end the push promptly.
func TestGitCommitAndPush_ReturnsPromptlyWhenContextCancelledMidPush(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git with a hanging remote helper")
	}
	if runtime.GOOS == "windows" {
		t.Skip("remote helper fixture needs a POSIX shell")
	}
	_, ledger := setupBareAndCloneLedger(t)

	// A remote helper that never answers. It blocks on stdin, which only the
	// parent git process holds open, so it exits the moment git is killed — the
	// way git-remote-https does — and cannot strand the output pipe.
	helperDir := t.TempDir()
	started := filepath.Join(helperDir, "started")
	pidFile := filepath.Join(helperDir, "pid")
	script := "#!/bin/sh\necho $$ > '" + pidFile + "'\ntouch '" + started + "'\nexec cat > /dev/null\n"
	require.NoError(t, os.WriteFile(filepath.Join(helperDir, "git-remote-hang"), []byte(script), 0o755))
	t.Setenv("PATH", helperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() { // never leak the helper, whatever the test outcome
		if raw, err := os.ReadFile(pidFile); err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); convErr == nil {
				if proc, findErr := os.FindProcess(pid); findErr == nil {
					_ = proc.Kill()
				}
			}
		}
		// belt and braces: anything still running from helperDir (a killed git
		// orphans its helper) — leaked helpers outlived a test run for hours
		_ = exec.Command("pkill", "-f", helperDir).Run()
	})
	runGitCmd(t, ledger, "remote", "set-url", "--push", "origin", "hang::nowhere")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler := newGitBackedHandler()
	handler.SetDaemonContext(ctx)
	payload, refs := ledgerSession(t, ledger, "2026-01-15T10-00-testuser-OxCANCEL")

	type outcome struct {
		pushed bool
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		pushed, err := handler.gitCommitAndPush(payload, refs)
		done <- outcome{pushed, err}
	}()

	require.Eventually(t, func() bool { _, err := os.Stat(started); return err == nil },
		10*time.Second, 20*time.Millisecond, "the push must actually be in flight before canceling")
	cancelledAt := time.Now()
	cancel()

	select {
	case got := <-done:
		assert.False(t, got.pushed)
		require.Error(t, got.err)
		assert.Less(t, time.Since(cancelledAt), 4*time.Second, "cancellation must end the push promptly")
	case <-time.After(10 * time.Second):
		t.Fatal("gitCommitAndPush did not return after the daemon context was canceled")
	}
}

// Failure prevented: an unmerged index found INSIDE the commit transaction, or
// a write-tree failure from a conflict that appeared mid-transaction, surfaced
// as a plain error — so #1145's pause never fired and 733 commit transactions a
// day failed individually.
func TestGitCommitAndPush_UnmergedIndexIsErrLedgerUnresolved(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	tests := []struct {
		name string
		// arrange runs after the ledger exists; it returns a hook for the commit
		// transaction's staged-but-uncommitted window, or nil
		arrange   func(t *testing.T, ledger string) func()
		wantErr   error
		wantInMsg string // proves WHICH check fired
	}{
		{
			name:      "conflict already in the index before the transaction",
			arrange:   func(t *testing.T, ledger string) func() { wedgeLedger(t, ledger); return nil },
			wantErr:   ErrLedgerUnresolved,
			wantInMsg: "commit blocked",
		},
		{
			name: "conflict appears between staging and commit (write-tree refuses)",
			arrange: func(t *testing.T, ledger string) func() {
				return func() { injectUnmergedEntry(t, ledger, "sessions/other/meta.json") }
			},
			wantErr:   ErrLedgerUnresolved,
			wantInMsg: "snapshot Ledger index",
		},
		{
			name:    "negative control: healthy ledger commits and pushes",
			arrange: func(*testing.T, string) func() { return nil },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ledger := setupBareAndCloneLedger(t)
			handler := newGitBackedHandler()
			payload, refs := ledgerSession(t, ledger, "2026-01-15T10-00-testuser-OxUNMERGED")
			handler.afterStageTestHook = tt.arrange(t, ledger)

			pushed, err := handler.gitCommitAndPush(payload, refs)

			if tt.wantErr == nil {
				require.NoError(t, err)
				assert.True(t, pushed)
				return
			}
			assert.False(t, pushed)
			require.ErrorIs(t, err, tt.wantErr)
			assert.True(t, isLedgerWideBlocker(err))
			assert.ErrorContains(t, err, tt.wantInMsg)
		})
	}
}

// injectUnmergedEntry leaves path unmerged in the index (stages 2 and 3), the
// state a conflicting pull leaves behind.
func injectUnmergedEntry(t *testing.T, ledger, rel string) {
	t.Helper()
	abs := filepath.Join(ledger, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
	var oids [2]string
	for i, content := range []string{"ours\n", "theirs\n"} {
		require.NoError(t, os.WriteFile(abs, []byte(content), 0o644))
		oids[i] = gitOutput(t, ledger, "hash-object", "-w", abs)
	}
	cmd := exec.Command("git", "update-index", "--index-info")
	cmd.Dir = ledger
	cmd.Stdin = strings.NewReader(fmt.Sprintf("100644 %s 2\t%s\n100644 %s 3\t%s\n", oids[0], rel, oids[1], rel))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	require.Contains(t, gitOutput(t, ledger, "ls-files", "--unmerged"), rel)
}

// The type pause applies to both ledger-wide blockers, with a message that
// names the real cause, and only once per window.
func TestManager_LedgerWideBlockersPauseTheType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		err     error
		wantMsg string
	}{
		{name: "unresolved index", err: fmt.Errorf("finalize blocked: %w", ErrLedgerUnresolved), wantMsg: "unresolved index conflicts"},
		{name: "wedged push", err: fmt.Errorf("%w: %w", ErrLedgerPushWedged, gitutil.ErrPushWedged), wantMsg: "push is wedged"},
		{name: "ordinary failure is not a pause", err: errors.New("upload failed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logBuf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
			h := &mockHandler{typ: "session-finalize", processErr: tt.err}
			m := NewManager(NewMockRunner(true), logger, func() *config.AgentWorkerConfig { return enabledConfigWith(1, 1000) }, make(chan struct{}, 1), t.TempDir(), "")
			m.RegisterHandler(h)
			clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			m.now = func() time.Time { return clock }

			h.detectItems = []*WorkItem{
				{Type: "session-finalize", DedupKey: "session-finalize:a"},
				{Type: "session-finalize", DedupKey: "session-finalize:b"},
			}
			runDetectCycle(t, m)

			if tt.wantMsg == "" {
				assert.False(t, m.isTypePaused("session-finalize", clock))
				assert.Equal(t, 2, int(h.processCalls.Load()), "an ordinary failure still runs the other items")
				return
			}
			assert.True(t, m.isTypePaused("session-finalize", clock))
			assert.Equal(t, 1, int(h.processCalls.Load()), "the second item is dropped once the type is paused")
			assert.Equal(t, 1, strings.Count(logBuf.String(), tt.wantMsg), "one warning per pause window")
		})
	}
}

// Failure prevented: work-item goroutines were bare `go func` literals no
// WaitGroup saw, so shutdown (which waits only on the daemon's WaitGroup)
// could finish — or time out — with a finalize still mid-push.
func TestManager_StartWaitsForInFlightItemsOnStop(t *testing.T) {
	tests := []struct {
		name         string
		grace        time.Duration
		releaseAfter time.Duration // zero: never released within the test
		wantWaited   bool
	}{
		{name: "item finishes within the grace period", grace: 5 * time.Second, releaseAfter: 150 * time.Millisecond, wantWaited: true},
		{name: "stuck item is abandoned after the grace period", grace: 150 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			h := &blockingHandler{started: started, release: release}
			m := NewManager(NewMockRunner(true), nil, func() *config.AgentWorkerConfig { return enabledConfigWith(1, 1000) }, make(chan struct{}, 1), t.TempDir(), "")
			m.RegisterHandler(h)
			m.stopGrace = tt.grace

			ctx, cancel := context.WithCancel(context.Background())
			stopped := make(chan time.Time, 1)
			go func() {
				m.Start(ctx)
				stopped <- time.Now()
			}()
			require.True(t, m.Enqueue(&WorkItem{Type: "blocking", DedupKey: "blocking:a"}))
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("the work item never started")
			}

			cancelledAt := time.Now()
			cancel()
			if tt.releaseAfter > 0 {
				time.AfterFunc(tt.releaseAfter, func() { close(release) })
			} else {
				t.Cleanup(func() { close(release) })
			}

			select {
			case stoppedAt := <-stopped:
				waited := stoppedAt.Sub(cancelledAt)
				if tt.wantWaited {
					assert.GreaterOrEqual(t, waited, tt.releaseAfter-10*time.Millisecond, "Start must not return while an item is still running")
				} else {
					assert.Less(t, waited, 3*time.Second, "a stuck item must not hold shutdown past the grace period")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Start never returned")
			}
		})
	}
}

// blockingHandler's ProcessResult blocks until released, standing in for a
// finalize mid-push.
type blockingHandler struct {
	started chan struct{}
	release chan struct{}
}

func (h *blockingHandler) Type() string { return "blocking" }
func (h *blockingHandler) Detect(string) ([]*WorkItem, error) {
	return nil, nil
}
func (h *blockingHandler) BuildPrompt(*WorkItem) (RunRequest, error) {
	return RunRequest{SkipLLM: true}, nil
}
func (h *blockingHandler) ProcessResult(*WorkItem, *RunResult) error {
	close(h.started)
	<-h.release
	return nil
}
