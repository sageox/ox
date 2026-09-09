package gitutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasLockFiles(t *testing.T) {
	t.Run("no lock files", func(t *testing.T) {
		gitDir := filepath.Join(t.TempDir(), ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))

		assert.Empty(t, HasLockFiles(gitDir))
	})

	t.Run("all lock types present", func(t *testing.T) {
		gitDir := filepath.Join(t.TempDir(), ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))

		for _, lock := range knownLockFiles {
			require.NoError(t, os.WriteFile(filepath.Join(gitDir, lock), []byte{}, 0644))
		}

		found := HasLockFiles(gitDir)
		assert.Len(t, found, len(knownLockFiles))
		for _, lock := range knownLockFiles {
			assert.Contains(t, found, lock)
		}
	})

	t.Run("partial locks", func(t *testing.T) {
		gitDir := filepath.Join(t.TempDir(), ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))

		// create all, then remove one
		for _, lock := range knownLockFiles {
			require.NoError(t, os.WriteFile(filepath.Join(gitDir, lock), []byte{}, 0644))
		}
		require.NoError(t, os.Remove(filepath.Join(gitDir, "index.lock")))

		found := HasLockFiles(gitDir)
		assert.Len(t, found, len(knownLockFiles)-1)
		assert.NotContains(t, found, "index.lock")
	})

	t.Run("nonexistent directory", func(t *testing.T) {
		found := HasLockFiles("/nonexistent/path/.git")
		assert.Empty(t, found)
	})
}

func TestRemoveStaleLockFiles(t *testing.T) {
	t.Run("removes old locks", func(t *testing.T) {
		gitDir := filepath.Join(t.TempDir(), ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))

		lockPath := filepath.Join(gitDir, "index.lock")
		require.NoError(t, os.WriteFile(lockPath, []byte{}, 0644))
		// backdate to look old
		oldTime := time.Now().Add(-(AbandonedLockAge + time.Second))
		require.NoError(t, os.Chtimes(lockPath, oldTime, oldTime))

		removed, errs := RemoveStaleLockFiles(gitDir)
		assert.Empty(t, errs)
		assert.Equal(t, []string{"index.lock"}, removed)
		assert.Empty(t, HasLockFiles(gitDir))
	})

	t.Run("preserves fresh locks", func(t *testing.T) {
		gitDir := filepath.Join(t.TempDir(), ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))

		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "index.lock"), []byte{}, 0644))
		// do NOT backdate — file is brand new

		removed, errs := RemoveStaleLockFiles(gitDir)
		assert.Empty(t, errs)
		assert.Empty(t, removed)
		assert.NotEmpty(t, HasLockFiles(gitDir))
	})

	t.Run("no locks present", func(t *testing.T) {
		gitDir := filepath.Join(t.TempDir(), ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))

		removed, errs := RemoveStaleLockFiles(gitDir)
		assert.Empty(t, errs)
		assert.Empty(t, removed)
	})

	t.Run("nonexistent directory", func(t *testing.T) {
		removed, errs := RemoveStaleLockFiles("/nonexistent/.git")
		assert.Empty(t, errs) // missing files are not errors
		assert.Empty(t, removed)
	})
}

func TestIsRebaseInProgress(t *testing.T) {
	t.Run("clean repo", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0755))

		assert.False(t, IsRebaseInProgress(repo))
	})

	t.Run("rebase-merge exists", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "rebase-merge"), 0755))

		assert.True(t, IsRebaseInProgress(repo))
	})

	t.Run("rebase-apply exists", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "rebase-apply"), 0755))

		assert.True(t, IsRebaseInProgress(repo))
	})

	t.Run("both exist", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "rebase-merge"), 0755))
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "rebase-apply"), 0755))

		assert.True(t, IsRebaseInProgress(repo))
	})
}

// TestRebaseAge pins the fresh-vs-stale distinction the daemon relies on to
// decide whether to auto-recover a wedged rebase.
// Failure prevented: without an age signal the daemon either skips a
// pre-existing wedge forever (stranding every new session — the original bug)
// or aborts a rebase its own pull just started seconds ago.
func TestRebaseAge(t *testing.T) {
	t.Run("no rebase in progress", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0755))

		_, inProgress := RebaseAge(repo)
		assert.False(t, inProgress)
	})

	// A non-ENOENT stat error (here ENOTDIR: .git is a file, not a dir) is NOT
	// proof the repo is clean. RebaseAge must conservatively report in-progress
	// so the caller skips rather than pulling on a possibly-wedged repo, and
	// fresh (age 0) so it never auto-aborts on a guess.
	t.Run("non-not-exist stat error treated as in-progress fresh", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(repo, ".git"), []byte("not a dir"), 0644))

		age, inProgress := RebaseAge(repo)
		assert.True(t, inProgress)
		assert.Equal(t, time.Duration(0), age)
	})

	t.Run("fresh rebase reports small age", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "rebase-merge"), 0755))

		age, inProgress := RebaseAge(repo)
		assert.True(t, inProgress)
		// just-created dir → well under any sane stale threshold
		assert.Less(t, age, time.Minute)
	})

	t.Run("stale rebase reports large age via backdated mtime", func(t *testing.T) {
		repo := t.TempDir()
		dir := filepath.Join(repo, ".git", "rebase-merge")
		require.NoError(t, os.MkdirAll(dir, 0755))
		// backdate the dir mtime to simulate a wedge abandoned an hour ago
		old := time.Now().Add(-time.Hour)
		require.NoError(t, os.Chtimes(dir, old, old))

		age, inProgress := RebaseAge(repo)
		assert.True(t, inProgress)
		assert.Greater(t, age, 30*time.Minute)
	})

	// rebase-apply (git am-style / older interactive rebases) hits the same
	// loop; a typo skipping it would silently bypass stale recovery for that
	// backend, so pin its stale path independently.
	t.Run("stale rebase-apply backend reports large age", func(t *testing.T) {
		repo := t.TempDir()
		dir := filepath.Join(repo, ".git", "rebase-apply")
		require.NoError(t, os.MkdirAll(dir, 0755))
		old := time.Now().Add(-time.Hour)
		require.NoError(t, os.Chtimes(dir, old, old))

		age, inProgress := RebaseAge(repo)
		assert.True(t, inProgress)
		assert.Greater(t, age, 30*time.Minute)
	})

	// Blind-spot guard: a partial/failed `rebase --abort` can bump the dir's
	// own mtime to "now" while the rebase metadata files keep their original
	// (old) mtime. Age must follow the OLDEST entry so a genuinely stuck wedge
	// still reads as stale instead of looking fresh for another threshold.
	t.Run("fresh dir mtime but old entry still reads stale", func(t *testing.T) {
		repo := t.TempDir()
		dir := filepath.Join(repo, ".git", "rebase-merge")
		require.NoError(t, os.MkdirAll(dir, 0755))
		oldFile := filepath.Join(dir, "onto")
		require.NoError(t, os.WriteFile(oldFile, []byte("deadbeef\n"), 0644))
		old := time.Now().Add(-time.Hour)
		require.NoError(t, os.Chtimes(oldFile, old, old)) // metadata file is old
		now := time.Now()
		require.NoError(t, os.Chtimes(dir, now, now)) // dir mtime bumped to "now"

		age, inProgress := RebaseAge(repo)
		assert.True(t, inProgress)
		assert.Greater(t, age, 30*time.Minute, "age must follow the oldest entry, not the bumped dir mtime")
	})
}

func TestIsSafeForGitOps(t *testing.T) {
	t.Run("clean repo", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0755))

		assert.NoError(t, IsSafeForGitOps(repo))
	})

	t.Run("lock files present", func(t *testing.T) {
		repo := t.TempDir()
		gitDir := filepath.Join(repo, ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "index.lock"), []byte{}, 0644))

		err := IsSafeForGitOps(repo)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "index.lock")
	})

	t.Run("rebase in progress", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "rebase-merge"), 0755))

		err := IsSafeForGitOps(repo)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "rebase")
	})

	t.Run("both lock and rebase", func(t *testing.T) {
		repo := t.TempDir()
		gitDir := filepath.Join(repo, ".git")
		require.NoError(t, os.MkdirAll(filepath.Join(gitDir, "rebase-merge"), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "index.lock"), []byte{}, 0644))

		// lock check runs first, so error should mention locks
		err := IsSafeForGitOps(repo)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "lock")
	})
}

func TestStripLFSConfig(t *testing.T) {
	t.Run("removes lfs section", func(t *testing.T) {
		repo := t.TempDir()
		gitDir := filepath.Join(repo, ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))

		config := `[core]
	repositoryformatversion = 0
	bare = false
[lfs]
	repositoryformatversion = 0
[remote "origin"]
	url = https://example.com/repo.git
`
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "config"), []byte(config), 0644))

		StripLFSConfig(repo)

		data, err := os.ReadFile(filepath.Join(gitDir, "config"))
		require.NoError(t, err)
		assert.NotContains(t, string(data), "[lfs]")
		assert.NotContains(t, string(data), "lfs")
		assert.Contains(t, string(data), "[core]")
		assert.Contains(t, string(data), "[remote \"origin\"]")
	})

	t.Run("no-op without lfs section", func(t *testing.T) {
		repo := t.TempDir()
		gitDir := filepath.Join(repo, ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))

		config := `[core]
	repositoryformatversion = 0
[remote "origin"]
	url = https://example.com/repo.git
`
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "config"), []byte(config), 0644))

		StripLFSConfig(repo)

		data, err := os.ReadFile(filepath.Join(gitDir, "config"))
		require.NoError(t, err)
		assert.Equal(t, config, string(data))
	})

	t.Run("no-op without .git dir", func(t *testing.T) {
		repo := t.TempDir()
		StripLFSConfig(repo) // should not panic
	})
}

func TestFetchHeadAge(t *testing.T) {
	t.Run("no FETCH_HEAD", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0755))

		age, ok := FetchHeadAge(repo)
		assert.False(t, ok)
		assert.Zero(t, age)
	})

	t.Run("recent FETCH_HEAD", func(t *testing.T) {
		repo := t.TempDir()
		gitDir := filepath.Join(repo, ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "FETCH_HEAD"), []byte("ref"), 0644))

		age, ok := FetchHeadAge(repo)
		assert.True(t, ok)
		assert.Less(t, age, 2*time.Second) // just created
	})

	t.Run("old FETCH_HEAD", func(t *testing.T) {
		repo := t.TempDir()
		gitDir := filepath.Join(repo, ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))

		fetchHead := filepath.Join(gitDir, "FETCH_HEAD")
		require.NoError(t, os.WriteFile(fetchHead, []byte("ref"), 0644))
		oldTime := time.Now().Add(-5 * time.Minute)
		require.NoError(t, os.Chtimes(fetchHead, oldTime, oldTime))

		age, ok := FetchHeadAge(repo)
		assert.True(t, ok)
		assert.Greater(t, age, 4*time.Minute)
	})

	// git truncates FETCH_HEAD before contacting the remote, so a FAILED fetch
	// leaves a zero-byte file with a fresh mtime. Counting that as a recent
	// fetch makes the failure suppress its own retry.
	t.Run("empty FETCH_HEAD is not a fetch", func(t *testing.T) {
		repo := t.TempDir()
		gitDir := filepath.Join(repo, ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "FETCH_HEAD"), nil, 0644))

		age, ok := FetchHeadAge(repo)
		assert.False(t, ok, "a zero-byte FETCH_HEAD means the fetch failed, not that one just succeeded")
		assert.Zero(t, age)
	})
}

// TestFetchHeadAge_FailedFetchDoesNotSuppressRetry drives real git rather than
// hand-written fixtures, because the bug lives in git's own behavior: the
// truncate-then-contact-remote ordering is what leaves a fresh, empty
// FETCH_HEAD behind a failure.
//
// Failure prevented: after a failed fetch, callers that gate on FetchHeadAge
// (daemon team-context/managed-repo pulls, ledger sync) skipped the next
// attempt as "recently fetched" — and pullTeamContext reports a skip as
// success, so the failure counter was cleared and last_sync recorded for a
// sync that never happened.
func TestFetchHeadAge_FailedFetchDoesNotSuppressRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git fetch")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run(root, "init", "-q", "--bare", "--initial-branch=main", origin)
	src := filepath.Join(root, "src")
	require.NoError(t, os.MkdirAll(src, 0o755))
	run(src, "init", "-q", "--initial-branch=main")
	run(src, "config", "user.name", "Test")
	run(src, "config", "user.email", "fetchhead-test@test.sageox.ai")
	run(src, "config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(src, "a.txt"), []byte("a\n"), 0o600))
	run(src, "add", "a.txt")
	run(src, "commit", "-qm", "one")
	run(src, "remote", "add", "origin", origin)
	run(src, "push", "-q", "origin", "HEAD:refs/heads/main")
	run(root, "clone", "-q", origin, work)

	// A SUCCESSFUL fetch — even with nothing new — must register.
	run(work, "fetch", "-q", "origin")
	age, ok := FetchHeadAge(work)
	require.True(t, ok, "a successful fetch must register as a recent fetch")
	assert.Less(t, age, time.Minute)

	// Now fail a fetch. RFC 2606 reserved TLD: never resolves, no network.
	run(work, "remote", "set-url", "origin", "https://nonexistent-host-for-gitutil-tests.invalid/x.git")
	failed := exec.Command("git", "fetch", "origin")
	failed.Dir = work
	require.Error(t, failed.Run(), "fixture must reproduce a failed fetch")

	info, err := os.Stat(filepath.Join(work, ".git", "FETCH_HEAD"))
	require.NoError(t, err, "git leaves FETCH_HEAD behind after a failed fetch")
	require.Zero(t, info.Size(), "and leaves it empty — the whole basis of the bug")
	require.WithinDuration(t, time.Now(), info.ModTime(), time.Minute,
		"with a FRESH mtime, which is why the age check was fooled")

	_, ok = FetchHeadAge(work)
	assert.False(t, ok,
		"a failed fetch must not count as a recent fetch, or it suppresses its own retry")
}

// TestLockSweep_PidSuffixedAndSelfHealing covers the wedge that kept a real
// ledger blocked for three months: a next-index-<pid>.lock left by a crashed
// git. Its pid varies, so it can never be listed in knownLockFiles, and the
// push pre-flight only ever *reported* locks — it never cleared them, unlike
// the pull path. Detection and removal must agree, and IsSafeForGitOps must
// self-heal an abandoned lock rather than blocking forever.
func TestLockSweep_PidSuffixedAndSelfHealing(t *testing.T) {
	stale := time.Now().Add(-2 * AbandonedLockAge)

	newGitDir := func(t *testing.T) (repo, gitDir string) {
		t.Helper()
		repo = t.TempDir()
		gitDir = filepath.Join(repo, ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))
		return repo, gitDir
	}

	writeLock := func(t *testing.T, gitDir, name string, mtime time.Time) string {
		t.Helper()
		p := filepath.Join(gitDir, name)
		require.NoError(t, os.WriteFile(p, []byte("lock"), 0644))
		require.NoError(t, os.Chtimes(p, mtime, mtime))
		return p
	}

	t.Run("pid-suffixed next-index lock is detected", func(t *testing.T) {
		_, gitDir := newGitDir(t)
		writeLock(t, gitDir, "next-index-13088.lock", stale)

		assert.Contains(t, HasLockFiles(gitDir), "next-index-13088.lock",
			"a lock we cannot detect is a lock we can never clear")
	})

	t.Run("pid-suffixed next-index lock is removable when stale", func(t *testing.T) {
		_, gitDir := newGitDir(t)
		p := writeLock(t, gitDir, "next-index-13088.lock", stale)

		removed, errs := RemoveStaleLockFiles(gitDir)
		assert.Empty(t, errs)
		assert.Contains(t, removed, "next-index-13088.lock")
		assert.NoFileExists(t, p)
	})

	t.Run("fresh locks are never swept", func(t *testing.T) {
		_, gitDir := newGitDir(t)
		fresh := writeLock(t, gitDir, "next-index-999.lock", time.Now())
		freshIndex := writeLock(t, gitDir, "index.lock", time.Now())

		removed, errs := RemoveStaleLockFiles(gitDir)
		assert.Empty(t, errs)
		assert.Empty(t, removed, "a live git process must not have its lock yanked")
		assert.FileExists(t, fresh)
		assert.FileExists(t, freshIndex)
	})

	t.Run("ref locks are left alone", func(t *testing.T) {
		_, gitDir := newGitDir(t)
		// packed-refs.lock belongs to a ref transaction, not the index. A broad
		// "*.lock" sweep would eat it; we must not.
		packed := writeLock(t, gitDir, "packed-refs.lock", stale)

		removed, _ := RemoveStaleLockFiles(gitDir)
		assert.NotContains(t, removed, "packed-refs.lock")
		assert.FileExists(t, packed)
	})

	t.Run("IsSafeForGitOps self-heals an abandoned lock", func(t *testing.T) {
		repo, gitDir := newGitDir(t)
		p := writeLock(t, gitDir, "index.lock", stale)

		assert.NoError(t, IsSafeForGitOps(repo),
			"an abandoned lock must not block pushes forever")
		assert.NoFileExists(t, p)
	})

	t.Run("IsSafeForGitOps still blocks on a live lock", func(t *testing.T) {
		repo, gitDir := newGitDir(t)
		writeLock(t, gitDir, "index.lock", time.Now())

		assert.Error(t, IsSafeForGitOps(repo),
			"a lock a running git may still hold must block")
	})
}

// TestLockSweep_OwnerLivenessBeatsAge covers the case age alone gets wrong:
// a lock whose owning process is STILL RUNNING must survive the sweep no matter
// how old it is. Deleting it would let a second writer into the index mid-write
// and lose uncommitted work — the one outcome worse than a blocked push.
//
// Only next-index-<pid>.lock encodes an owner. git's index.lock IS the lock (its
// content is the partially-written index), so no owner exists to interrogate and
// age remains the only available signal there.
func TestLockSweep_OwnerLivenessBeatsAge(t *testing.T) {
	ancient := time.Now().Add(-100 * AbandonedLockAge)

	writeAged := func(t *testing.T, gitDir, name string) string {
		t.Helper()
		require.NoError(t, os.MkdirAll(gitDir, 0755))
		p := filepath.Join(gitDir, name)
		require.NoError(t, os.WriteFile(p, []byte("lock"), 0644))
		require.NoError(t, os.Chtimes(p, ancient, ancient))
		return p
	}

	t.Run("live owner retains the lock despite extreme age", func(t *testing.T) {
		gitDir := filepath.Join(t.TempDir(), ".git")
		// Our own PID is guaranteed alive for the duration of this test.
		p := writeAged(t, gitDir, fmt.Sprintf("next-index-%d.lock", os.Getpid()))

		removed, errs := RemoveStaleLockFiles(gitDir)
		assert.Empty(t, errs)
		assert.Empty(t, removed, "a lock held by a LIVE process must never be removed")
		assert.FileExists(t, p, "removing it could corrupt the index mid-write")

		// And the repo must stay blocked rather than silently proceeding.
		assert.Error(t, IsSafeForGitOps(filepath.Dir(gitDir)),
			"an actively-held lock must keep blocking git operations")
	})

	t.Run("dead owner allows removal", func(t *testing.T) {
		gitDir := filepath.Join(t.TempDir(), ".git")
		// PID 0x7FFFFFFF is not a live process on any realistic system.
		p := writeAged(t, gitDir, "next-index-2147483647.lock")

		removed, errs := RemoveStaleLockFiles(gitDir)
		assert.Empty(t, errs)
		assert.Contains(t, removed, "next-index-2147483647.lock",
			"a lock whose owner is verifiably gone is abandoned")
		assert.NoFileExists(t, p)
	})

	t.Run("ownerless lock still falls back to age", func(t *testing.T) {
		// index.lock carries no PID, so age is the only signal git's format offers.
		gitDir := filepath.Join(t.TempDir(), ".git")
		p := writeAged(t, gitDir, "index.lock")

		removed, _ := RemoveStaleLockFiles(gitDir)
		assert.Contains(t, removed, "index.lock")
		assert.NoFileExists(t, p)
	})

	t.Run("ownerless lock younger than StaleLockAge is retained", func(t *testing.T) {
		gitDir := filepath.Join(t.TempDir(), ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))
		p := filepath.Join(gitDir, "index.lock")
		require.NoError(t, os.WriteFile(p, []byte("lock"), 0644))

		removed, _ := RemoveStaleLockFiles(gitDir)
		assert.Empty(t, removed, "a lock a running git may still hold must survive")
		assert.FileExists(t, p)
	})
}

func TestLockOwnerPID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		lock    string
		wantPID int
		wantOK  bool
	}{
		{"next-index with pid", "next-index-13088.lock", 13088, true},
		{"index.lock has no owner", "index.lock", 0, false},
		{"shallow.lock has no owner", "shallow.lock", 0, false},
		{"non-numeric pid", "next-index-abc.lock", 0, false},
		{"empty pid", "next-index-.lock", 0, false},
		{"zero pid rejected", "next-index-0.lock", 0, false},
		{"negative pid rejected", "next-index--5.lock", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pid, ok := lockOwnerPID(tt.lock)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantPID, pid)
		})
	}
}

func TestProcessAlive(t *testing.T) {
	assert.True(t, processAlive(os.Getpid()), "our own process is alive")
	assert.False(t, processAlive(2147483647), "an implausible PID is not alive")
}

// TestLockSweep_OwnerlessLockSurvivesTheStaleWindow is the direct regression for
// the review finding that age alone can displace a live writer.
//
// Failure prevented: a legitimate `git pull --rebase` on a large repo over a bad
// network holds index.lock for more than StaleLockAge; a push preflight that
// swept at that threshold would delete an ACTIVE lock, admit a second writer,
// and risk index corruption or lost uncommitted changes. index.lock carries no
// PID (it IS the lock), so no owner probe is possible and the only safe move is
// a threshold no real index operation reaches.
func TestLockSweep_OwnerlessLockSurvivesTheStaleWindow(t *testing.T) {
	for _, lock := range []string{"index.lock", "shallow.lock", "config.lock", "HEAD.lock"} {
		t.Run(lock+" held past StaleLockAge is retained", func(t *testing.T) {
			repo := t.TempDir()
			gitDir := filepath.Join(repo, ".git")
			require.NoError(t, os.MkdirAll(gitDir, 0755))
			p := filepath.Join(gitDir, lock)
			require.NoError(t, os.WriteFile(p, []byte("lock"), 0644))

			// Comfortably past the old 5-minute bar, far short of abandonment.
			held := time.Now().Add(-(StaleLockAge * 3))
			require.NoError(t, os.Chtimes(p, held, held))

			removed, errs := RemoveStaleLockFiles(gitDir)
			assert.Empty(t, errs)
			assert.Empty(t, removed,
				"a slow but legitimate git operation must not have its lock deleted")
			assert.FileExists(t, p)
			assert.Error(t, IsSafeForGitOps(repo),
				"the repo must stay blocked while a writer may still hold the index")
		})
	}
}
