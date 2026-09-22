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

// stubConverge builds a teamConvergeFunc returning fixed results. executeSyncConvergence
// takes teamconverge.Converge as a function value (not a one-method interface), so
// a closure substitutes directly without a throwaway stub type.
func stubConverge(report teamconverge.Report, err error) teamConvergeFunc {
	return func(context.Context, teamconverge.Request) (teamconverge.Report, error) {
		return report, err
	}
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
			Kind: teamconverge.KindSkill, Name: "deploy", SourcePath: "agents/skills/deploy",
			SourceCommit: "abc123", Origin: teamconverge.Origin{Kind: teamconverge.OriginLoose},
			State: teamconverge.StateUnsupported, Required: true,
			Detail: "this repository has no native skill target",
		}},
	}

	result, err := executeSyncConvergence(context.Background(), stubConverge(report, nil), projectRoot,
		config.TeamContext{TeamID: "team_acme", TeamName: "Acme", Path: teamPath},
		RepositoryConvergenceSyncResult{TeamID: "team_acme", TeamPath: teamPath}, teamconverge.ModeExplicit, true)
	require.Error(t, err)
	require.Equal(t, "failed", result.Status)
	require.NotNil(t, result.Report)

	pending, loadErr := teamconverge.LoadPending(projectRoot)
	require.NoError(t, loadErr)
	require.NotNil(t, pending)
	require.Equal(t, teamconverge.PendingFailed, pending.Status)
	require.Equal(t, "abc123", pending.TeamCommit)
}

func TestExecuteSyncConvergence_PersistsBusyErrorAsPending(t *testing.T) {
	projectRoot := t.TempDir()
	teamPath := filepath.Join(t.TempDir(), "team")
	report := teamconverge.Report{
		SchemaVersion: teamconverge.ReportSchemaVersion,
		Snapshot:      teamconverge.Snapshot{Path: teamPath, Commit: "def456"},
	}

	result, err := executeSyncConvergence(context.Background(),
		stubConverge(report, errors.New("projection lock is busy")),
		projectRoot, config.TeamContext{TeamID: "team_acme", Path: teamPath},
		RepositoryConvergenceSyncResult{TeamID: "team_acme", TeamPath: teamPath}, teamconverge.ModeExplicit, true)
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
	result, err := executeSyncConvergence(context.Background(), stubConverge(successReport, nil), projectRoot,
		config.TeamContext{TeamID: "team_acme", Path: teamPath},
		RepositoryConvergenceSyncResult{TeamID: "team_acme", TeamPath: teamPath}, teamconverge.ModeExplicit, true)
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
		result, err := executeSyncConvergence(context.Background(),
			stubConverge(teamconverge.Report{}, errors.New("busy")),
			projectFile, team, RepositoryConvergenceSyncResult{}, teamconverge.ModeExplicit, true)
		require.ErrorContains(t, err, "persist retry state")
		require.Equal(t, "failed", result.Status)
		require.NotNil(t, result.Report)
		require.Equal(t, team.Path, result.Report.Snapshot.Path)
	})

	t.Run("failed outcome state", func(t *testing.T) {
		report := teamconverge.Report{Outcomes: []teamconverge.Outcome{{
			Kind: teamconverge.KindSkill, Name: "deploy", State: teamconverge.StateUnsupported, Required: true,
		}}}
		result, err := executeSyncConvergence(context.Background(), stubConverge(report, nil),
			projectFile, team, RepositoryConvergenceSyncResult{}, teamconverge.ModeExplicit, true)
		require.ErrorContains(t, err, "persist retry state")
		require.Equal(t, "failed", result.Status)
	})

	t.Run("completed state cannot be cleared", func(t *testing.T) {
		project := t.TempDir()
		pendingPath := teamconverge.PendingPath(project)
		require.NoError(t, os.MkdirAll(pendingPath, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(pendingPath, "child"), []byte("x"), 0o600))
		result, err := executeSyncConvergence(context.Background(), stubConverge(teamconverge.Report{}, nil), project,
			team, RepositoryConvergenceSyncResult{}, teamconverge.ModeExplicit, true)
		require.ErrorContains(t, err, "clear completed convergence state")
		require.Equal(t, "failed", result.Status)
	})
}

// TestExecuteSyncConvergence_SessionBoundaryDoesNotPersist covers the class of
// bug where a best-effort convergence call spends a shared, bounded resource
// on work nobody is waiting for: convergeAfterSessionBoundary always discards
// its result (cmd/ox/sync_convergence.go), so if executeSyncConvergence still
// wrote pending/failed state for it, ordinary session-stop churn would burn
// the daemon's automatic retry budget (teamconverge.MaxAutomaticConvergenceAttempts)
// for an outcome nobody reads. persist=false must leave no trace on disk,
// whether the call fails, reports incomplete, or fully converges.
func TestExecuteSyncConvergence_SessionBoundaryDoesNotPersist(t *testing.T) {
	team := config.TeamContext{TeamID: "team_acme", Path: "/team"}

	t.Run("error path", func(t *testing.T) {
		projectRoot := t.TempDir()
		_, err := executeSyncConvergence(context.Background(),
			stubConverge(teamconverge.Report{}, errors.New("busy")),
			projectRoot, team, RepositoryConvergenceSyncResult{}, teamconverge.ModeAutomatic, false)
		require.Error(t, err)
		pending, loadErr := teamconverge.LoadPending(projectRoot)
		require.NoError(t, loadErr)
		require.Nil(t, pending, "a discarded session-boundary error must not spend the retry budget")
	})

	t.Run("not-converged path", func(t *testing.T) {
		projectRoot := t.TempDir()
		report := teamconverge.Report{Outcomes: []teamconverge.Outcome{{
			Kind: teamconverge.KindSkill, Name: "deploy", State: teamconverge.StatePending, Required: true,
		}}}
		_, err := executeSyncConvergence(context.Background(), stubConverge(report, nil),
			projectRoot, team, RepositoryConvergenceSyncResult{}, teamconverge.ModeAutomatic, false)
		require.Error(t, err)
		pending, loadErr := teamconverge.LoadPending(projectRoot)
		require.NoError(t, loadErr)
		require.Nil(t, pending, "a discarded session-boundary pending outcome must not spend the retry budget")
	})

	t.Run("converged path leaves a pre-existing record alone", func(t *testing.T) {
		projectRoot := t.TempDir()
		stale := teamconverge.Report{Outcomes: []teamconverge.Outcome{{
			Kind: teamconverge.KindSkill, Name: "deploy", State: teamconverge.StatePending, Required: true,
		}}}
		_, err := teamconverge.SavePending(projectRoot, teamconverge.PendingRetry, stale, "stale")
		require.NoError(t, err)

		successReport := teamconverge.Report{Snapshot: teamconverge.Snapshot{Path: team.Path, Commit: "new"}}
		result, err := executeSyncConvergence(context.Background(), stubConverge(successReport, nil),
			projectRoot, team, RepositoryConvergenceSyncResult{}, teamconverge.ModeAutomatic, false)
		require.NoError(t, err)
		require.Equal(t, "converged", result.Status)
		pending, loadErr := teamconverge.LoadPending(projectRoot)
		require.NoError(t, loadErr)
		require.NotNil(t, pending, "persist=false must not clear a pre-existing pending record either")
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

// TestExecuteSyncConvergence_RepoSlugUsesCanonicalOriginNotDirectoryNameFallback
// covers the class of bug where convergence and prime disagree about "this
// repository": teamdocs.RuleAppliesToRepo/SkillAppliesToRepo fail closed on an
// empty repo slug, so only a canonical origin-derived identity may gate a
// repos: filter. repotools.RepoSlug's directory-name fallback has no
// relationship to the team's repos: naming, so if it reached
// teamconverge.Request.RepoSlug, a clone with no origin remote (or a worktree
// where the remote lookup fails) could natively project a rule that prime
// would exclude.
func TestExecuteSyncConvergence_RepoSlugUsesCanonicalOriginNotDirectoryNameFallback(t *testing.T) {
	repo := t.TempDir()
	gitInitRepo(t, repo) // deliberately no origin remote
	dirName := filepath.Base(repo)

	team := t.TempDir()
	gitInitRepo(t, team)
	require.NoError(t, os.MkdirAll(filepath.Join(team, "agents", "rules"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(team, "agents", "rules", "scoped.md"),
		[]byte("---\nname: scoped\ndescription: repo-scoped rule\nrepos: [\""+dirName+"\"]\nvisibility: always\n---\n\nBody.\n"), 0o644))
	gitOutput(t, team, "add", "-A")
	gitOutput(t, team, "commit", "-q", "-m", "add scoped rule")
	wireTeamContext(t, repo, team)

	result, err := executeSyncConvergence(context.Background(), teamconverge.Converge, repo,
		config.TeamContext{TeamID: "team_publish_test", TeamName: "Publish Test Team", Path: team},
		RepositoryConvergenceSyncResult{TeamID: "team_publish_test", TeamPath: team}, teamconverge.ModeExplicit, true)
	require.NoError(t, err)
	require.NotNil(t, result.Report)

	var found bool
	for _, outcome := range result.Report.Outcomes {
		if outcome.Name != "scoped" {
			continue
		}
		found = true
		require.Equal(t, teamconverge.StateFiltered, outcome.State,
			"a repos:-scoped rule matched the working directory's basename instead of the canonical origin identity")
	}
	require.True(t, found, "expected the scoped rule to appear in the convergence report")
}

// TestSyncHelp_NamesTheAddonCatalogBoundary pins the rule `ox sync --help`
// states: convergence delivers add-on-managed content, but never checks the
// catalog for new releases. That boundary is why there is no `ox addons sync`.
//
// This used to be a two-case table over the FEATURE_ADDONS gate. The flag is
// gone, so the paragraph is unconditional and the table had one row left.
func TestSyncHelp_NamesTheAddonCatalogBoundary(t *testing.T) {
	require.Contains(t, syncCmd.Short, "rarely needed")

	long := syncLong()
	// Both halves must survive: the paragraph never displaces the rest.
	require.Contains(t, long, "You should RARELY need this command")
	require.Contains(t, long, "The background daemon automatically")
	require.Contains(t, long, "The daemon syncs automatically on:")
	require.Contains(t, long, "ledger-read-sync.md")

	require.Contains(t, long, "Add-on-managed and hand-authored")
	require.Contains(t, long, "does not check the Add-on Catalog")
	require.Contains(t, long, "ox addons update")
}

// TestSyncHelp_RegisteredLongMatchesTheAssembler pins the registered command
// to what syncLong() produces. Cobra renders Long before PersistentPreRunE, so
// a command left holding gate-on help would advertise the catalog for one whole
// invocation before anything could correct it — and `make docs` generates
// docs/reference/sync.mdx from exactly this value.
func TestSyncHelp_RegisteredLongMatchesTheAssembler(t *testing.T) {
	require.Equal(t, syncLong(), syncCmd.Long)
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
