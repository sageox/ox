package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSyncBubbles_Pull_ReappliesSparseFromManifest verifies that when a
// bubble's server-side sync.manifest grows new `include` directives after
// the initial clone, the daemon's next pull pass materializes the newly-
// included paths instead of leaving them pinned to the clone-time set.
//
// This is a regression test for the kb sparse-on-pull drift bug: kb sync
// applied sparse-checkout only at clone time via TwoPhaseClone, while
// team-context sync (sync_team.go::applySparseCheckout) reapplied on every
// pull. With the bug, a manifest update that added new include paths
// silently never reached kb users until a full reclone.
//
// Failure prevented: server-side manifest changes (e.g., adding a new
// "include also/" line) silently never materialize in kb checkouts —
// users see stale tree contents until they reclone manually.
func TestSyncBubbles_Pull_ReappliesSparseFromManifest(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations + two sync passes")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	// --- Stage 1: bare repo with manifest including "allowed/" only. ---
	bareDir, workDir := initBareRepo(t, "sparse")

	// initial manifest: only "allowed/" is included.
	manifestV1 := "version 1\ninclude .sageox/\ninclude allowed/\n"
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, ".sageox", "sync.manifest"), []byte(manifestV1), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "allowed"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "also"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "allowed", "foo.md"), []byte("foo\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "also", "bar.md"), []byte("bar\n"), 0o644))
	require.NoError(t, exec.Command("git", "-C", workDir, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "commit", "-m", "v1: include allowed/ only").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "push", "origin", "HEAD:main").Run())

	bubble := api.KB{
		KBID:    "kb_sparse_drift",
		KBType:  api.KBTypeTeam,
		Slug:    "sparse-drift",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	// --- Stage 2: first sync — clone. allowed/ materializes, also/ does not. ---
	s.syncBubbles(context.Background())
	target := paths.KBDir(endpoint.Get(), bubble.KBID)
	require.DirExists(t, filepath.Join(target, ".git"), "first sync must clone the bubble")
	require.FileExists(t, filepath.Join(target, "allowed", "foo.md"),
		"allowed/ is in the manifest's include set, must materialize on clone")
	_, err := os.Stat(filepath.Join(target, "also", "bar.md"))
	require.True(t, os.IsNotExist(err),
		"also/ is NOT in the v1 manifest, must remain unmaterialized after clone")

	// --- Stage 3: update bare to a manifest that ALSO includes also/. ---
	manifestV2 := "version 1\ninclude .sageox/\ninclude allowed/\ninclude also/\n"
	require.NoError(t, os.WriteFile(filepath.Join(workDir, ".sageox", "sync.manifest"), []byte(manifestV2), 0o644))
	require.NoError(t, exec.Command("git", "-C", workDir, "add", ".sageox/sync.manifest").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "commit", "-m", "v2: also include also/").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "push", "origin", "HEAD:main").Run())

	// nudge FETCH_HEAD into the past so dedup doesn't skip the pull.
	fetchHead := filepath.Join(target, ".git", "FETCH_HEAD")
	if info, err := os.Stat(fetchHead); err == nil {
		past := info.ModTime().Add(-10 * time.Minute)
		_ = os.Chtimes(fetchHead, past, past)
	}

	// --- Stage 4: second sync — pull. The fix wires applySparseFromManifest
	// into reconcileBubble's post-pull path. Without it, the sparse set
	// stays pinned to the clone-time value and also/bar.md never appears. ---
	s.syncBubbles(context.Background())

	// First diagnose the pull itself: did the new manifest reach disk?
	manifestBytes, err := os.ReadFile(filepath.Join(target, ".sageox", "sync.manifest"))
	require.NoError(t, err)
	require.Contains(t, string(manifestBytes), "include also/",
		"pullManagedRepo must fetch+rebase the v2 manifest into the local checkout — without this, the sparse reapply has nothing new to act on")

	require.FileExists(t, filepath.Join(target, "allowed", "foo.md"),
		"allowed/ must remain materialized after pull")
	assert.FileExists(t, filepath.Join(target, "also", "bar.md"),
		"also/ was added to the manifest after clone — must materialize on next sync (the fix)")
}
