package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Local-only skeletons (whisper/CodeDB caches, an empty sessions/ folder) get
// created at a clone target by other ox processes before, or while, the first
// clone runs. They hold nothing that is not rebuildable, so they must never
// stop the first clone from landing.
//
// Failure prevented: on first `ox init` the Ledger and team context never
// cloned ("rename ... file exists", "destination path already exists and is not
// an empty directory") until `ox doctor --fix` was run by hand.
func TestSyncScheduler_Checkout_CloneLandsOverLocalOnlySkeleton(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}

	whisperCache := func(t *testing.T, repoPath string) {
		dir := filepath.Join(repoPath, ".sageox", "cache", "whisper")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "whisper.db"), []byte("db"), 0o644))
	}
	cacheAndEmptySessions := func(t *testing.T, repoPath string) {
		whisperCache(t, repoPath)
		require.NoError(t, os.MkdirAll(filepath.Join(repoPath, "sessions"), 0o755))
	}

	tests := []struct {
		name     string
		repoType string
		prepare  func(t *testing.T, repoPath string)
		before   bool // skeleton exists before Checkout starts, not mid-clone
	}{
		{name: "ledger: cache plus empty sessions appear mid-clone", repoType: "ledger", prepare: cacheAndEmptySessions},
		{name: "ledger: cache plus empty sessions exist before Checkout", repoType: "ledger", prepare: cacheAndEmptySessions, before: true},
		{name: "team context: whisper cache appears mid-clone", repoType: "team-context", prepare: whisperCache},
		{name: "team context: whisper cache exists before Checkout", repoType: "team-context", prepare: whisperCache, before: true},
		{name: "team context: whisper cache plus empty dir exist before Checkout", repoType: "team-context", prepare: cacheAndEmptySessions, before: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := checkoutTestScheduler(t)
			cloneURL := setupBareLedgerHTTP(t)
			if tt.repoType == "team-context" {
				cloneURL = serveTeamContext(t)
			}
			repoPath := filepath.Join(t.TempDir(), "target")
			if tt.before {
				tt.prepare(t, repoPath)
			} else {
				s.onBeforeCloneSem = func() { tt.prepare(t, repoPath) }
			}

			result, err := s.Checkout(CheckoutPayload{CloneURL: cloneURL, RepoPath: repoPath, RepoType: tt.repoType}, nil)

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.True(t, result.Cloned)
			assert.DirExists(t, filepath.Join(repoPath, ".git"), "the clone must land")
			assert.FileExists(t, filepath.Join(repoPath, ".sageox", "cache", "whisper", "whisper.db"), "local cache is carried into the clone")
			assert.Empty(t, tmpCloneSiblings(t, repoPath))
			siblings, err := os.ReadDir(filepath.Dir(repoPath))
			require.NoError(t, err)
			assert.Len(t, siblings, 1, "nothing but the clone may remain in its parent (no staging or aside directory): %v", siblings)
			backups, _ := filepath.Glob(repoPath + ".bak.*")
			assert.Empty(t, backups, "a local-only skeleton must not be moved aside as corrupt")
			assert.NoDirExists(t, filepath.Join(repoPath, "sessions"), "the empty skeleton folder is not carried")
		})
	}
}

// Failure prevented: a team-context clone that fails (server unreachable) left
// the coworker's local cache moved away, or stranded a staging directory in the
// teams directory where it would be listed as a team.
func TestSyncScheduler_Checkout_FailedTeamCloneKeepsCacheAndLeavesNoOrphan(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	s := checkoutTestScheduler(t)
	repoPath := filepath.Join(t.TempDir(), "team_failing")
	cache := filepath.Join(repoPath, ".sageox", "cache", "whisper", "whisper.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(cache), 0o755))
	require.NoError(t, os.WriteFile(cache, []byte("db"), 0o644))

	_, err := s.Checkout(CheckoutPayload{CloneURL: "http://127.0.0.1:1/team.git", RepoPath: repoPath, RepoType: "team-context"}, nil)

	require.Error(t, err)
	assert.FileExists(t, cache, "a failed clone must leave the local cache where it was")
	siblings, readErr := os.ReadDir(filepath.Dir(repoPath))
	require.NoError(t, readErr)
	assert.Len(t, siblings, 1, "a failed clone must not strand a staging directory: %v", siblings)
	lastErr, _ := s.LastError()
	assert.Contains(t, lastErr, "clone team-context failed", "the failure must be surfaced, not swallowed")
}
