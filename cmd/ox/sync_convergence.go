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

// teamConvergeFunc matches teamconverge.Converge's signature. executeSyncConvergence
// takes this as a function value — teamconverge.Converge has exactly one
// production implementation, so a func type lets tests substitute a closure
// directly instead of a throwaway stub type satisfying a one-method interface.
type teamConvergeFunc func(context.Context, teamconverge.Request) (teamconverge.Report, error)

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

	repository, err = executeSyncConvergence(ctx, teamconverge.Converge, projectRoot, *team, repository, teamconverge.ModeExplicit, true)
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

// executeSyncConvergence converges one repository. When persist is true, the
// resulting pending/failed/converged state is written to disk for the
// daemon's automatic retry budget (teamconverge.MaxAutomaticConvergenceAttempts);
// when false, the outcome is returned but never saved or cleared — see
// convergeAfterSessionBoundary, whose caller already discards the result and
// must not spend the shared retry budget on it.
func executeSyncConvergence(
	ctx context.Context,
	converge teamConvergeFunc,
	projectRoot string,
	team config.TeamContext,
	repository RepositoryConvergenceSyncResult,
	mode teamconverge.Mode,
	persist bool,
) (RepositoryConvergenceSyncResult, error) {
	// RuleAppliesToRepo/SkillAppliesToRepo (internal/teamdocs) fail closed on an
	// empty slug, so only a canonical origin-derived identity may gate a repos:
	// filter here — matching prime's discoverTeamContext (cmd/ox/agent_prime.go).
	// repotools.RepoSlug's directory-name fallback is retained for the display-only
	// RepositoryConvergenceSyncResult.Repository field above and must not reach a
	// repos: decision, or convergence and prime disagree about "this repository".
	repoSlug, _ := repotools.RepoSlugFromRemote(projectRoot)
	report, convergeErr := converge(ctx, teamconverge.Request{
		ProjectRoot: projectRoot,
		TeamPath:    team.Path,
		RepoSlug:    repoSlug,
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
		if !persist {
			return repository, fmt.Errorf("team context convergence pending: %w", convergeErr)
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
		if !persist {
			return repository, fmt.Errorf("team context convergence %s: %s", status, reason)
		}
		if _, saveErr := teamconverge.SavePending(projectRoot, status, report, reason); saveErr != nil {
			repository.Status = "failed"
			repository.Error = fmt.Sprintf("%s; persist retry state: %v", reason, saveErr)
			return repository, errors.New(repository.Error)
		}
		return repository, fmt.Errorf("team context convergence %s: %s", status, reason)
	}

	if persist {
		if err := teamconverge.ClearPending(projectRoot); err != nil {
			repository.Status = "failed"
			repository.Error = fmt.Sprintf("clear completed convergence state: %v", err)
			return repository, errors.New(repository.Error)
		}
	}
	repository.Status = "converged"
	return repository, nil
}

// convergeAfterSessionBoundary consumes pending Team Context work as soon as
// the last live AI coworker releases the repository. It is best effort:
// session completion must never fail because local projection is contended or
// broken, and its result is discarded (persist=false) rather than saved —
// this runs on ordinary session-stop churn in ModeAutomatic with a 250ms lock
// cap, and persisting a contended or incomplete outcome here would spend the
// daemon's bounded automatic-retry budget on work nobody is waiting on.
func convergeAfterSessionBoundary(projectRoot string) {
	team := config.FindRepoTeamContext(projectRoot)
	if team == nil || team.Path == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = executeSyncConvergence(ctx, teamconverge.Converge, projectRoot, *team, RepositoryConvergenceSyncResult{
		Repository: repotools.RepoSlug(projectRoot),
		TeamID:     team.TeamID,
		TeamName:   team.TeamName,
		TeamPath:   team.Path,
	}, teamconverge.ModeAutomatic, false)
}
