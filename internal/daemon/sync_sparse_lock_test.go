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

// an old ownerless sparse lock is uncertain: leave it intact and report the
// blocked update instead of permitting a second Git writer.
func TestApplySparseFromManifest_KeepsOldSparseCheckoutLock(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	repo, cfg := newSparseLockRepo(t)
	lock := filepath.Join(repo, ".git", "info", "sparse-checkout.lock")
	require.NoError(t, os.MkdirAll(filepath.Dir(lock), 0o755))
	require.NoError(t, os.WriteFile(lock, nil, 0o644))
	old := time.Now().Add(-(gitutil.AbandonedLockAge * 24))
	require.NoError(t, os.Chtimes(lock, old, old))

	err := applySparseFromManifest(context.Background(), repo, cfg, manifest.RepoKindTeamContext, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "sparse-checkout.lock")
	assert.FileExists(t, lock)
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
