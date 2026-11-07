package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/gitserver"
)

// Failure prevented: Checkout moved a symlinked clone target aside (or let the
// clone rename over it), undoing a coworker's relocation and orphaning the
// directory behind the link. The link and its target must be untouched and the
// refusal must be visible to `ox doctor`.
func TestSyncScheduler_Checkout_RefusesSymlinkedTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone fixtures")
	}
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	for _, repoType := range []string{"ledger", "team-context"} {
		{
			// dangling links never get this far: isValidRepoPath rejects them
			t.Run(repoType, func(t *testing.T) {
				s := checkoutTestScheduler(t)
				cloneURL := setupBareLedgerHTTP(t)
				if repoType == "team-context" {
					cloneURL = serveTeamContext(t)
				}
				root := t.TempDir()
				real := filepath.Join(root, "relocated")
				writeFile(t, filepath.Join(real, ".sageox", "cache", "whisper", "whisper.db"), "db")
				repoPath := filepath.Join(root, "target")
				require.NoError(t, os.Symlink(real, repoPath))

				result, err := s.Checkout(CheckoutPayload{CloneURL: cloneURL, RepoPath: repoPath, RepoType: repoType}, nil)

				require.Error(t, err)
				assert.Nil(t, result)
				assert.True(t, errors.Is(err, gitserver.ErrTargetIsSymlink), "got %v", err)
				fi, lerr := os.Lstat(repoPath)
				require.NoError(t, lerr, "the symlink must still exist")
				assert.NotZero(t, fi.Mode()&os.ModeSymlink, "the target must still be a symlink")
				dest, rerr := os.Readlink(repoPath)
				require.NoError(t, rerr)
				assert.Equal(t, real, dest)
				assert.FileExists(t, filepath.Join(real, ".sageox", "cache", "whisper", "whisper.db"))
				assert.NoDirExists(t, filepath.Join(real, ".git"))
				backups, _ := filepath.Glob(repoPath + ".bak.*")
				assert.Empty(t, backups, "the symlink must not be moved aside")
				assert.Empty(t, tmpCloneSiblings(t, repoPath))

				s.mu.Lock()
				defer s.mu.Unlock()
				require.Len(t, s.recentErrors, 1, "doctor must see the refusal")
				assert.Equal(t, "target", s.recentErrors[0].Repo)
			})
		}
	}
}
