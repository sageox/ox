package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
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

func TestIndexGitHubCommandSkipsDisabledKinds(t *testing.T) {
	cmd, _, _ := testGitHubIndexCommand(t)
	require.NoError(t, cmd.Flags().Set("prs-only", "true"))
	require.NoError(t, cmd.Flags().Set("issues-only", "true"))
	previous := runManualGitHubMirror
	t.Cleanup(func() { runManualGitHubMirror = previous })
	called := false
	runManualGitHubMirror = func(context.Context, string, daemon.MirrorTarget, daemon.ManualMirrorOptions) (daemon.ManualMirrorResult, error) {
		called = true
		return daemon.ManualMirrorResult{}, nil
	}
	require.NoError(t, runIndexGitHub(cmd, nil))
	require.False(t, called, "the command must not fetch or publish when neither kind is selected")
}

func TestIndexGitHubCommandNoPushCanReuseLocallyIndexedItems(t *testing.T) {
	cmd, root, ledger := testGitHubIndexCommand(t)
	require.NoError(t, cmd.Flags().Set("days", "0"))
	require.NoError(t, cmd.Flags().Set("no-push", "true"))
	previous := runManualGitHubMirror
	t.Cleanup(func() { runManualGitHubMirror = previous })
	updated := time.Now().UTC().Truncate(time.Second)
	runManualGitHubMirror = func(ctx context.Context, projectRoot string, target daemon.MirrorTarget, opts daemon.ManualMirrorOptions) (daemon.ManualMirrorResult, error) {
		require.Equal(t, root, projectRoot)
		require.True(t, opts.NoPush)
		require.False(t, opts.Full)
		require.Equal(t, defaultGitHubSyncMaxDays, opts.Days)
		item := githubmirror.Item{Kind: githubmirror.KindPullRequest, Number: 41, Title: "local search marker", State: "open", URL: "https://github.com/acme/example/pull/41", CreatedAt: updated, UpdatedAt: updated}
		n, err := opts.IndexLocal(ctx, []githubmirror.Item{item})
		require.NoError(t, err)
		known, err := opts.KnownLocal(ctx)
		require.NoError(t, err)
		require.Equal(t, updated, known[githubmirror.SourceKey(target.Owner, target.Repo, item.Kind, item.Number)].UpdatedAt)
		return daemon.ManualMirrorResult{Indexed: n}, nil
	}
	require.NoError(t, runIndexGitHub(cmd, nil))
	_, err := os.Stat(filepath.Join(ledger, "data", "github"))
	require.True(t, os.IsNotExist(err), "local-only indexing must not create Ledger snapshots")
}

func TestIndexGitHubCommandReportsRefreshAfterPublication(t *testing.T) {
	for _, tc := range []struct {
		name, response, want string
		indexErr             error
	}{
		{"team synced", `{"success":true,"data":[{"team_id":"team_1","status":"synced"}]}`, "Team Context synced; search refresh requested.", nil},
		{"team sync failed", `{"success":false,"error":"sync unavailable","data":[]}`, "Published; search refresh requested through repository access.", nil},
		{"team checkout unavailable", `{"success":true,"data":[]}`, "Published; search refresh requested through repository access.", nil},
		{"index request failed", `{"success":true,"data":[{"team_id":"team_1","status":"synced"}]}`, "Published; search indexing is pending.", errors.New("index unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, root, ledger := testGitHubIndexCommand(t)
			t.Setenv("SAGEOX_DAEMON", "false") // prevents a real daemon spawn if the fake is unavailable
			t.Setenv("OX_XDG_ENABLE", "1")
			t.Setenv("OX_XDG_DISABLE", "")
			runtimeDir, err := os.MkdirTemp("/tmp", "ox-index-")
			require.NoError(t, err)
			t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
			t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
			require.NoError(t, config.SaveProjectConfig(root, &config.ProjectConfig{RepoID: "repo_1", TeamID: "team_1"}))
			require.NoError(t, config.SaveLocalConfig(root, &config.LocalConfig{Ledger: &config.LedgerConfig{Path: ledger}, TeamContexts: []config.TeamContext{{TeamID: "team_1", Path: t.TempDir()}}}))
			sock := daemon.SocketPath()
			require.NoError(t, os.MkdirAll(filepath.Dir(sock), 0o700))
			_, statErr := os.Lstat(sock)
			require.True(t, os.IsNotExist(statErr), "test socket must not replace an existing daemon")
			ln, err := net.Listen("unix", sock)
			require.NoError(t, err)
			t.Cleanup(func() { _ = ln.Close() })
			request := make(chan string, 1)
			go func() {
				conn, acceptErr := ln.Accept()
				if acceptErr != nil {
					request <- "accept error"
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				var msg daemon.Message
				if decodeErr := json.NewDecoder(conn).Decode(&msg); decodeErr != nil {
					request <- "decode error"
					return
				}
				request <- msg.Type
				_, _ = io.WriteString(conn, tc.response+"\n")
			}()
			oldManual, oldIndex := runManualGitHubMirror, indexGitHubViaDaemon
			t.Cleanup(func() { runManualGitHubMirror, indexGitHubViaDaemon = oldManual, oldIndex })
			runManualGitHubMirror = func(context.Context, string, daemon.MirrorTarget, daemon.ManualMirrorOptions) (daemon.ManualMirrorResult, error) {
				return daemon.ManualMirrorResult{Accepted: 2, Current: 1, Unchanged: 3, Expired: 4}, nil
			}
			refreshes := 0
			indexGitHubViaDaemon = func() error { refreshes++; return tc.indexErr }
			var diagnostic bytes.Buffer
			cmd.SetErr(&diagnostic)
			stdout := captureStdoutForPlanCLI(t, func() { require.NoError(t, runIndexGitHub(cmd, nil)) })
			require.Contains(t, stdout, "2 published, 1 current, 3 unchanged, 4 expired")
			require.Equal(t, tc.want+"\n", diagnostic.String())
			require.Equal(t, 1, refreshes, "every publication requests a search refresh even when team sync fails")
			require.Equal(t, daemon.MsgTypeTeamSync, <-request)
		})
	}
}
