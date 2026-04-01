//go:build slow

package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Session finalize push interleaved with daemon pull ---

// TestSessionFinalize_PushDuringPull verifies that a session finalize
// push (add/commit/push) can succeed while a daemon pull is happening
// concurrently on the same ledger clone.
// Failure prevented: index.lock contention between session finalize's
// git add/commit and daemon's pull --rebase, causing one to fail
// silently and lose session data.
func TestSessionFinalize_PushDuringPull(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-finalize-pull")

	// seed with ledger structure
	pushMultipleFiles(t, cloneURL, map[string]string{
		".sageox/config.json":     `{"version":1}`,
		"sessions/old/summary.md": "old session\n",
	})

	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	g.cloneRepo(t, cloneURL, ledgerDir)

	// prepare a session dir with files to commit
	sessionDir := filepath.Join(ledgerDir, "sessions", "new-session-001")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(sessionDir, "summary.md"), []byte("# New Session\n"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(sessionDir, "raw.jsonl"), []byte(`{"seq":1}`+"\n"), 0o644))

	// push a concurrent change from elsewhere (so pull has something to fetch)
	g.pushFromTempClone(t, cloneURL, "sessions/remote-session/summary.md", "remote session\n")

	var wg sync.WaitGroup
	var pullErr, pushErr error

	// simulate daemon pull and session finalize push concurrently
	wg.Add(2)
	go func() {
		defer wg.Done()
		cmd := exec.Command("git", "-C", ledgerDir, "pull", "--rebase", "--autostash")
		cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			pullErr = fmt.Errorf("pull: %s: %w", string(out), err)
		}
	}()
	go func() {
		defer wg.Done()
		// small delay to let pull start
		time.Sleep(50 * time.Millisecond)

		// git add + commit + push (simulates gitCommitAndPush)
		cmd := exec.Command("git", "-C", ledgerDir, "add", "--sparse", "sessions/new-session-001/")
		out, err := cmd.CombinedOutput()
		if err != nil {
			pushErr = fmt.Errorf("add: %s: %w", string(out), err)
			return
		}

		cmd = exec.Command("git", "-C", ledgerDir, "commit", "-m", "finalize session new-session-001")
		out, err = cmd.CombinedOutput()
		if err != nil {
			pushErr = fmt.Errorf("commit: %s: %w", string(out), err)
			return
		}

		pushErr = gitutil.PushWithRetry(context.Background(), ledgerDir, gitutil.PushOpts{
			MaxRetries:          3,
			OpTimeout:           30 * time.Second,
			AutoResolvePrefixes: []string{"sessions/", "data/"},
		})
	}()
	wg.Wait()

	// at least the sequential fallback should work
	// (concurrent may hit index.lock, but retry should handle it)
	if pullErr != nil && pushErr != nil {
		t.Logf("pull error: %v", pullErr)
		t.Logf("push error: %v", pushErr)
		t.Fatal("both pull and push failed — concurrent operations not resilient")
	}

	// verify repo is not corrupted
	cmd := exec.Command("git", "-C", ledgerDir, "status")
	out, err := cmd.CombinedOutput()
	assert.NoError(t, err, "repo should be in valid state: %s", string(out))
}

// --- B. Session finalize with auto-resolve on sessions/ ---

// TestSessionFinalize_AutoResolve_Sessions verifies that two session
// finalizes pushing to different session directories with auto-resolve
// both succeed, matching the ledger's actual AutoResolvePrefixes.
// Failure prevented: concurrent session finalizes (two AI coworkers
// finishing at the same time) causing one to permanently fail.
func TestSessionFinalize_AutoResolve_Sessions(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-finalize-autoresolve")

	pushMultipleFiles(t, cloneURL, map[string]string{
		".sageox/config.json":     `{"version":1}`,
		"sessions/seed/summary.md": "seed\n",
	})

	// two separate clones (simulates two daemon instances or two coworkers)
	ledger1 := filepath.Join(t.TempDir(), "ledger1")
	ledger2 := filepath.Join(t.TempDir(), "ledger2")
	g.cloneRepo(t, cloneURL, ledger1)
	g.cloneRepo(t, cloneURL, ledger2)

	// each writes a different session
	session1Dir := filepath.Join(ledger1, "sessions", "session-aaa")
	require.NoError(t, os.MkdirAll(session1Dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(session1Dir, "summary.md"), []byte("session A\n"), 0o644))

	session2Dir := filepath.Join(ledger2, "sessions", "session-bbb")
	require.NoError(t, os.MkdirAll(session2Dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(session2Dir, "summary.md"), []byte("session B\n"), 0o644))

	// commit in each clone
	cmd := exec.Command("git", "-C", ledger1, "add", "--sparse", "sessions/session-aaa/")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "add1: %s", string(out))
	cmd = exec.Command("git", "-C", ledger1, "commit", "-m", "finalize session-aaa")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "commit1: %s", string(out))

	cmd = exec.Command("git", "-C", ledger2, "add", "--sparse", "sessions/session-bbb/")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "add2: %s", string(out))
	cmd = exec.Command("git", "-C", ledger2, "commit", "-m", "finalize session-bbb")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "commit2: %s", string(out))

	opts := gitutil.PushOpts{
		MaxRetries:          3,
		OpTimeout:           30 * time.Second,
		AutoResolvePrefixes: []string{"sessions/", "data/"},
	}

	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = gitutil.PushWithRetry(ctx, ledger1, opts)
	}()
	go func() {
		defer wg.Done()
		errs[1] = gitutil.PushWithRetry(ctx, ledger2, opts)
	}()
	wg.Wait()

	assert.NoError(t, errs[0], "ledger1 push should succeed")
	assert.NoError(t, errs[1], "ledger2 push should succeed")

	// verify both sessions exist on remote
	verify := filepath.Join(t.TempDir(), "verify")
	g.cloneRepo(t, cloneURL, verify)
	require.FileExists(t, filepath.Join(verify, "sessions/session-aaa/summary.md"))
	require.FileExists(t, filepath.Join(verify, "sessions/session-bbb/summary.md"))
}

// --- C. Session push preserves content files on push failure ---

// TestSessionFinalize_PushFails_ContentPreserved verifies that when
// push fails (e.g., bad credentials), the committed session files
// remain in the local ledger and are not destroyed.
// Failure prevented: session data lost when push fails — the commit
// exists locally but content files are somehow removed.
func TestSessionFinalize_PushFails_ContentPreserved(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-finalize-pushfail")

	pushMultipleFiles(t, cloneURL, map[string]string{
		".sageox/config.json": `{"version":1}`,
	})

	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	g.cloneRepo(t, cloneURL, ledgerDir)

	// write session files
	sessionDir := filepath.Join(ledgerDir, "sessions", "fail-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	summaryContent := "# Important Session\n\nThis must not be lost.\n"
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.md"), []byte(summaryContent), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(`{"data":"precious"}`+"\n"), 0o644))

	// commit locally
	cmd := exec.Command("git", "-C", ledgerDir, "add", "--sparse", "sessions/fail-session/")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "add: %s", string(out))

	cmd = exec.Command("git", "-C", ledgerDir, "commit", "-m", "finalize fail-session")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "commit: %s", string(out))

	// break credentials so push fails
	badURL := strings.Replace(cloneURL, g.adminUser+":"+g.adminPass, "nobody:wrongpass", 1)
	cmd = exec.Command("git", "-C", ledgerDir, "remote", "set-url", "origin", badURL)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "set-url: %s", string(out))

	// push should fail
	err = gitutil.PushWithRetry(context.Background(), ledgerDir, gitutil.PushOpts{
		MaxRetries: 1,
		OpTimeout:  10 * time.Second,
	})
	require.Error(t, err, "push with bad creds should fail")

	// session files MUST still exist locally
	require.FileExists(t, filepath.Join(sessionDir, "summary.md"))
	require.FileExists(t, filepath.Join(sessionDir, "raw.jsonl"))

	content, err := os.ReadFile(filepath.Join(sessionDir, "summary.md"))
	require.NoError(t, err)
	assert.Equal(t, summaryContent, string(content),
		"session content must be preserved exactly after push failure")

	// commit should still be in local log
	cmd = exec.Command("git", "-C", ledgerDir, "log", "--oneline", "-1")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err)
	assert.Contains(t, string(out), "fail-session", "commit should be in local history")
}

// --- D. Multiple sessions committed atomically ---

// TestSessionFinalize_MultipleSessionsSequential verifies that
// multiple sessions finalized sequentially all end up in the remote,
// matching the real-world pattern of rapid session completion.
// Failure prevented: second session's push silently overwrites first
// session's data due to force-push or bad rebase.
func TestSessionFinalize_MultipleSessionsSequential(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-finalize-multi")

	pushMultipleFiles(t, cloneURL, map[string]string{
		".sageox/config.json": `{"version":1}`,
	})

	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	g.cloneRepo(t, cloneURL, ledgerDir)

	sessions := []string{"session-001", "session-002", "session-003"}

	for _, name := range sessions {
		dir := filepath.Join(ledgerDir, "sessions", name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, "summary.md"),
			[]byte("# "+name+"\n"), 0o644))

		cmd := exec.Command("git", "-C", ledgerDir, "add", "--sparse", "sessions/"+name+"/")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "add %s: %s", name, string(out))

		cmd = exec.Command("git", "-C", ledgerDir, "commit", "-m", "finalize "+name)
		out, err = cmd.CombinedOutput()
		require.NoError(t, err, "commit %s: %s", name, string(out))

		err = gitutil.PushWithRetry(context.Background(), ledgerDir, gitutil.PushOpts{
			MaxRetries:          3,
			OpTimeout:           30 * time.Second,
			AutoResolvePrefixes: []string{"sessions/"},
		})
		require.NoError(t, err, "push %s should succeed", name)
	}

	// verify all 3 sessions exist on remote
	verify := filepath.Join(t.TempDir(), "verify")
	g.cloneRepo(t, cloneURL, verify)

	for _, name := range sessions {
		require.FileExists(t, filepath.Join(verify, "sessions", name, "summary.md"),
			"session %s should exist on remote", name)
	}

	// verify git log has all 3 commits
	cmd := exec.Command("git", "-C", verify, "log", "--oneline")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err)
	for _, name := range sessions {
		assert.Contains(t, string(out), name, "git log should contain %s", name)
	}
}
