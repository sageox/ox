//go:build slow

package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. GC reclone for team context workspace ---

// TestGC_TeamContext_FullCycle_RealRemote verifies the blue-green GC
// reclone for team context workspaces (not ledger), which uses
// TwoPhaseClone internally instead of CloneWithSparseCheckout.
// Failure prevented: GC reclone for team context fails because
// TwoPhaseClone's --depth=1 + unshallow path differs from ledger's
// full-depth clone, and the validation checks different files.
func TestGC_TeamContext_FullCycle_RealRemote(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-gc-teamctx")

	// push team context structure
	pushMultipleFiles(t, cloneURL, map[string]string{
		".sageox/sync.manifest": "version 1\ninclude docs/\ninclude memory/\n",
		".sageox/config.json":   `{"version":1}`,
		"SOUL.md":               "# Soul\nTeam identity.\n",
		"memory/daily/obs.md":   "observation one\n",
		"docs/guide.md":         "# Guide\nHow to do things.\n",
	})

	// clone locally (simulating existing workspace)
	cloneDir := filepath.Join(t.TempDir(), "teamctx")
	g.cloneRepo(t, cloneURL, cloneDir)

	// add dirty state
	require.NoError(t, os.WriteFile(
		filepath.Join(cloneDir, "SOUL.md"),
		[]byte("# Soul\nModified locally.\n"), 0o644))

	// add untracked file
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, "memory", "local"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(cloneDir, "memory", "local", "draft.md"),
		[]byte("in-progress memory\n"), 0o644))

	// add cache
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, ".sageox", "cache"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(cloneDir, ".sageox", "cache", "state.json"),
		[]byte(`{"cached":true}`), 0o644))

	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	ws := WorkspaceState{
		ID:       "teamctx-gc-test",
		Type:     WorkspaceTypeTeamContext,
		Path:     cloneDir,
		CloneURL: cloneURL,
		Exists:   true,
	}

	result := s.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result, "GC reclone should succeed for team context")

	// verify git repo exists
	_, err := os.Stat(filepath.Join(cloneDir, ".git", "HEAD"))
	require.NoError(t, err, ".git/HEAD should exist after GC")

	// verify .sageox/ exists
	require.DirExists(t, filepath.Join(cloneDir, ".sageox"))

	// verify SOUL.md exists (core file for team context validation)
	require.FileExists(t, filepath.Join(cloneDir, "SOUL.md"))

	// verify dirty state restored
	content, err := os.ReadFile(filepath.Join(cloneDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "Modified locally",
		"dirty SOUL.md should be restored after GC")

	// verify untracked file restored
	content, err = os.ReadFile(filepath.Join(cloneDir, "memory", "local", "draft.md"))
	require.NoError(t, err)
	assert.Equal(t, "in-progress memory\n", string(content))

	// verify .old and .new cleaned up
	_, err = os.Stat(cloneDir + ".old")
	assert.True(t, os.IsNotExist(err), ".old should be cleaned up")
	_, err = os.Stat(cloneDir + ".new")
	assert.True(t, os.IsNotExist(err), ".new should be cleaned up")
}

// --- B. GC with unpushed commits ---

// TestGC_TeamContext_UnpushedCommits_RealRemote verifies that unpushed
// local commits are pushed to the remote before reclone, and survive
// the GC cycle.
// Failure prevented: unpushed team context edits (memory/, docs/)
// silently lost during daemon GC reclone.
func TestGC_TeamContext_UnpushedCommits_RealRemote(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-gc-unpushed")

	pushMultipleFiles(t, cloneURL, map[string]string{
		".sageox/sync.manifest": "version 1\ninclude memory/\n",
		".sageox/config.json":   `{"version":1}`,
		"SOUL.md":               "# Soul\n",
		"memory/daily/day1.md":  "day 1\n",
	})

	cloneDir := filepath.Join(t.TempDir(), "teamctx")
	g.cloneRepo(t, cloneURL, cloneDir)

	// make an unpushed commit
	twinCommitFile(t, cloneDir, "memory/daily/day2.md", "day 2 observation\n", "add day 2")

	// verify it's unpushed
	cmd := exec.Command("git", "-C", cloneDir, "log", "--oneline", "@{u}..HEAD")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err)
	assert.Contains(t, string(out), "add day 2", "should have unpushed commit")

	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	ws := WorkspaceState{
		ID:       "unpushed-tc",
		Type:     WorkspaceTypeTeamContext,
		Path:     cloneDir,
		CloneURL: cloneURL,
		Exists:   true,
	}

	result := s.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result, "GC should succeed after pushing unpushed commits")

	// verify the unpushed content survives reclone
	require.FileExists(t, filepath.Join(cloneDir, "memory", "daily", "day2.md"))
	content, err := os.ReadFile(filepath.Join(cloneDir, "memory", "daily", "day2.md"))
	require.NoError(t, err)
	assert.Equal(t, "day 2 observation\n", string(content))
}

// --- C. GC skips when push fails ---

// TestGC_TeamContext_PushFails_SkipsDirty verifies that GC returns
// gcSkippedDirty when unpushed commits cannot be pushed (e.g., bad
// credentials), protecting local data.
// Failure prevented: GC reclone proceeds despite push failure, losing
// unpushed commits that only existed locally.
func TestGC_TeamContext_PushFails_SkipsDirty(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-gc-pushfail")

	pushMultipleFiles(t, cloneURL, map[string]string{
		".sageox/sync.manifest": "version 1\n",
		".sageox/config.json":   `{"version":1}`,
		"SOUL.md":               "# Soul\n",
	})

	cloneDir := filepath.Join(t.TempDir(), "teamctx")
	g.cloneRepo(t, cloneURL, cloneDir)

	// make unpushed commit
	twinCommitFile(t, cloneDir, "docs/new.md", "new doc\n", "add doc")

	// break remote URL so push fails
	cmd := exec.Command("git", "-C", cloneDir, "remote", "set-url", "origin",
		"http://baduser:badpass@localhost:"+giteaHostPort+"/testadmin/twin-gc-pushfail.git")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "set-url: %s", string(out))

	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	ws := WorkspaceState{
		ID:       "pushfail-tc",
		Type:     WorkspaceTypeTeamContext,
		Path:     cloneDir,
		CloneURL: "http://baduser:badpass@localhost:" + giteaHostPort + "/testadmin/twin-gc-pushfail.git",
		Exists:   true,
	}

	result := s.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSkippedDirty, result,
		"GC should skip when unpushed commits can't be pushed")

	// original clone should be untouched
	require.FileExists(t, filepath.Join(cloneDir, "docs", "new.md"))
}
