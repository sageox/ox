package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/manifest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newSparseLockRepo(t *testing.T) (repo string, cfg *manifest.ManifestConfig) {
	t.Helper()
	repo = t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo // never the developer's own repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("config", "commit.gpgsign", "false")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".sageox", "sync.manifest"), []byte("version 1\ninclude .sageox/\ninclude docs/\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "docs", "a.md"), []byte("a\n"), 0o644))
	run("add", ".")
	run("commit", "-q", "-m", "seed")
	cfg = manifest.ParseFile(filepath.Join(repo, ".sageox", "sync.manifest"), manifest.RepoKindTeamContext)
	return repo, cfg
}

// A 0-byte info/sparse-checkout.lock left by a dead git process blocked team
// sync until it was removed by hand. The re-apply must clear a stale one and
// succeed in the same pass.
func TestApplySparseFromManifest_ClearsStaleSparseCheckoutLock(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	repo, cfg := newSparseLockRepo(t)
	lock := filepath.Join(repo, ".git", "info", "sparse-checkout.lock")
	require.NoError(t, os.MkdirAll(filepath.Dir(lock), 0o755))
	require.NoError(t, os.WriteFile(lock, nil, 0o644))
	old := time.Now().Add(-(gitutil.SparseCheckoutLockAge + time.Minute))
	require.NoError(t, os.Chtimes(lock, old, old))

	err := applySparseFromManifest(context.Background(), repo, cfg, manifest.RepoKindTeamContext, nil)

	require.NoError(t, err)
	assert.NoFileExists(t, lock)
}

// A fresh lock may belong to a live git; it is left alone and the failure is
// reported, never forced.
func TestApplySparseFromManifest_KeepsFreshSparseCheckoutLock(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	repo, cfg := newSparseLockRepo(t)
	lock := filepath.Join(repo, ".git", "info", "sparse-checkout.lock")
	require.NoError(t, os.MkdirAll(filepath.Dir(lock), 0o755))
	require.NoError(t, os.WriteFile(lock, nil, 0o644))

	err := applySparseFromManifest(context.Background(), repo, cfg, manifest.RepoKindTeamContext, nil)

	require.Error(t, err)
	assert.FileExists(t, lock)
}

// TestApplySparseFromManifest_WaitsForTheRepoLock: the per-pull sparse
// re-apply rewrites the team context's index. Failure prevented: run while an
// ox commit holds the repo lock (`ox import`, `ox memory put`), it fails that
// commit on index.lock.
func TestApplySparseFromManifest_WaitsForTheRepoLock(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	repo, cfg := newSparseLockRepo(t)
	sparseFile := filepath.Join(repo, ".git", "info", "sparse-checkout")

	require.NoError(t, gitutil.WithRepoLock(context.Background(), repo, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		err := applySparseFromManifest(ctx, repo, cfg, manifest.RepoKindTeamContext, nil)
		assert.True(t, gitutil.IsRepoLockBusy(err), "want the lock-busy error, got %v", err)
		assert.NoFileExists(t, sparseFile, "the re-apply ran while a peer held the repo lock")
		return nil
	}))

	require.NoError(t, applySparseFromManifest(context.Background(), repo, cfg, manifest.RepoKindTeamContext, nil))
	assert.FileExists(t, sparseFile, "the re-apply must run once the lock is free")
}
