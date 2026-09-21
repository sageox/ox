package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
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
		RepositoryConvergenceSyncResult{TeamID: "team_acme", TeamPath: teamPath}, teamconverge.ModeExplicit)
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
		RepositoryConvergenceSyncResult{TeamID: "team_acme", TeamPath: teamPath}, teamconverge.ModeExplicit)
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
		RepositoryConvergenceSyncResult{TeamID: "team_acme", TeamPath: teamPath}, teamconverge.ModeExplicit)
	require.NoError(t, err)
	require.Equal(t, "converged", result.Status)
	pending, loadErr := teamconverge.LoadPending(projectRoot)
	require.NoError(t, loadErr)
	require.Nil(t, pending)
}

func TestExecuteSyncConvergence_ReportsPersistenceFailures(t *testing.T) {
	team := config.TeamContext{TeamID: "team_acme", Path: "/team"}
	projectFile := filepath.Join(t.TempDir(), "project-file")
	require.NoError(t, os.WriteFile(projectFile, []byte("not a directory"), 0o600))

	t.Run("retry state", func(t *testing.T) {
		result, err := executeSyncConvergence(context.Background(), stubSyncConverger{
			err: errors.New("busy"),
		}, projectFile, team, RepositoryConvergenceSyncResult{}, teamconverge.ModeExplicit)
		require.ErrorContains(t, err, "persist retry state")
		require.Equal(t, "failed", result.Status)
		require.NotNil(t, result.Report)
		require.Equal(t, team.Path, result.Report.Snapshot.Path)
	})

	t.Run("failed outcome state", func(t *testing.T) {
		report := teamconverge.Report{Outcomes: []teamconverge.Outcome{{
			Kind: teamconverge.KindTool, Name: "deploy", State: teamconverge.StateUnsupported, Required: true,
		}}}
		result, err := executeSyncConvergence(context.Background(), stubSyncConverger{report: report},
			projectFile, team, RepositoryConvergenceSyncResult{}, teamconverge.ModeExplicit)
		require.ErrorContains(t, err, "persist retry state")
		require.Equal(t, "failed", result.Status)
	})

	t.Run("completed state cannot be cleared", func(t *testing.T) {
		project := t.TempDir()
		pendingPath := teamconverge.PendingPath(project)
		require.NoError(t, os.MkdirAll(pendingPath, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(pendingPath, "child"), []byte("x"), 0o600))
		result, err := executeSyncConvergence(context.Background(), stubSyncConverger{}, project,
			team, RepositoryConvergenceSyncResult{}, teamconverge.ModeExplicit)
		require.ErrorContains(t, err, "clear completed convergence state")
		require.Equal(t, "failed", result.Status)
	})
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

func TestFindTeamTransport_FallsBackToTeamIDAndReportsMisses(t *testing.T) {
	results := []TeamContextSyncResult{{TeamID: "team_acme", Status: "skipped"}}
	got := findTeamTransport(results, config.TeamContext{TeamID: "team_acme", Path: "/different/path"})
	require.NotNil(t, got)
	require.Equal(t, "skipped", got.Status)
	require.Nil(t, findTeamTransport(results, config.TeamContext{TeamID: "team_other"}))
}

func TestRunSyncConvergence_RepositoryAndTransportBoundaries(t *testing.T) {
	t.Run("outside initialized repository", func(t *testing.T) {
		t.Chdir(t.TempDir())
		result := SyncResult{}
		require.NoError(t, runSyncConvergence(context.Background(), "", &result))
		require.Equal(t, "skipped", result.Convergence.Status)
		require.Contains(t, result.Convergence.Detail, "not inside")
	})

	t.Run("initialized repository without team context", func(t *testing.T) {
		repo := t.TempDir()
		gitInitRepo(t, repo)
		require.NoError(t, config.SaveProjectConfig(repo, &config.ProjectConfig{
			ProjectID: "proj_no_team", RepoID: "repo_no_team",
		}))
		t.Chdir(repo)
		result := SyncResult{}
		require.NoError(t, runSyncConvergence(context.Background(), "", &result))
		require.Equal(t, "skipped", result.Convergence.Status)
		require.Contains(t, result.Convergence.Detail, "no local Team Context")
	})

	t.Run("targeted sync for another team", func(t *testing.T) {
		repo, _ := stageTeamPublishRepo(t)
		t.Chdir(repo)
		result := SyncResult{}
		require.NoError(t, runSyncConvergence(context.Background(), "team_other", &result))
		require.Equal(t, "skipped", result.Convergence.Status)
		require.Contains(t, result.Convergence.Detail, "does not own")
	})

	t.Run("default sync requires owning transport result", func(t *testing.T) {
		repo, _ := stageTeamPublishRepo(t)
		t.Chdir(repo)
		result := SyncResult{}
		err := runSyncConvergence(context.Background(), "", &result)
		require.ErrorContains(t, err, "not reported by transport")
		require.Equal(t, "failed", result.Convergence.Status)
		require.Len(t, result.Convergence.Repositories, 1)
		require.Equal(t, "failed", result.Convergence.Repositories[0].Status)
	})

	t.Run("non-ready owning transport blocks projection", func(t *testing.T) {
		repo, team := stageTeamPublishRepo(t)
		t.Chdir(repo)
		result := SyncResult{Transport: SyncTransportResult{TeamContexts: []TeamContextSyncResult{{
			TeamID: "team_publish_test", Path: team, Status: "cloning",
		}}}}
		err := runSyncConvergence(context.Background(), "", &result)
		require.ErrorContains(t, err, "transport is cloning")
		require.Equal(t, "failed", result.Convergence.Status)
	})

	t.Run("ready owning transport converges", func(t *testing.T) {
		repo, team := stageTeamPublishRepo(t)
		t.Chdir(repo)
		result := SyncResult{Transport: SyncTransportResult{TeamContexts: []TeamContextSyncResult{{
			TeamID: "team_publish_test", TeamName: "Publish Test Team", Path: team, Status: "synced",
		}}}}
		require.NoError(t, runSyncConvergence(context.Background(), "", &result))
		require.Equal(t, "converged", result.Convergence.Status)
		require.Len(t, result.Convergence.Repositories, 1)
		require.Equal(t, "converged", result.Convergence.Repositories[0].Status)
		require.Equal(t, team, result.Convergence.Repositories[0].TeamPath)
	})
}

func TestSyncHelp_ExplainsAutomationAndPackBoundary(t *testing.T) {
	require.Contains(t, syncCmd.Short, "rarely needed")
	require.Contains(t, syncCmd.Long, "Pack-managed and hand-authored")
	require.Contains(t, syncCmd.Long, "does not check the Pack Catalog")
}

func TestConvergeAfterSessionBoundary_AppliesPendingTeamContent(t *testing.T) {
	repo, _ := stageTeamPublishRepo(t)
	stageLocalPublishableSkill(t, repo, teamPublishSkill)
	_, err := publishRepoSkillsToTeam(repo, []string{teamPublishSkill})
	require.NoError(t, err)

	installed := filepath.Join(repo, ".claude", "skills", "sageox-team-"+teamPublishSkill, "SKILL.md")
	require.NoFileExists(t, installed)
	convergeAfterSessionBoundary(repo)
	require.FileExists(t, installed)
	pending, err := teamconverge.LoadPending(repo)
	require.NoError(t, err)
	require.Nil(t, pending)
}
