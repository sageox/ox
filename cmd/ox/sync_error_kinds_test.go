package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/daemon/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSync_FailuresCarryAKindAndDetail drives `ox sync` through the real
// daemon IPC client against an in-process daemon, in text and --json mode, and
// reads the failure the way usage telemetry does.
//
// Failure prevented: every `ox sync` failure reaching PostHog as
// error_kind=other with an empty error_detail (or cli.SilentError in --json,
// which is how AI coworkers run it), so a daemon that is down, a diverged
// Ledger, and a mistyped --team are indistinguishable on the dashboard.
func TestSync_FailuresCarryAKindAndDetail(t *testing.T) {
	const secret = "sk-secret-path" // must never reach PostHog

	tests := []struct {
		name     string
		daemon   *testutil.MockService // nil: no daemon listening
		team     string
		status   func() (*daemon.StatusData, error) // nil: ask the daemon
		converge func(context.Context, string, *SyncResult) error
		kind     string
		detail   string
	}{
		{
			name:   "daemon down mid-sync",
			kind:   "daemon",
			detail: "ledger sync failed",
		},
		{
			name: "ledger diverged",
			daemon: &testutil.MockService{StatusFunc: func() *daemon.StatusData {
				return &daemon.StatusData{Running: true, Issues: []daemon.DaemonIssue{
					{Type: daemon.IssueTypeDiverged, Repo: "ledger", Summary: "diverged at /Users/x/" + secret},
				}}
			}},
			kind:   "other",
			detail: "ledger not synced: diverged",
		},
		{
			name:   "daemon gone before the ledger status check",
			daemon: &testutil.MockService{},
			status: func() (*daemon.StatusData, error) {
				err := daemon.NewClientWithSocket(filepath.Join(t.TempDir(), "none.sock")).Ping()
				require.Error(t, err)
				return nil, fmt.Errorf("query daemon status: %w", err)
			},
			kind:   "daemon",
			detail: "ledger not synced: inspect_failed",
		},
		{
			name: "team contexts not ready",
			daemon: &testutil.MockService{TeamSyncFunc: func(*daemon.ProgressWriter) ([]daemon.TeamSyncResult, error) {
				return []daemon.TeamSyncResult{
					{TeamID: "t1", TeamName: "Team " + secret, Status: "cloning"},
					{TeamID: "t2", TeamName: "Other", Status: "error", Error: "clone failed at /tmp/" + secret},
				}, nil
			}},
			kind:   "other",
			detail: "team context not ready: cloning,error",
		},
		{
			name:   "--team not found",
			daemon: &testutil.MockService{TeamSyncFunc: teamsSynced("alpha")},
			team:   "nope-" + secret,
			kind:   "usage",
			detail: "team not found",
		},
		{
			name:   "--team matches two teams",
			daemon: &testutil.MockService{TeamSyncFunc: teamsSynced("dup", "dup")},
			team:   "dup",
			kind:   "usage",
			detail: "team selector ambiguous",
		},
		{
			name:   "--team with the daemon down",
			team:   "alpha",
			kind:   "daemon",
			detail: "team context sync failed",
		},
		{
			name: "--team failure the daemon reported",
			daemon: &testutil.MockService{TeamSyncFunc: func(*daemon.ProgressWriter) ([]daemon.TeamSyncResult, error) {
				return []daemon.TeamSyncResult{{TeamID: "t1", TeamName: "alpha", Status: "error", Error: "pull failed in /Users/x/" + secret}}, nil
			}},
			team:   "alpha",
			kind:   "other",
			detail: "team context sync failed",
		},
		{
			name: "the daemon being down outranks the convergence it broke",
			converge: func(_ context.Context, _ string, result *SyncResult) error {
				result.Convergence.Status = "failed"
				return errors.New("owning Team Context was not reported by transport")
			},
			kind:   "daemon",
			detail: "ledger sync failed",
		},
		{
			name:   "convergence past its deadline",
			daemon: &testutil.MockService{},
			converge: func(_ context.Context, _ string, result *SyncResult) error {
				result.Convergence.Status = "pending"
				return fmt.Errorf("team context convergence pending: %w", context.DeadlineExceeded)
			},
			kind:   "timeout",
			detail: "team context convergence pending",
		},
	}
	for _, tt := range tests {
		for _, jsonOutput := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%t", tt.name, jsonOutput), func(t *testing.T) {
				startSyncTestDaemon(t, tt.daemon)
				if tt.status != nil {
					prev := ledgerStatusForSync
					ledgerStatusForSync = tt.status
					t.Cleanup(func() { ledgerStatusForSync = prev })
				}
				stubSyncRuntime(t, func(bool) error { return nil },
					syncViaDaemon, syncTeamContext, syncAllTeamContexts, tt.converge)

				cmd, _ := newSyncRuntimeTestCommand(t)
				if tt.team != "" {
					require.NoError(t, cmd.Flags().Set("team", tt.team))
				}
				if jsonOutput {
					require.NoError(t, cmd.Flags().Set("json", "true"))
				}
				err := runSync(cmd, nil)
				require.Error(t, err)
				assert.Equal(t, jsonOutput, cli.IsSilent(err), "--json prints its own result; text mode prints the error")

				props := postHogCommandProps(&cli.Context{Cmd: cmd, Err: err, CommandStartTime: time.Now()}, "sync", 1)
				assert.Equal(t, tt.kind, props["error_kind"])
				assert.Equal(t, tt.detail, props["error_detail"])
				sent, merr := json.Marshal(props)
				require.NoError(t, merr)
				assert.NotContains(t, string(sent), secret)
			})
		}
	}
}

// startSyncTestDaemon serves svc over the real IPC socket for the test's
// working directory. With svc nil nothing listens, so a dial fails the way it
// does when the daemon is down.
func startSyncTestDaemon(t *testing.T, svc *testutil.MockService) {
	t.Helper()
	t.Chdir(t.TempDir())                                // not inside this repository: no real Ledger or Team Context
	runtimeDir, err := os.MkdirTemp("/tmp", "ox-sync-") // short: macOS caps socket paths at 104 bytes
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("OX_NO_DAEMON", "1") // never let a sync path start a real daemon
	if svc == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	server := daemon.NewServerWithService(slog.New(slog.DiscardHandler), svc)
	done := make(chan error, 1)
	go func() { done <- server.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, daemon.IsRunning, 2*time.Second, 10*time.Millisecond, "IPC server never answered ping")
}

// teamsSynced reports one synced team per name, each with its own ID.
func teamsSynced(names ...string) func(*daemon.ProgressWriter) ([]daemon.TeamSyncResult, error) {
	return func(*daemon.ProgressWriter) ([]daemon.TeamSyncResult, error) {
		results := make([]daemon.TeamSyncResult, len(names))
		for i, n := range names {
			results[i] = daemon.TeamSyncResult{TeamID: fmt.Sprintf("team_%d", i), TeamName: n, Status: "synced"}
		}
		return results, nil
	}
}
