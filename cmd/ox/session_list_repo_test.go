package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sandboxSessionCache redirects paths.CacheDir() into a fresh temp dir for
// the life of the test, so session store fixtures never touch the
// developer's real ~/.cache/sageox. XDG_CACHE_HOME is set directly (rather
// than cleared to fall back through HOME) because paths.getHomeDir() caches
// os.UserHomeDir() behind a sync.Once for the life of the test binary --
// relying on a HOME override alone would only take effect if this happened
// to be the first test in the package to resolve a path, which isn't
// guaranteed.
func sandboxSessionCache(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

// newTestSessionListCmd builds an isolated 'session list' command with its
// own root (carrying the persistent --json flag runSessionList reads via
// cmd.Root()), instead of reusing the package-level sessionListCmd/rootCmd
// singletons that every other test in this package's binary also mutates.
func newTestSessionListCmd() *cobra.Command {
	root := &cobra.Command{Use: "ox"}
	root.PersistentFlags().Bool("json", false, "")

	cmd := &cobra.Command{Use: "list"}
	cmd.Flags().Int("limit", 10, "")
	cmd.Flags().Bool("all", false, "")
	cmd.Flags().String("repo", "", "")
	root.AddCommand(cmd)

	return cmd
}

// --- newSessionStoreForRepoPath ---

func TestNewSessionStoreForRepoPath_ValidRepo(t *testing.T) {
	sandboxSessionCache(t)
	dir := createInitializedProjectWithConfig(t, &config.ProjectConfig{RepoID: "repo_test_valid_0001"})

	store, resolved, err := newSessionStoreForRepoPath(dir)
	require.NoError(t, err)
	require.NotNil(t, store)

	wantAbs, err := filepath.Abs(dir)
	require.NoError(t, err)
	assert.Equal(t, wantAbs, resolved)
}

// TestNewSessionStoreForRepoPath_NoSageoxDir_HardErrors proves the top risk
// flagged for this feature: an unconfigured --repo path must hard-error, not
// silently resolve to the shared base sessions directory that
// paths.SessionCacheDir returns for an empty repo ID.
func TestNewSessionStoreForRepoPath_NoSageoxDir_HardErrors(t *testing.T) {
	sandboxSessionCache(t)
	dir := t.TempDir() // no .sageox/ at all

	store, resolved, err := newSessionStoreForRepoPath(dir)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.Empty(t, resolved)
}

// TestNewSessionStoreForRepoPath_SageoxDirNoConfig_HardErrors covers the
// specific shape LoadProjectConfig treats as "present but uninitialized": a
// .sageox/ directory that exists with no config.json/config.yaml inside.
// getRepoIDOrDefault (the cwd path used by newSessionStore) papers over this
// with the shared "default" bucket; newSessionStoreForRepoPath must not.
func TestNewSessionStoreForRepoPath_SageoxDirNoConfig_HardErrors(t *testing.T) {
	sandboxSessionCache(t)
	dir := createInitializedProject(t) // .sageox/ exists, no config.json

	store, _, err := newSessionStoreForRepoPath(dir)
	require.Error(t, err)
	assert.Nil(t, store)
}

// --- runSessionList --repo ---

// TestRunSessionList_RepoFlag_ListsTargetNotCwd is the core cross-repo
// scenario this feature exists for: a caller sitting in repo A wants
// sessions for repo B without cd-ing there. Failure mode prevented: --repo
// being ignored, or silently listing the cwd's sessions instead of the
// named repo's.
func TestRunSessionList_RepoFlag_ListsTargetNotCwd(t *testing.T) {
	sandboxSessionCache(t)
	t.Setenv(config.EnvProjectRoot, "")

	dirA := createInitializedProjectWithConfig(t, &config.ProjectConfig{RepoID: "repo_test_cwd_a"})
	dirB := createInitializedProjectWithConfig(t, &config.ProjectConfig{RepoID: "repo_test_target_b"})

	createSessionInDir(t, session.GetContextPath("repo_test_cwd_a"), "2026-01-01T00-00-testuser-OxAAAA")
	createSessionInDir(t, session.GetContextPath("repo_test_target_b"), "2026-01-02T00-00-testuser-OxBBBB")

	t.Chdir(dirA) // cwd is repo A; --repo below asks for repo B

	cmd := newTestSessionListCmd()
	require.NoError(t, cmd.Flags().Set("repo", dirB))
	require.NoError(t, cmd.Flags().Set("all", "true"))
	require.NoError(t, cmd.Root().PersistentFlags().Set("json", "true"))
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := runSessionList(cmd, nil)
	require.NoError(t, err)

	var out sessionListOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))

	assert.Equal(t, "repo_test_target_b", out.RepoID, "must resolve repo id from --repo path, not cwd")
	assert.Equal(t, filepath.Base(dirB), out.RepoName)
	require.Len(t, out.Sessions, 1, "must list only the --repo target's session")
	assert.Equal(t, "2026-01-02T00-00-testuser-OxBBBB", out.Sessions[0].Name)
}

// TestRunSessionList_RepoFlag_MissingConfigHardErrors proves --repo does NOT
// fall back to the shared bucket paths.SessionCacheDir("") returns for an
// empty repo ID -- that fallback would leak unrelated sessions under the
// name of the repo the caller asked for.
func TestRunSessionList_RepoFlag_MissingConfigHardErrors(t *testing.T) {
	sandboxSessionCache(t)
	t.Setenv(config.EnvProjectRoot, "")

	// plant a session in the shared bucket. If --repo ever silently fell
	// back to it (the bug this test exists to prevent), it would show up
	// in the output asserted below.
	createSessionInDir(t, session.GetContextPath(""), "2026-01-03T00-00-eve-OxLEAK")

	unconfigured := createInitializedProject(t) // .sageox/ exists, no config.json

	cmd := newTestSessionListCmd()
	require.NoError(t, cmd.Flags().Set("repo", unconfigured))
	require.NoError(t, cmd.Root().PersistentFlags().Set("json", "true"))
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := runSessionList(cmd, nil)
	require.Error(t, err)
	assert.Empty(t, buf.String(), "hard error must produce no output, including no leaked shared-bucket session")
}

// TestRunSessionList_NoRepoFlag_UsesCwd is the regression baseline: omitting
// --repo must behave exactly as it did before this feature existed.
func TestRunSessionList_NoRepoFlag_UsesCwd(t *testing.T) {
	sandboxSessionCache(t)
	t.Setenv(config.EnvProjectRoot, "")

	dir := createInitializedProjectWithConfig(t, &config.ProjectConfig{RepoID: "repo_test_cwd_only"})
	createSessionInDir(t, session.GetContextPath("repo_test_cwd_only"), "2026-01-04T00-00-testuser-OxCCCC")

	t.Chdir(dir)

	cmd := newTestSessionListCmd()
	require.NoError(t, cmd.Flags().Set("all", "true"))
	require.NoError(t, cmd.Root().PersistentFlags().Set("json", "true"))
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := runSessionList(cmd, nil)
	require.NoError(t, err)

	var out sessionListOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	assert.Equal(t, "repo_test_cwd_only", out.RepoID)
	require.Len(t, out.Sessions, 1)
	assert.Equal(t, "2026-01-04T00-00-testuser-OxCCCC", out.Sessions[0].Name)
}

// --- runSessionList --since / --until / --window ---

// sessionNameAgo names a session directory as production does, with a UTC
// minute-resolution prefix the store parses back into the session's start time.
func sessionNameAgo(age time.Duration, suffix string) string {
	return time.Now().UTC().Add(-age).Format("2006-01-02T15-04") + "-testuser-" + suffix
}

// runSessionListWindow drives the production flag surface and RunE and
// returns the decoded JSON.
func runSessionListWindow(t *testing.T, args ...string) (sessionListOutput, error) {
	t.Helper()
	root := &cobra.Command{Use: "ox", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().Bool("json", false, "")
	cmd := &cobra.Command{Use: "list", RunE: runSessionList}
	registerSessionListFlags(cmd)
	root.AddCommand(cmd)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(append([]string{"list", "--json"}, args...))
	if err := root.Execute(); err != nil {
		return sessionListOutput{}, err
	}
	var out sessionListOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out), "stdout must be one JSON document: %q", buf.String())
	return out, nil
}

func sessionNames(out sessionListOutput) []string {
	names := make([]string, 0, len(out.Sessions))
	for _, s := range out.Sessions {
		names = append(names, s.Name)
	}
	slices.Sort(names)
	return names
}

// TestRunSessionList_SinceUntilWindow: `session list` answers to --since and
// --until, and an explicit window replaces the 7-day default instead of being
// clipped by it.
// Failure prevented: `session list --since 2d` / `--window 2d` is "unknown
// flag", or `--since 30d` is silently answered from the last week only.
func TestRunSessionList_SinceUntilWindow(t *testing.T) {
	sandboxSessionCache(t)
	t.Setenv(config.EnvProjectRoot, "")

	dir := createInitializedProjectWithConfig(t, &config.ProjectConfig{RepoID: "repo_test_session_window"})
	recent := sessionNameAgo(30*time.Minute, "OxNew1")
	lastWeek := sessionNameAgo(3*24*time.Hour, "OxMid2")
	old := sessionNameAgo(20*24*time.Hour, "OxOld3")
	for _, name := range []string{recent, lastWeek, old} {
		createSessionInDir(t, session.GetContextPath("repo_test_session_window"), name)
	}
	t.Chdir(dir)

	tests := []struct {
		name       string
		args       []string
		want       []string
		wantWindow string
	}{
		{"default is the last 7 days", nil, []string{lastWeek, recent}, "7d"},
		{"all shows everything", []string{"--all"}, []string{lastWeek, old, recent}, "all"},
		{"since 2d", []string{"--since", "2d"}, []string{recent}, "since=2d"},
		{"since reaches past the default week", []string{"--since", "30d"}, []string{lastWeek, old, recent}, "since=30d"},
		{"window is an alias of since", []string{"--window", "2d"}, []string{recent}, "since=2d"},
		{"window alias also reaches past the default week", []string{"--window", "30d"}, []string{lastWeek, old, recent}, "since=30d"},
		{"since wins when both are given", []string{"--since", "2d", "--window", "30d"}, []string{recent}, "since=2d"},
		{"until alone replaces the default week", []string{"--until", "1d"}, []string{lastWeek, old}, "until=1d"},
		{"since and until bound both ends", []string{"--since", "30d", "--until", "10d"}, []string{old}, "since=30d until=10d"},
		{"limit applies inside the window", []string{"--since", "30d", "--limit", "1"}, nil, "since=30d"},
		{"window with no match", []string{"--since", "5m"}, []string{}, "since=5m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := runSessionListWindow(t, tt.args...)
			require.NoError(t, err)
			got := sessionNames(out)
			if tt.want == nil {
				// limit case: the page size is the claim, not which row ModTime ordering picks
				assert.Len(t, got, 1)
			} else {
				assert.ElementsMatch(t, tt.want, got)
			}
			assert.Equal(t, tt.wantWindow, out.Window, "window must describe what was actually listed")
		})
	}
}

// TestRunSessionList_InvalidWindowIsAUsageError: a bad value names its flag
// and an example; an inverted window is refused rather than listed empty.
func TestRunSessionList_InvalidWindowIsAUsageError(t *testing.T) {
	sandboxSessionCache(t)
	t.Setenv(config.EnvProjectRoot, "")
	dir := createInitializedProjectWithConfig(t, &config.ProjectConfig{RepoID: "repo_test_session_window_bad"})
	t.Chdir(dir)

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"bad since", []string{"--since", "lately"}, []string{"--since", "lately", "2026-10-01"}},
		{"bad window alias", []string{"--window", "lately"}, []string{"--since", "lately"}},
		{"bad until", []string{"--until", "soon"}, []string{"--until", "soon", "2026-10-01"}},
		{"inverted window", []string{"--since", "1d", "--until", "7d"}, []string{"--since", "--until"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := runSessionListWindow(t, tt.args...)
			require.Error(t, err)
			for _, want := range tt.want {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}
