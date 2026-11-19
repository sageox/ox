package paths

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Failure prevented: the daemon opens a database under a checkout's
// .sageox/cache through a symlink a teammate pushed, writing outside it.
func TestCheckNoSymlinksBelow(t *testing.T) {
	outside := t.TempDir()
	for _, tc := range []struct {
		name    string
		build   func(t *testing.T, root string)
		refused bool
	}{
		{"real directories", func(t *testing.T, root string) {
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox", "cache", "whisper"), 0o755))
		}, false},
		{"not created yet", func(*testing.T, string) {}, false},
		{"symlinked directory on the way", func(t *testing.T, root string) {
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox"), 0o755))
			require.NoError(t, os.Symlink(outside, filepath.Join(root, ".sageox", "cache")))
		}, true},
		{"symlinked target", func(t *testing.T, root string) {
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox", "cache"), 0o755))
			require.NoError(t, os.Symlink(outside, filepath.Join(root, ".sageox", "cache", "whisper")))
		}, true},
		{"a link checked out as a plain file", func(t *testing.T, root string) {
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox", "cache"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "cache", "whisper"), []byte(outside), 0o644))
		}, true},
		{"symlinked file inside the target", func(t *testing.T, root string) {
			dir := filepath.Join(root, ".sageox", "cache", "whisper")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.Symlink(filepath.Join(outside, "whisper.db"), filepath.Join(dir, "whisper.db-wal")))
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "ledger")
			require.NoError(t, os.MkdirAll(root, 0o755))
			tc.build(t, root)
			err := CheckNoSymlinksBelow(root, filepath.Join(root, ".sageox", "cache", "whisper"))
			if tc.refused {
				require.ErrorIs(t, err, ErrSymlinkInCache)
			} else {
				require.NoError(t, err)
			}
		})
	}

	// a relocated checkout (root itself a symlink) is a teammate's choice
	real := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(real, ".sageox", "cache", "whisper"), 0o755))
	linked := filepath.Join(t.TempDir(), "ledger")
	require.NoError(t, os.Symlink(real, linked))
	require.NoError(t, CheckNoSymlinksBelow(linked, filepath.Join(linked, ".sageox", "cache", "whisper")))
	require.Error(t, CheckNoSymlinksBelow(linked, filepath.Join(outside, "x")), "a target outside root is refused")
}
