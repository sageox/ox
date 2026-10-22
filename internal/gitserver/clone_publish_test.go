package gitserver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A failed swap deletes the temp clone; the adopted CodeDB index must be handed
// back rather than die with it, and a symlinked .sageox in the clone must never
// receive local data.
func TestAdoptCacheOnlyTarget_RestoreAndSymlinkGuard(t *testing.T) {
	mk := func(t *testing.T) (tmp, target string) {
		root := t.TempDir()
		target = filepath.Join(root, "ledger")
		tmp = filepath.Join(root, "ledger.tmp-1")
		require.NoError(t, os.MkdirAll(filepath.Join(target, ".sageox", "cache", "codedb"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(target, ".sageox", "cache", "codedb", "index.db"), []byte("i"), 0o644))
		require.NoError(t, os.MkdirAll(tmp, 0o755))
		return tmp, target
	}

	t.Run("restore puts the cache back", func(t *testing.T) {
		tmp, target := mk(t)
		restore, err := AdoptCacheOnlyTarget(tmp, target)
		require.NoError(t, err)
		_, err = os.Stat(target)
		require.True(t, os.IsNotExist(err), "skeleton removed")
		restore()
		_, err = os.Stat(filepath.Join(target, ".sageox", "cache", "codedb", "index.db"))
		assert.NoError(t, err)
	})

	t.Run("symlinked .sageox in the clone is refused", func(t *testing.T) {
		tmp, target := mk(t)
		outside := t.TempDir()
		require.NoError(t, os.Symlink(outside, filepath.Join(tmp, ".sageox")))
		_, err := AdoptCacheOnlyTarget(tmp, target)
		require.Error(t, err)
		entries, _ := os.ReadDir(outside)
		assert.Empty(t, entries, "nothing moved through the symlink")
		_, err = os.Stat(filepath.Join(target, ".sageox", "cache", "codedb", "index.db"))
		assert.NoError(t, err, "cache untouched")
	})
}

func TestAdoptCacheOnlyTarget_Branches(t *testing.T) {
	write := func(t *testing.T, path string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
	}

	t.Run("clone already ships a cache: the old derived cache is dropped", func(t *testing.T) {
		root := t.TempDir()
		target, tmp := filepath.Join(root, "ledger"), filepath.Join(root, "ledger.tmp-1")
		write(t, filepath.Join(target, ".sageox", "cache", "codedb", "index.db"))
		write(t, filepath.Join(tmp, ".sageox", "cache", "shipped.txt"))
		_, err := AdoptCacheOnlyTarget(tmp, target)
		require.NoError(t, err)
		_, err = os.Stat(target)
		assert.True(t, os.IsNotExist(err), "skeleton removed so the swap can land")
		_, err = os.Stat(filepath.Join(tmp, ".sageox", "cache", "shipped.txt"))
		assert.NoError(t, err, "the clone's own cache is untouched")
		_, err = os.Stat(filepath.Join(tmp, ".sageox", "cache", "codedb"))
		assert.True(t, os.IsNotExist(err))
	})

	t.Run("empty .sageox skeleton is removed", func(t *testing.T) {
		root := t.TempDir()
		target, tmp := filepath.Join(root, "ledger"), filepath.Join(root, "ledger.tmp-1")
		require.NoError(t, os.MkdirAll(filepath.Join(target, ".sageox"), 0o755))
		require.NoError(t, os.MkdirAll(tmp, 0o755))
		_, err := AdoptCacheOnlyTarget(tmp, target)
		require.NoError(t, err)
		_, err = os.Stat(target)
		assert.True(t, os.IsNotExist(err))
	})

	t.Run("missing target or foreign content is a no-op", func(t *testing.T) {
		root := t.TempDir()
		tmp := filepath.Join(root, "ledger.tmp-1")
		require.NoError(t, os.MkdirAll(tmp, 0o755))
		_, err := AdoptCacheOnlyTarget(tmp, filepath.Join(root, "absent"))
		require.NoError(t, err)

		target := filepath.Join(root, "ledger")
		write(t, filepath.Join(target, ".sageox", "cache", "codedb", "index.db"))
		write(t, filepath.Join(target, "notes.txt"))
		_, err = AdoptCacheOnlyTarget(tmp, target)
		require.NoError(t, err)
		_, err = os.Stat(filepath.Join(target, ".sageox", "cache", "codedb", "index.db"))
		assert.NoError(t, err, "foreign content: cache left exactly where it was")

		other := filepath.Join(root, "other")
		write(t, filepath.Join(other, ".sageox", "config.json"))
		_, err = AdoptCacheOnlyTarget(tmp, other)
		require.NoError(t, err)
		_, err = os.Stat(filepath.Join(other, ".sageox", "config.json"))
		assert.NoError(t, err)
	})

	t.Run("skeleton removal fails: cache is moved back, nothing lost", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("needs POSIX permission enforcement (chmod is a no-op on Windows, root ignores it)")
		}
		root := t.TempDir()
		target, tmp := filepath.Join(root, "ledger"), filepath.Join(root, "ledger.tmp-1")
		write(t, filepath.Join(target, ".sageox", "cache", "codedb", "index.db"))
		require.NoError(t, os.MkdirAll(tmp, 0o755))
		// target not writable: .sageox cannot be unlinked from it
		require.NoError(t, os.Chmod(target, 0o500))
		t.Cleanup(func() { _ = os.Chmod(target, 0o755) })

		_, err := AdoptCacheOnlyTarget(tmp, target)
		require.Error(t, err)
		// confirm the gate: the failure is the skeleton removal, i.e. after the
		// cache was already moved into the clone, so restoration really ran
		var pathErr *os.PathError
		require.ErrorAs(t, err, &pathErr)
		assert.Equal(t, "remove", pathErr.Op)
		assert.Equal(t, filepath.Join(target, ".sageox"), pathErr.Path)
		require.NoError(t, os.Chmod(target, 0o755))
		_, statErr := os.Stat(filepath.Join(target, ".sageox", "cache", "codedb", "index.db"))
		assert.NoError(t, statErr, "the index must survive a failed adoption")
	})
}

// TestVerifyClone_OnlyACompleteCloneIsPublished verifies a clone is only
// called complete when it has a .git and a HEAD that resolves.
// Failure prevented: a clone that exited zero with an unborn HEAD (an empty
// remote, an early EOF) is renamed into place as if it were a Ledger.
func TestVerifyClone_OnlyACompleteCloneIsPublished(t *testing.T) {
	git := func(t *testing.T, dir string, args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	t.Run("no .git", func(t *testing.T) {
		require.ErrorContains(t, VerifyClone(context.Background(), t.TempDir()), "no .git directory")
	})
	t.Run("HEAD does not resolve", func(t *testing.T) {
		dir := t.TempDir()
		git(t, dir, "init", "-q")
		require.ErrorContains(t, VerifyClone(context.Background(), dir), "HEAD does not resolve")
	})
	t.Run("complete clone", func(t *testing.T) {
		dir := t.TempDir()
		git(t, dir, "init", "-q")
		git(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "seed")
		require.NoError(t, VerifyClone(context.Background(), dir))
	})
}

// TestMoveCloneIntoPlace_TargetStates verifies the rename that publishes a
// clone, for each state its target can be in.
// Failure prevented: a clone merged into, or silently lost over, a target
// someone else filled; CodeDB's cache destroyed by a failed publish.
func TestMoveCloneIntoPlace_TargetStates(t *testing.T) {
	cacheFile := func(target string) string {
		return filepath.Join(target, ".sageox", "cache", "codedb", "index.db")
	}
	mk := func(t *testing.T) (tmp, target string) {
		root := t.TempDir()
		tmp, target = filepath.Join(root, "ledger.tmp-1"), filepath.Join(root, "ledger")
		require.NoError(t, os.MkdirAll(filepath.Join(tmp, ".git"), 0o755))
		return tmp, target
	}

	t.Run("absent target: the clone lands", func(t *testing.T) {
		tmp, target := mk(t)
		require.NoError(t, MoveCloneIntoPlace(tmp, target))
		assert.DirExists(t, filepath.Join(target, ".git"))
		assert.NoDirExists(t, tmp)
	})

	t.Run("target holds someone's files: refused, both sides intact", func(t *testing.T) {
		tmp, target := mk(t)
		require.NoError(t, os.MkdirAll(target, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(target, "notes.txt"), []byte("mine"), 0o600))
		require.ErrorContains(t, MoveCloneIntoPlace(tmp, target), "failed to move completed clone into place")
		assert.FileExists(t, filepath.Join(target, "notes.txt"))
		assert.DirExists(t, filepath.Join(tmp, ".git"))
	})

	t.Run("cache-only target: the clone lands carrying the cache", func(t *testing.T) {
		tmp, target := mk(t)
		require.NoError(t, os.MkdirAll(filepath.Dir(cacheFile(target)), 0o755))
		require.NoError(t, os.WriteFile(cacheFile(target), []byte("idx"), 0o600))
		require.NoError(t, MoveCloneIntoPlace(tmp, target))
		assert.DirExists(t, filepath.Join(target, ".git"))
		assert.FileExists(t, cacheFile(target))
	})

	t.Run("cache cannot be adopted: refused with both reasons, cache left in place", func(t *testing.T) {
		tmp, target := mk(t)
		require.NoError(t, os.MkdirAll(filepath.Dir(cacheFile(target)), 0o755))
		require.NoError(t, os.WriteFile(cacheFile(target), []byte("idx"), 0o600))
		// the clone ships .sageox as a file, so the cache has nowhere to go
		require.NoError(t, os.WriteFile(filepath.Join(tmp, ".sageox"), []byte("x"), 0o600))
		err := MoveCloneIntoPlace(tmp, target)
		require.ErrorContains(t, err, "failed to move completed clone into place")
		require.ErrorContains(t, err, "clone's .sageox is not a directory")
		assert.FileExists(t, cacheFile(target))
	})
}

func TestIsCacheOnlyDir(t *testing.T) {
	for _, tt := range []struct {
		name  string
		files []string // created under the dir; a trailing / makes a directory
		want  bool
	}{
		{name: "empty .sageox", files: []string{".sageox/"}, want: true},
		{name: "cache dir", files: []string{".sageox/cache/codedb/"}, want: true},
		{name: "nothing at all", files: nil},
		{name: "cache plus an empty sessions dir", files: []string{".sageox/cache/whisper/", "sessions/"}, want: true},
		{name: "cache plus nested empty dirs", files: []string{".sageox/cache/", "sessions/a/b/"}, want: true},
		{name: "a sessions dir holding a file", files: []string{".sageox/cache/", "sessions/draft/meta.json"}},
		{name: "a second top-level entry", files: []string{".sageox/cache/", "notes.txt"}},
		{name: "a second .sageox entry", files: []string{".sageox/cache/", ".sageox/config.json"}},
		{name: "cache is a file", files: []string{".sageox/cache"}},
		{name: ".sageox is a file", files: []string{".sageox"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tt.files {
				p := filepath.Join(dir, f)
				if f[len(f)-1] == '/' {
					require.NoError(t, os.MkdirAll(p, 0o755))
					continue
				}
				require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
				require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
			}
			assert.Equal(t, tt.want, IsCacheOnlyDir(dir))
		})
	}
}

// TestAdoptCacheOnlyTarget_FailuresKeepTheCache covers adoption failing at
// each later step: the cache must end up where it started.
func TestAdoptCacheOnlyTarget_FailuresKeepTheCache(t *testing.T) {
	cacheOnly := func(t *testing.T) (root, target string) {
		root = t.TempDir()
		target = filepath.Join(root, "ledger")
		require.NoError(t, os.MkdirAll(filepath.Join(target, ".sageox", "cache", "codedb"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(target, ".sageox", "cache", "codedb", "index.db"), []byte("i"), 0o600))
		return root, target
	}

	t.Run("clone dir is a file: nothing moved", func(t *testing.T) {
		root, target := cacheOnly(t)
		tmp := filepath.Join(root, "ledger.tmp-1")
		require.NoError(t, os.WriteFile(tmp, []byte("x"), 0o600)) // MkdirAll under it fails (ENOTDIR)
		_, err := AdoptCacheOnlyTarget(tmp, target)
		require.Error(t, err)
		assert.FileExists(t, filepath.Join(target, ".sageox", "cache", "codedb", "index.db"))
	})

	t.Run("restore twice is harmless", func(t *testing.T) {
		root, target := cacheOnly(t)
		tmp := filepath.Join(root, "ledger.tmp-1")
		require.NoError(t, os.MkdirAll(tmp, 0o755))
		restore, err := AdoptCacheOnlyTarget(tmp, target)
		require.NoError(t, err)
		restore()
		restore()
		assert.FileExists(t, filepath.Join(target, ".sageox", "cache", "codedb", "index.db"))
	})

	t.Run("target itself cannot be removed: cache moved back", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("needs POSIX permission enforcement (chmod is a no-op on Windows, root ignores it)")
		}
		root, target := cacheOnly(t)
		tmp := filepath.Join(t.TempDir(), "ledger.tmp-1")
		require.NoError(t, os.MkdirAll(tmp, 0o755))
		// root not writable: the emptied target cannot be unlinked from it
		require.NoError(t, os.Chmod(root, 0o500))
		t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

		_, err := AdoptCacheOnlyTarget(tmp, target)
		var pathErr *os.PathError
		require.ErrorAs(t, err, &pathErr)
		assert.Equal(t, target, pathErr.Path, "the failure is the target's removal, after the cache moved")
		assert.FileExists(t, filepath.Join(target, ".sageox", "cache", "codedb", "index.db"))
	})
}

// Failure prevented: a target that is a symlink to a coworker's relocated
// (cache-only) folder was adopted, which removed the link; the clone then
// created a real directory and silently undid the relocation.
func TestIsCacheOnlyDir_SymlinkedTargetIsNotASkeleton(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	require.NoError(t, os.MkdirAll(filepath.Join(real, ".sageox", "cache"), 0o755))
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	assert.True(t, IsCacheOnlyDir(real), "negative control: the real folder is a skeleton")
	assert.False(t, IsCacheOnlyDir(link))
}

// Failure prevented: a skeleton with an empty sessions/ folder next to the
// cache made the publish rename fail "file exists" on first init.
func TestMoveCloneIntoPlace_AdoptsCacheAndRemovesEmptyFolders(t *testing.T) {
	root := t.TempDir()
	tmp, target := filepath.Join(root, ".t.tmp-1"), filepath.Join(root, "t")
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(target, ".sageox", "cache", "whisper"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, ".sageox", "cache", "whisper", "whisper.db"), []byte("db"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(target, "sessions", "nested"), 0o755))

	require.NoError(t, MoveCloneIntoPlace(tmp, target))

	assert.DirExists(t, filepath.Join(target, ".git"))
	assert.FileExists(t, filepath.Join(target, ".sageox", "cache", "whisper", "whisper.db"))
	assert.NoDirExists(t, filepath.Join(target, "sessions"))
}
