package gitutil

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// makeZombieRebase builds a real repo on `main` and injects a structurally
// incomplete .git/rebase-merge directory — only an `autostash` entry, no
// head-name/orig-head — exactly the wedge a process killed mid-`pull --rebase
// --autostash` leaves behind (bd ox-j3cl). `git rebase --abort` CANNOT clear
// this: there is nothing to reset HEAD to. HEAD stays on `main`, so the branch
// still holds every original commit.
func makeZombieRebase(t *testing.T) (repo, headBefore string) {
	t.Helper()
	repo = t.TempDir()
	runGit(t, repo, "init", "--initial-branch=main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "f.txt"), []byte("base\n"), 0o644))
	runGit(t, repo, "add", "f.txt")
	runGit(t, repo, "commit", "-m", "base")

	// A real stash object stands in for the autostash git parks at rebase
	// start, so `git rebase --quit`'s autostash handling has a valid OID —
	// faithful to production and avoids an invalid-autostash test artifact.
	require.NoError(t, os.WriteFile(filepath.Join(repo, "f.txt"), []byte("dirty\n"), 0o644))
	stashOID := strings.TrimSpace(captureGit(t, repo, "stash", "create"))
	runGit(t, repo, "checkout", "--", "f.txt") // restore a clean tree

	headBefore = strings.TrimSpace(captureGit(t, repo, "rev-parse", "HEAD"))

	stateDir := filepath.Join(repo, ".git", "rebase-merge")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "autostash"), []byte(stashOID+"\n"), 0o644))

	require.True(t, IsRebaseInProgress(repo), "setup should look like a rebase in progress")
	return repo, headBefore
}

// TestAbortOrClearRebase_ZombieDirCleared is the core regression for bd ox-j3cl:
// a structurally-incomplete rebase-merge dir (autostash only) wedges every pull.
// `git rebase --abort` cannot clear it; AbortOrClearRebase escalates to
// `git rebase --quit` and clears it WITHOUT moving HEAD.
// Failure prevented: a corrupt rebase dir permanently suspends ledger sync.
func TestAbortOrClearRebase_ZombieDirCleared(t *testing.T) {
	if testing.Short() {
		t.Skip("short: spawns git subprocesses")
	}
	repo, headBefore := makeZombieRebase(t)

	// Prove the OLD recovery path can't clear this shape: plain abort fails and
	// leaves the wedge. This is what the pre-fix daemon/doctor did — and why the
	// ledger stayed stuck. Without the quit escalation below, this repo is dead.
	abortErr := AuditAndAbort(context.Background(), repo, AuditOpRebase, "regression: abort alone", quietLogger())
	require.Error(t, abortErr, "git rebase --abort must fail on a zombie dir (no head-name/orig-head)")
	require.True(t, IsRebaseInProgress(repo), "abort failure must leave the wedge in place")

	// The fix: abort→quit escalation clears it.
	err := AbortOrClearRebase(context.Background(), repo, "regression: clear zombie", quietLogger())
	require.NoError(t, err, "AbortOrClearRebase must clear a zombie rebase dir")
	assert.False(t, IsRebaseInProgress(repo), "rebase state must be gone after recovery")

	headAfter := strings.TrimSpace(captureGit(t, repo, "rev-parse", "HEAD"))
	assert.Equal(t, headBefore, headAfter, "quit must NOT move HEAD")
	branch := strings.TrimSpace(captureGit(t, repo, "rev-parse", "--abbrev-ref", "HEAD"))
	assert.Equal(t, "main", branch, "HEAD must remain on the original branch")
}

// TestAbortOrClearRebase_IntactStateUsesAbort proves the escalation does not
// change behavior for a NORMAL wedge: a real conflict-halted rebase has a
// complete, abortable state dir, so rung 1 (`git rebase --abort`) clears it and
// we never reach --quit. Failure prevented: over-eager --quit that skips the
// reversible abort on an intact rebase.
func TestAbortOrClearRebase_IntactStateUsesAbort(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git rebase operations")
	}
	_, repo := setupDivergentRepos(t, "conflict.txt", "local-side", "remote-side")
	require.True(t, IsRebaseInProgress(repo), "precondition: an intact conflict rebase")

	err := AbortOrClearRebase(context.Background(), repo, "intact abort", quietLogger())
	require.NoError(t, err)
	assert.False(t, IsRebaseInProgress(repo), "intact rebase must clear via --abort")
}

// TestAbortOrClearRebase_DetachedHeadRefusesQuit guards the one shape where
// --quit is UNSAFE: a detached HEAD means the rebase had already rewound and
// was mid-replay, where dropping the state dir would strand a partial replay.
// The recovery must refuse and surface the error for a human.
// Failure prevented: silent data loss by quitting a genuinely in-flight rebase.
func TestAbortOrClearRebase_DetachedHeadRefusesQuit(t *testing.T) {
	if testing.Short() {
		t.Skip("short: spawns git subprocesses")
	}
	repo := t.TempDir()
	runGit(t, repo, "init", "--initial-branch=main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "f.txt"), []byte("base\n"), 0o644))
	runGit(t, repo, "add", "f.txt")
	runGit(t, repo, "commit", "-m", "base")
	head := strings.TrimSpace(captureGit(t, repo, "rev-parse", "HEAD"))
	// Detach BEFORE injecting the state dir — git refuses checkout mid-rebase.
	runGit(t, repo, "checkout", "--detach", head)

	stateDir := filepath.Join(repo, ".git", "rebase-merge")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	// autostash content is irrelevant here — we must bail before the quit.
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "autostash"),
		[]byte("0000000000000000000000000000000000000000\n"), 0o644))
	require.True(t, IsRebaseInProgress(repo))

	err := AbortOrClearRebase(context.Background(), repo, "detached guard", quietLogger())
	require.Error(t, err, "must refuse to quit when HEAD is detached (mid-replay risk)")
	assert.True(t, IsRebaseInProgress(repo), "the state dir must be left intact for a human")
}
