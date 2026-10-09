package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CodeDB creates its shared index at <ledger>/.sageox/cache/codedb as soon as
// it opens, which can be before the Ledger's first clone lands. The atomic
// swap then failed "file exists" on every retry while `ox sync` kept going.
// These tests drive the real Checkout against a real git server.
//
// Failure prevented: a brand-new Ledger never clones because a cache-only
// directory squats on its path.
func TestSyncScheduler_Checkout_LedgerCloneLandsOverCodeDBCache(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}

	cacheOnly := func(t *testing.T, repoPath string) {
		require.NoError(t, os.MkdirAll(filepath.Join(repoPath, ".sageox", "cache", "codedb"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(repoPath, ".sageox", "cache", "codedb", "index.db"), []byte("idx"), 0o644))
	}

	tests := []struct {
		name      string
		prepare   func(t *testing.T, repoPath string)
		before    bool // create the cache before Checkout starts, not mid-clone
		wantErr   bool
		wantCache bool // CodeDB index survives into the clone
	}{
		{
			name:      "cache appears while the clone runs (CodeDB opens mid-clone)",
			prepare:   cacheOnly,
			wantCache: true,
		},
		{
			name:      "cache exists before Checkout starts (not moved aside as corrupt)",
			prepare:   cacheOnly,
			before:    true,
			wantCache: true,
		},
		{
			name: "cache is a file, not a directory: swap still fails",
			prepare: func(t *testing.T, repoPath string) {
				require.NoError(t, os.MkdirAll(filepath.Join(repoPath, ".sageox"), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(repoPath, ".sageox", "cache"), []byte("x"), 0o644))
			},
			wantErr: true,
		},
		{
			name: "cache dir with no codedb content yet",
			prepare: func(t *testing.T, repoPath string) {
				require.NoError(t, os.MkdirAll(filepath.Join(repoPath, ".sageox", "cache"), 0o755))
			},
		},
		{
			name: "target holds unrelated user files: swap still fails loudly",
			prepare: func(t *testing.T, repoPath string) {
				cacheOnly(t, repoPath)
				require.NoError(t, os.WriteFile(filepath.Join(repoPath, "notes.txt"), []byte("mine"), 0o644))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloneURL := setupBareLedgerHTTP(t)
			s := checkoutTestScheduler(t)
			repoPath := filepath.Join(t.TempDir(), "ledger")

			// fires after Checkout's early move-aside logic, i.e. while the
			// clone is in flight — the window CodeDB hit in the field.
			if tt.before {
				tt.prepare(t, repoPath)
			} else {
				s.onBeforeCloneSem = func() { tt.prepare(t, repoPath) }
			}

			result, err := s.Checkout(CheckoutPayload{CloneURL: cloneURL, RepoPath: repoPath, RepoType: "ledger"}, nil)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.True(t, result.Cloned)
			_, statErr := os.Stat(filepath.Join(repoPath, ".git"))
			require.NoError(t, statErr, "the Ledger clone must land")
			_, statErr = os.Stat(filepath.Join(repoPath, "seed.txt"))
			assert.NoError(t, statErr)
			_, statErr = os.Stat(filepath.Join(repoPath, ".sageox", "cache", "codedb", "index.db"))
			assert.Equal(t, tt.wantCache, statErr == nil, "CodeDB index carry-over")
			assert.Empty(t, tmpCloneSiblings(t, repoPath))
			backups, _ := filepath.Glob(repoPath + ".bak.*")
			assert.Empty(t, backups, "a cache-only target must not be moved aside")
		})
	}
}

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
		restore, err := adoptCacheOnlyTarget(tmp, target)
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
		_, err := adoptCacheOnlyTarget(tmp, target)
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
		_, err := adoptCacheOnlyTarget(tmp, target)
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
		_, err := adoptCacheOnlyTarget(tmp, target)
		require.NoError(t, err)
		_, err = os.Stat(target)
		assert.True(t, os.IsNotExist(err))
	})

	t.Run("missing target or foreign content is a no-op", func(t *testing.T) {
		root := t.TempDir()
		tmp := filepath.Join(root, "ledger.tmp-1")
		require.NoError(t, os.MkdirAll(tmp, 0o755))
		_, err := adoptCacheOnlyTarget(tmp, filepath.Join(root, "absent"))
		require.NoError(t, err)

		target := filepath.Join(root, "ledger")
		write(t, filepath.Join(target, ".sageox", "cache", "codedb", "index.db"))
		write(t, filepath.Join(target, "notes.txt"))
		_, err = adoptCacheOnlyTarget(tmp, target)
		require.NoError(t, err)
		_, err = os.Stat(filepath.Join(target, ".sageox", "cache", "codedb", "index.db"))
		assert.NoError(t, err, "foreign content: cache left exactly where it was")

		other := filepath.Join(root, "other")
		write(t, filepath.Join(other, ".sageox", "config.json"))
		_, err = adoptCacheOnlyTarget(tmp, other)
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

		_, err := adoptCacheOnlyTarget(tmp, target)
		require.Error(t, err)
		require.NoError(t, os.Chmod(target, 0o755))
		_, statErr := os.Stat(filepath.Join(target, ".sageox", "cache", "codedb", "index.db"))
		assert.NoError(t, statErr, "the index must survive a failed adoption")
	})
}
