package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/githubmirror"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func testGitHubIndexCommand(t *testing.T) (*cobra.Command, string, string) {
	t.Helper()
	repo := newIndexTestRepo(t)
	t.Chdir(repo)
	remote := exec.Command("git", "remote", "add", "origin", "https://github.com/acme/example.git")
	remote.Dir = repo
	require.NoError(t, remote.Run())
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".sageox"), 0o700))
	ledger := t.TempDir()
	require.NoError(t, config.SaveLocalConfig(repo, &config.LocalConfig{Ledger: &config.LedgerConfig{Path: ledger}}))
	t.Setenv("GITHUB_TOKEN", "test-token")
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.Flags().Bool("full", false, "")
	cmd.Flags().Bool("prs-only", false, "")
	cmd.Flags().Bool("issues-only", false, "")
	cmd.Flags().Bool("no-push", false, "")
	cmd.Flags().Int("days", 30, "")
	return cmd, findGitRoot(), ledger
}

func TestIndexGitHubCommandNoPushBuildsFreshIndex(t *testing.T) {
	cmd, root, ledger := testGitHubIndexCommand(t)
	require.NoError(t, cmd.Flags().Set("days", "120"))
	require.NoError(t, cmd.Flags().Set("full", "true"))
	require.NoError(t, cmd.Flags().Set("prs-only", "true"))
	require.NoError(t, cmd.Flags().Set("no-push", "true"))
	previous := runManualGitHubMirror
	t.Cleanup(func() { runManualGitHubMirror = previous })
	runManualGitHubMirror = func(ctx context.Context, projectRoot string, target daemon.MirrorTarget, opts daemon.ManualMirrorOptions) (daemon.ManualMirrorResult, error) {
		require.Equal(t, root, projectRoot)
		require.Equal(t, 120, opts.Days)
		require.True(t, opts.Full)
		require.True(t, opts.NoPush)
		require.True(t, target.PullRequests)
		require.False(t, target.Issues)
		n, err := opts.IndexLocal(ctx, []githubmirror.Item{{Kind: githubmirror.KindPullRequest, Number: 41, Title: "fresh search marker", State: "open", CreatedAt: time.Now(), UpdatedAt: time.Now()}})
		return daemon.ManualMirrorResult{Indexed: n}, err
	}
	require.NoError(t, runIndexGitHub(cmd, nil))
	db, err := codedb.OpenSQLOnly(resolveCodeDBDir(root))
	require.NoError(t, err)
	defer db.Close()
	var title string
	require.NoError(t, db.Store().QueryRow("SELECT title FROM pull_requests WHERE number = 41").Scan(&title))
	require.Equal(t, "fresh search marker", title)
	_, err = os.Stat(filepath.Join(ledger, "data", "github"))
	require.True(t, os.IsNotExist(err))
}

func TestIndexGitHubCommandPassesNormalFilters(t *testing.T) {
	cmd, _, _ := testGitHubIndexCommand(t)
	require.NoError(t, cmd.Flags().Set("days", "7"))
	require.NoError(t, cmd.Flags().Set("issues-only", "true"))
	previous := runManualGitHubMirror
	t.Cleanup(func() { runManualGitHubMirror = previous })
	sentinel := errors.New("relay unavailable")
	runManualGitHubMirror = func(_ context.Context, _ string, target daemon.MirrorTarget, opts daemon.ManualMirrorOptions) (daemon.ManualMirrorResult, error) {
		require.Equal(t, 7, opts.Days)
		require.False(t, opts.NoPush)
		require.False(t, target.PullRequests)
		require.True(t, target.Issues)
		return daemon.ManualMirrorResult{}, sentinel
	}
	require.ErrorIs(t, runIndexGitHub(cmd, nil), sentinel)
}
