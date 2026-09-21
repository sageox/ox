package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/repotools"
	"github.com/sageox/ox/internal/teamconverge"
)

type syncConverger interface {
	Converge(context.Context, teamconverge.Request) (teamconverge.Report, error)
}

// runSyncConvergence applies the current repository's owning Team Context only.
// Other teams visible to a coworker are transport-only: they do not own this
// repository and must never project skills or rules into it.
func runSyncConvergence(ctx context.Context, selectedTeam string, result *SyncResult) error {
	projectRoot, err := repotools.FindRepoRoot(repotools.VCSGit)
	if err != nil || !config.IsInitialized(projectRoot) {
		result.Convergence.Status = "skipped"
		result.Convergence.Detail = "not inside an initialized SageOx repository"
		return nil
	}

	team := config.FindRepoTeamContext(projectRoot)
	if team == nil || team.Path == "" {
		result.Convergence.Status = "skipped"
		result.Convergence.Detail = "this repository has no local Team Context"
		return nil
	}

	repository := RepositoryConvergenceSyncResult{
		Repository: repotools.RepoSlug(projectRoot),
		TeamID:     team.TeamID,
		TeamName:   team.TeamName,
		TeamPath:   team.Path,
	}
	transport := findTeamTransport(result.Transport.TeamContexts, *team)
	if transport == nil {
		if selectedTeam != "" {
			result.Convergence.Status = "skipped"
			result.Convergence.Detail = "the selected Team Context does not own this repository"
			return nil
		}
		repository.Status = "failed"
		repository.Error = "owning Team Context was not reported by transport"
		result.Convergence.Status = "failed"
		result.Convergence.Repositories = append(result.Convergence.Repositories, repository)
		return errors.New(repository.Error)
	}
	if transport.Status != "synced" && transport.Status != "skipped" {
		repository.Status = "failed"
		repository.Error = fmt.Sprintf("Team Context transport is %s", transport.Status)
		result.Convergence.Status = "failed"
		result.Convergence.Repositories = append(result.Convergence.Repositories, repository)
		return errors.New(repository.Error)
	}

	coordinator, err := teamconverge.NewDefault()
	if err != nil {
		repository.Status = "failed"
		repository.Error = err.Error()
		result.Convergence.Status = "failed"
		result.Convergence.Repositories = append(result.Convergence.Repositories, repository)
		return fmt.Errorf("initialize Team Context convergence: %w", err)
	}

	repository, err = executeSyncConvergence(ctx, coordinator, projectRoot, *team, repository, teamconverge.ModeExplicit)
	result.Convergence.Status = repository.Status
	result.Convergence.Repositories = append(result.Convergence.Repositories, repository)
	return err
}

func findTeamTransport(results []TeamContextSyncResult, team config.TeamContext) *TeamContextSyncResult {
	for i := range results {
		candidate := &results[i]
		if team.Path != "" && candidate.Path != "" && filepath.Clean(candidate.Path) == filepath.Clean(team.Path) {
			return candidate
		}
		if team.TeamID != "" && candidate.TeamID == team.TeamID {
			return candidate
		}
	}
	return nil
}

func executeSyncConvergence(
	ctx context.Context,
	coordinator syncConverger,
	projectRoot string,
	team config.TeamContext,
	repository RepositoryConvergenceSyncResult,
	mode teamconverge.Mode,
) (RepositoryConvergenceSyncResult, error) {
	report, convergeErr := coordinator.Converge(ctx, teamconverge.Request{
		ProjectRoot: projectRoot,
		TeamPath:    team.Path,
		RepoSlug:    repotools.RepoSlug(projectRoot),
		Mode:        mode,
	})
	repository.Report = &report
	if convergeErr != nil {
		repository.Status = "pending"
		repository.Error = convergeErr.Error()
		if report.Snapshot.Path == "" {
			report.Snapshot.Path = team.Path
			repository.Report = &report
		}
		if _, saveErr := teamconverge.SavePending(projectRoot, teamconverge.PendingRetry, report, convergeErr.Error()); saveErr != nil {
			repository.Status = "failed"
			repository.Error = fmt.Sprintf("%v; persist retry state: %v", convergeErr, saveErr)
			return repository, errors.New(repository.Error)
		}
		return repository, fmt.Errorf("team context convergence pending: %w", convergeErr)
	}

	if !report.Converged() {
		status := teamconverge.PendingStatusFor(report)
		reason := teamconverge.FailureReason(report)
		repository.Status = string(status)
		repository.Error = reason
		if _, saveErr := teamconverge.SavePending(projectRoot, status, report, reason); saveErr != nil {
			repository.Status = "failed"
			repository.Error = fmt.Sprintf("%s; persist retry state: %v", reason, saveErr)
			return repository, errors.New(repository.Error)
		}
		return repository, fmt.Errorf("team context convergence %s: %s", status, reason)
	}

	if err := teamconverge.ClearPending(projectRoot); err != nil {
		repository.Status = "failed"
		repository.Error = fmt.Sprintf("clear completed convergence state: %v", err)
		return repository, errors.New(repository.Error)
	}
	repository.Status = "converged"
	return repository, nil
}

// convergeAfterSessionBoundary consumes pending Team Context work as soon as
// the last live AI coworker releases the repository. It is best effort: session
// completion must never fail because local projection is contended or broken,
// and executeSyncConvergence persists either condition for the daemon retry.
func convergeAfterSessionBoundary(projectRoot string) {
	team := config.FindRepoTeamContext(projectRoot)
	if team == nil || team.Path == "" {
		return
	}
	coordinator, err := teamconverge.NewDefault()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = executeSyncConvergence(ctx, coordinator, projectRoot, *team, RepositoryConvergenceSyncResult{
		Repository: repotools.RepoSlug(projectRoot),
		TeamID:     team.TeamID,
		TeamName:   team.TeamName,
		TeamPath:   team.Path,
	}, teamconverge.ModeAutomatic)
}
