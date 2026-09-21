package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/teamconverge"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

type failOnWrite struct {
	writesUntilFailure int
}

func (w *failOnWrite) Write(p []byte) (int, error) {
	w.writesUntilFailure--
	if w.writesUntilFailure <= 0 {
		return 0, io.ErrClosedPipe
	}
	return len(p), nil
}

func completeSyncResultForText() SyncResult {
	return SyncResult{
		Transport: SyncTransportResult{
			Status: "failed",
			Ledger: &SyncLedgerResult{Status: "error"},
			TeamContexts: []TeamContextSyncResult{
				{TeamID: "team_named", TeamName: "Named Team", Status: "synced"},
				{TeamID: "team_fallback", Status: "skipped"},
			},
			Error: "transport unavailable",
		},
		Convergence: SyncConvergenceResult{
			Status: "failed",
			Repositories: []RepositoryConvergenceSyncResult{
				{Repository: "acme/widget", Status: "failed", Error: "projection failed"},
				{TeamID: "team_fallback", Status: "converged"},
			},
			Detail: "retry after repairing the repository",
		},
	}
}

func TestWriteSyncResultText_RendersEveryFallbackAndDetail(t *testing.T) {
	var output bytes.Buffer
	require.NoError(t, writeSyncResultText(&output, completeSyncResultForText()))
	require.Contains(t, output.String(), "Ledger: error")
	require.Contains(t, output.String(), "Team Context Named Team: synced")
	require.Contains(t, output.String(), "Team Context team_fallback: skipped")
	require.Contains(t, output.String(), "acme/widget: failed")
	require.Contains(t, output.String(), "team_fallback: converged")
	require.Contains(t, output.String(), "projection failed")
	require.Contains(t, output.String(), "retry after repairing")
}

func TestWriteSyncResultText_PropagatesWriterFailureAtEverySection(t *testing.T) {
	result := completeSyncResultForText()
	for writeNumber := 1; writeNumber <= 10; writeNumber++ {
		writer := &failOnWrite{writesUntilFailure: writeNumber}
		require.ErrorIs(t, writeSyncResultText(writer, result), io.ErrClosedPipe,
			"write %d was not propagated", writeNumber)
	}

	result = SyncResult{Convergence: SyncConvergenceResult{
		Status: "converged",
		Repositories: []RepositoryConvergenceSyncResult{{
			Repository: "acme/widget",
			Status:     "converged",
			Report: &teamconverge.Report{
				Snapshot: teamconverge.Snapshot{Commit: "1234567890abcdef"},
			},
		}},
	}}
	// Transport, convergence, and repository labels consume three writes; the
	// fourth fails inside teamconverge.WriteText and must still reach the caller.
	require.ErrorIs(t, writeSyncResultText(&failOnWrite{writesUntilFailure: 4}, result), io.ErrClosedPipe)
}

func TestFinishSync_PreservesOperationAndWriterErrors(t *testing.T) {
	operationErr := errors.New("transport failed")

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(io.Discard)
	require.NoError(t, finishSync(cmd, completeSyncResultForText(), false, nil))
	require.ErrorIs(t, finishSync(cmd, completeSyncResultForText(), false, operationErr), operationErr)

	cmd.SetOut(&failOnWrite{writesUntilFailure: 1})
	require.ErrorIs(t, finishSync(cmd, completeSyncResultForText(), false, nil), io.ErrClosedPipe)
	require.ErrorIs(t, finishSync(cmd, completeSyncResultForText(), false, operationErr), operationErr,
		"rendering must not replace the operation's actionable error")

	output := captureStdoutForPlanCLI(t, func() {
		require.NoError(t, finishSync(cmd, SyncResult{Success: true}, true, nil))
	})
	require.Contains(t, output, `"success": true`)
	output = captureStdoutForPlanCLI(t, func() {
		require.ErrorIs(t, finishSync(cmd, SyncResult{Success: false}, true, operationErr), cli.ErrSilent)
	})
	require.Contains(t, output, `"success": false`)
}

func TestRunSync_OrchestratesEachTransportMode(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configure  func(*cobra.Command)
		wantLedger int
		wantTeam   int
		wantAll    int
		wantSelect string
	}{
		{name: "default", wantLedger: 1, wantAll: 1},
		{name: "one team", configure: func(cmd *cobra.Command) {
			require.NoError(t, cmd.Flags().Set("team", "team_acme"))
		}, wantTeam: 1, wantSelect: "team_acme"},
		{name: "all teams", configure: func(cmd *cobra.Command) {
			require.NoError(t, cmd.Flags().Set("all-teams", "true"))
		}, wantAll: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, output := newSyncRuntimeTestCommand(t)
			if tc.configure != nil {
				tc.configure(cmd)
			}
			ledgerCalls, teamCalls, allCalls := 0, 0, 0
			stubSyncRuntime(t,
				func(bool) error { return nil },
				func(_ context.Context, _ bool, result *SyncResult) error {
					ledgerCalls++
					result.Transport.Ledger = &SyncLedgerResult{Status: "synced"}
					return nil
				},
				func(_ context.Context, team string, _ bool, result *SyncResult) error {
					teamCalls++
					result.Transport.TeamContexts = append(result.Transport.TeamContexts,
						TeamContextSyncResult{TeamID: team, Status: "synced"})
					return nil
				},
				func(_ context.Context, _ bool, result *SyncResult) error {
					allCalls++
					result.Transport.TeamContexts = append(result.Transport.TeamContexts,
						TeamContextSyncResult{TeamID: "team_acme", Status: "synced"})
					return nil
				},
				func(_ context.Context, selected string, result *SyncResult) error {
					require.Equal(t, tc.wantSelect, selected)
					result.Convergence.Status = "converged"
					return nil
				},
			)

			require.NoError(t, runSync(cmd, nil))
			require.Equal(t, tc.wantLedger, ledgerCalls)
			require.Equal(t, tc.wantTeam, teamCalls)
			require.Equal(t, tc.wantAll, allCalls)
			require.Contains(t, output.String(), "Transport: synced")
			require.Contains(t, output.String(), "Convergence: converged")
		})
	}
}

func TestRunSync_ReportsStartupAndAggregatedFailures(t *testing.T) {
	t.Run("daemon startup", func(t *testing.T) {
		cmd, output := newSyncRuntimeTestCommand(t)
		startupErr := errors.New("daemon unavailable")
		stubSyncRuntime(t, func(bool) error { return startupErr }, nil, nil, nil, nil)
		require.ErrorIs(t, runSync(cmd, nil), startupErr)
		require.Contains(t, output.String(), "Transport: failed")
		require.Contains(t, output.String(), "transport unavailable")
	})

	t.Run("transport and convergence", func(t *testing.T) {
		cmd, output := newSyncRuntimeTestCommand(t)
		stubSyncRuntime(t,
			func(bool) error { return nil },
			func(context.Context, bool, *SyncResult) error { return errors.New("ledger broke") },
			nil,
			func(context.Context, bool, *SyncResult) error { return errors.New("team broke") },
			func(_ context.Context, _ string, result *SyncResult) error {
				result.Convergence.Status = "failed"
				result.Convergence.Detail = "projection broke"
				return errors.New("projection broke")
			},
		)
		err := runSync(cmd, nil)
		require.ErrorContains(t, err, "ledger broke")
		require.ErrorContains(t, err, "team broke")
		require.ErrorContains(t, err, "projection broke")
		require.Contains(t, output.String(), "Transport: failed")
		require.Contains(t, output.String(), "ledger broke; team broke")
		require.Contains(t, output.String(), "projection broke")
	})
}

func TestRunSync_PreservesInterruptFromEveryTransportBranch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*cobra.Command)
		ledger    func(context.Context, bool, *SyncResult) error
		team      func(context.Context, string, bool, *SyncResult) error
		all       func(context.Context, bool, *SyncResult) error
	}{
		{name: "one team", configure: func(cmd *cobra.Command) {
			require.NoError(t, cmd.Flags().Set("team", "team_acme"))
		}, team: func(context.Context, string, bool, *SyncResult) error { return tea.ErrInterrupted }},
		{name: "all teams", configure: func(cmd *cobra.Command) {
			require.NoError(t, cmd.Flags().Set("all-teams", "true"))
		}, all: func(context.Context, bool, *SyncResult) error { return tea.ErrInterrupted }},
		{name: "default ledger", ledger: func(context.Context, bool, *SyncResult) error { return tea.ErrInterrupted }},
		{name: "default team", ledger: func(context.Context, bool, *SyncResult) error { return nil },
			all: func(context.Context, bool, *SyncResult) error { return tea.ErrInterrupted }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, _ := newSyncRuntimeTestCommand(t)
			if tc.configure != nil {
				tc.configure(cmd)
			}
			stubSyncRuntime(t, func(bool) error { return nil }, tc.ledger, tc.team, tc.all,
				func(context.Context, string, *SyncResult) error {
					t.Fatal("convergence ran after an interrupt")
					return nil
				})
			require.ErrorIs(t, runSync(cmd, nil), tea.ErrInterrupted)
		})
	}
}

func newSyncRuntimeTestCommand(t *testing.T) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.Flags().Bool("read-only", false, "")
	cmd.Flags().String("repo", "", "")
	cmd.Flags().Duration("timeout", 30*time.Minute, "")
	cmd.Flags().Bool("check", false, "")
	cmd.Flags().String("team", "", "")
	cmd.Flags().Bool("all-teams", false, "")
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().String("remove-team", "", "")
	var output bytes.Buffer
	cmd.SetOut(&output)
	return cmd, &output
}

func stubSyncRuntime(
	t *testing.T,
	ensure func(bool) error,
	ledger func(context.Context, bool, *SyncResult) error,
	team func(context.Context, string, bool, *SyncResult) error,
	all func(context.Context, bool, *SyncResult) error,
	converge func(context.Context, string, *SyncResult) error,
) {
	t.Helper()
	oldEnsure, oldLedger, oldTeam := ensureDaemonForSync, syncLedgerForSync, syncTeamForSync
	oldAll, oldConverge := syncAllTeamsForSync, convergeRepositorySync
	t.Cleanup(func() {
		ensureDaemonForSync, syncLedgerForSync, syncTeamForSync = oldEnsure, oldLedger, oldTeam
		syncAllTeamsForSync, convergeRepositorySync = oldAll, oldConverge
	})
	if ensure == nil {
		ensure = func(bool) error { return nil }
	}
	if ledger == nil {
		ledger = func(context.Context, bool, *SyncResult) error { return nil }
	}
	if team == nil {
		team = func(context.Context, string, bool, *SyncResult) error { return nil }
	}
	if all == nil {
		all = func(context.Context, bool, *SyncResult) error { return nil }
	}
	if converge == nil {
		converge = func(context.Context, string, *SyncResult) error { return nil }
	}
	ensureDaemonForSync = ensure
	syncLedgerForSync = ledger
	syncTeamForSync = team
	syncAllTeamsForSync = all
	convergeRepositorySync = converge
}
