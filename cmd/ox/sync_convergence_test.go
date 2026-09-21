package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/teamconverge"
	"github.com/stretchr/testify/require"
)

type stubSyncConverger struct {
	report teamconverge.Report
	err    error
}

func (s stubSyncConverger) Converge(context.Context, teamconverge.Request) (teamconverge.Report, error) {
	return s.report, s.err
}

func TestExecuteSyncConvergence_PersistsFailedOutcome(t *testing.T) {
	projectRoot := t.TempDir()
	teamPath := filepath.Join(t.TempDir(), "team")
	report := teamconverge.Report{
		SchemaVersion: teamconverge.ReportSchemaVersion,
		ProjectRoot:   projectRoot,
		RepoSlug:      "acme/widget",
		Snapshot:      teamconverge.Snapshot{Path: teamPath, Commit: "abc123"},
		Outcomes: []teamconverge.Outcome{{
			Kind: teamconverge.KindTool, Name: "deploy", SourcePath: "agents/tools/deploy.yaml",
			SourceCommit: "abc123", Origin: teamconverge.Origin{Kind: teamconverge.OriginLoose},
			State: teamconverge.StateUnsupported, Required: true,
			Detail: "no delivery handler supports this artifact type",
		}},
	}

	result, err := executeSyncConvergence(context.Background(), stubSyncConverger{report: report}, projectRoot,
		config.TeamContext{TeamID: "team_acme", TeamName: "Acme", Path: teamPath},
		RepositoryConvergenceSyncResult{TeamID: "team_acme", TeamPath: teamPath})
	require.Error(t, err)
	require.Equal(t, "failed", result.Status)
	require.NotNil(t, result.Report)

	pending, loadErr := teamconverge.LoadPending(projectRoot)
	require.NoError(t, loadErr)
	require.NotNil(t, pending)
	require.Equal(t, teamconverge.PendingFailed, pending.Status)
	require.Equal(t, "abc123", pending.TeamCommit)
}

func TestExecuteSyncConvergence_PersistsRetryableErrorAsPending(t *testing.T) {
	projectRoot := t.TempDir()
	teamPath := filepath.Join(t.TempDir(), "team")
	report := teamconverge.Report{
		SchemaVersion: teamconverge.ReportSchemaVersion,
		Snapshot:      teamconverge.Snapshot{Path: teamPath, Commit: "def456"},
	}

	result, err := executeSyncConvergence(context.Background(), stubSyncConverger{
		report: report,
		err:    errors.New("projection lock is busy"),
	}, projectRoot, config.TeamContext{TeamID: "team_acme", Path: teamPath},
		RepositoryConvergenceSyncResult{TeamID: "team_acme", TeamPath: teamPath})
	require.Error(t, err)
	require.Equal(t, "pending", result.Status)

	pending, loadErr := teamconverge.LoadPending(projectRoot)
	require.NoError(t, loadErr)
	require.NotNil(t, pending)
	require.Equal(t, teamconverge.PendingRetry, pending.Status)
}

func TestExecuteSyncConvergence_ClearsPendingOnlyAfterVerifiedSuccess(t *testing.T) {
	projectRoot := t.TempDir()
	teamPath := filepath.Join(t.TempDir(), "team")
	failedReport := teamconverge.Report{
		Snapshot: teamconverge.Snapshot{Path: teamPath, Commit: "old"},
		Outcomes: []teamconverge.Outcome{{
			Kind: teamconverge.KindSkill, Name: "deploy", State: teamconverge.StatePending,
			Required: true, Origin: teamconverge.Origin{Kind: teamconverge.OriginLoose},
		}},
	}
	_, err := teamconverge.SavePending(projectRoot, teamconverge.PendingRetry, failedReport, "busy")
	require.NoError(t, err)

	successReport := teamconverge.Report{
		SchemaVersion: teamconverge.ReportSchemaVersion,
		Snapshot:      teamconverge.Snapshot{Path: teamPath, Commit: "new"},
		Outcomes: []teamconverge.Outcome{{
			Kind: teamconverge.KindRule, Name: "review", State: teamconverge.StateIndexed,
			Required: true, Origin: teamconverge.Origin{Kind: teamconverge.OriginLoose},
		}},
	}
	result, err := executeSyncConvergence(context.Background(), stubSyncConverger{report: successReport}, projectRoot,
		config.TeamContext{TeamID: "team_acme", Path: teamPath},
		RepositoryConvergenceSyncResult{TeamID: "team_acme", TeamPath: teamPath})
	require.NoError(t, err)
	require.Equal(t, "converged", result.Status)
	pending, loadErr := teamconverge.LoadPending(projectRoot)
	require.NoError(t, loadErr)
	require.Nil(t, pending)
}

func TestSyncResult_SeparatesTransportAndConvergence(t *testing.T) {
	result := SyncResult{
		SchemaVersion: syncResultSchemaVersion,
		Success:       false,
		Mode:          "daemon",
		Transport: SyncTransportResult{
			Status: "synced",
			Ledger: &SyncLedgerResult{Status: "synced"},
			TeamContexts: []TeamContextSyncResult{{
				TeamID: "team_acme", TeamName: "Acme", Path: "/team", Status: "synced",
			}},
		},
		Convergence: SyncConvergenceResult{
			Status: "failed",
			Repositories: []RepositoryConvergenceSyncResult{{
				Repository: "acme/widget", TeamID: "team_acme", TeamPath: "/team",
				Status: "failed", Error: "skill/deploy conflicts with repository content",
			}},
		},
		Error: "Team Context convergence failed",
	}

	data, err := json.Marshal(result)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, float64(syncResultSchemaVersion), decoded["schema_version"])
	require.Contains(t, decoded, "transport")
	require.Contains(t, decoded, "convergence")
	require.NotContains(t, decoded, "ledger", "transport fields must not masquerade as the overall result")
	require.False(t, decoded["success"].(bool), "failed convergence cannot be fully synced")

	var human bytes.Buffer
	require.NoError(t, writeSyncResultText(&human, result))
	require.Contains(t, human.String(), "Transport: synced")
	require.Contains(t, human.String(), "Convergence: failed")
	require.Contains(t, human.String(), "acme/widget: failed")
}

func TestFindTeamTransport_MatchesOwningTeamByPath(t *testing.T) {
	results := []TeamContextSyncResult{{
		// A slug selector is retained in TeamID by the existing wire contract;
		// the canonical path still proves this is the owning Team Context.
		TeamID: "acme", Path: "/contexts/team_acme", Status: "synced",
	}}
	got := findTeamTransport(results, config.TeamContext{TeamID: "team_acme", Path: "/contexts/team_acme"})
	require.NotNil(t, got)
	require.Equal(t, "synced", got.Status)
}

func TestSyncHelp_ExplainsAutomationAndPackBoundary(t *testing.T) {
	require.Contains(t, syncCmd.Short, "rarely needed")
	require.Contains(t, syncCmd.Long, "Pack-managed and hand-authored")
	require.Contains(t, syncCmd.Long, "does not check the Pack Catalog")
}
