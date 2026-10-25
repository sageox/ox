package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/require"
)

// newWorktreeRepo builds an initialized SageOx repository with .sageox/
// committed and a linked worktree laid out the way the Claude desktop app lays
// one out: <repo>/.claude/worktrees/<name>. Returns both checkouts, resolved.
func newWorktreeRepo(t *testing.T) (mainRoot, worktree string) {
	t.Helper()
	mainRoot = resolvePathForCompare(t.TempDir())
	initGitRepo(t, mainRoot)
	require.NoError(t, config.SaveProjectConfig(mainRoot, &config.ProjectConfig{
		RepoID:   "repo_worktree_store_test",
		Endpoint: "http://127.0.0.1:1",
	}))
	worktree = filepath.Join(mainRoot, ".claude", "worktrees", "wt")
	gitOK(t, mainRoot, "add", ".sageox")
	gitOK(t, mainRoot, "commit", "-m", "initialize SageOx")
	gitOK(t, mainRoot, "worktree", "add", worktree, "-b", "wt")
	require.True(t, config.IsInitialized(worktree), "the worktree checks out its own .sageox/")
	return mainRoot, worktree
}

func gitOK(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := runIsolatedGit(t, dir, args...)
	require.NoError(t, err, "git %v: %s", args, out)
}

// An AI coworker primed in one checkout of a repository keeps working from any
// other worktree of it.
//
// Failure prevented (#1407): the Claude desktop app runs hooks — and so
// `ox agent prime` — in the main checkout while the coworker's shell works in a
// linked worktree. The instance landed in the main checkout's store, and every
// later `ox agent <id> heartbeat | session stop | ...` from the worktree failed
// with "instance not found", so the session could not be stopped and its
// recording was never finalized.
func TestAgentCommandsFindInstanceAcrossWorktrees(t *testing.T) {
	if testing.Short() {
		t.Skip("short: builds and runs a real ox binary")
	}
	oxBin := testguard.BuildOxBinary(t, filepath.Dir(filepath.Dir(packageDir)))

	for _, tc := range []struct {
		name           string
		primeIn, useIn func(mainRoot, worktree string) string
	}{
		{
			name:    "primed in main checkout, used in worktree",
			primeIn: func(mainRoot, _ string) string { return mainRoot },
			useIn:   func(_, worktree string) string { return worktree },
		},
		{
			name:    "primed in worktree, used in main checkout",
			primeIn: func(_, worktree string) string { return worktree },
			useIn:   func(mainRoot, _ string) string { return mainRoot },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mainRoot, worktree := newWorktreeRepo(t)
			home := t.TempDir()
			env := []string{
				"HOME=" + home,
				"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
				"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
				"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
				"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
				"GIT_CONFIG_GLOBAL=/dev/null",
				"SAGEOX_ENDPOINT=http://127.0.0.1:1",
				"HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1",
				"NO_PROXY=127.0.0.1,localhost",
			}

			prime := testguard.OxCmd(t, oxBin, tc.primeIn(mainRoot, worktree), env,
				"agent", "prime", "--agent", "codex", "--format", "json")
			stdout, err := prime.Output()
			require.NoError(t, err, "prime: %s", stdout)
			var primed struct {
				AgentID string `json:"agent_id"`
			}
			require.NoError(t, json.Unmarshal(stdout, &primed), "prime output: %s", stdout)
			require.NotEmpty(t, primed.AgentID, "prime output: %s", stdout)

			output, code, _ := testguard.RunOx(t, oxBin, tc.useIn(mainRoot, worktree), env,
				"agent", primed.AgentID, "heartbeat")
			require.Equal(t, 0, code, "heartbeat from the other checkout must find the instance prime created:\n%s", output)

			// an unknown ID still fails, and says which store it searched
			output, code, _ = testguard.RunOx(t, oxBin, tc.useIn(mainRoot, worktree), env,
				"agent", "OxZzzz", "heartbeat")
			require.NotEqual(t, 0, code, output)
			require.Contains(t, output, "instance not found: OxZzzz")
			require.Contains(t, output, "Searched: "+filepath.Join(mainRoot, ".sageox", "agent_instances"))
		})
	}
}

func TestInstanceStoreRoot(t *testing.T) {
	mainRoot, worktree := newWorktreeRepo(t)

	uninitMain := resolvePathForCompare(t.TempDir())
	initGitRepo(t, uninitMain)
	uninitWorktree := filepath.Join(uninitMain, "wt")
	gitOK(t, uninitMain, "worktree", "add", uninitWorktree, "-b", "wt")
	require.NoError(t, config.SaveProjectConfig(uninitWorktree, &config.ProjectConfig{RepoID: "repo_worktree_only"}))

	notARepo := resolvePathForCompare(t.TempDir())

	for _, tc := range []struct {
		name        string
		projectRoot string
		want        string
	}{
		{"main checkout keeps its own store", mainRoot, mainRoot},
		{"linked worktree shares the main checkout's store", worktree, mainRoot},
		{"main checkout not initialized: worktree keeps its own store", uninitWorktree, uninitWorktree},
		{"not a git repository", notARepo, notARepo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, resolvePathForCompare(instanceStoreRoot(tc.projectRoot)))
		})
	}
}

// An AI coworker primed in a worktree by an ox version that kept a store per
// checkout still resolves after the upgrade, and a lookup never creates an
// empty store in the worktree.
func TestResolveInstanceReadsWorktreeStoreFromBeforeSharing(t *testing.T) {
	mainRoot, worktree := newWorktreeRepo(t)
	t.Chdir(worktree)
	slug := getUserSlug()

	_, err := resolveInstance("OxAbcd")
	require.Error(t, err)
	require.Contains(t, err.Error(), "instance not found: OxAbcd")
	_, statErr := os.Stat(filepath.Dir(agentinstance.StorePath(worktree, slug)))
	require.True(t, os.IsNotExist(statErr), "a lookup must not create a store in the worktree")

	legacy, err := agentinstance.NewStoreForUser(worktree, slug)
	require.NoError(t, err)
	require.NoError(t, legacy.Add(&agentinstance.Instance{
		AgentID:         "OxAbcd",
		ServerSessionID: "oxsid_worktree_store_test",
		CreatedAt:       time.Now(),
		ExpiresAt:       time.Now().Add(time.Hour),
	}))

	inst, err := resolveInstance("OxAbcd")
	require.NoError(t, err)
	require.Equal(t, "OxAbcd", inst.AgentID)

	shared, err := agentinstance.NewStoreForUser(mainRoot, slug)
	require.NoError(t, err)
	_, err = shared.Get("OxAbcd")
	require.ErrorIs(t, err, agentinstance.ErrInstanceNotFound, "the fallback reads the old store; it does not copy")
}

func TestResolveInstanceReportsExpiry(t *testing.T) {
	mainRoot, worktree := newWorktreeRepo(t)
	t.Chdir(worktree)

	store, err := agentinstance.NewStoreForUser(mainRoot, getUserSlug())
	require.NoError(t, err)
	require.NoError(t, store.Add(&agentinstance.Instance{
		AgentID:         "OxOld1",
		ServerSessionID: "oxsid_worktree_store_test",
		CreatedAt:       time.Now().Add(-25 * time.Hour),
		ExpiresAt:       time.Now().Add(-time.Hour),
	}))

	_, err = resolveInstance("OxOld1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "instance expired: OxOld1")
}

// A store that cannot be read says nothing about whether the instance exists.
// Failure prevented: an I/O error on the shared store is reported as "instance
// not found", and the coworker re-primes into a new agent ID while its live
// recording is left behind.
func TestResolveInstanceReportsAnUnreadableStore(t *testing.T) {
	mainRoot, worktree := newWorktreeRepo(t)
	t.Chdir(worktree)

	// a directory where the store file belongs: Open succeeds, reading fails
	require.NoError(t, os.MkdirAll(agentinstance.StorePath(mainRoot, getUserSlug()), 0o755))

	_, err := resolveInstance("OxAbcd")
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to read instance store")
	require.NotContains(t, err.Error(), "instance not found")
}
