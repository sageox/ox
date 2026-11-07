package daemon

import (
	"context"
	"errors"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/repotools"
	"github.com/sageox/ox/internal/teamconverge"
	"github.com/sageox/ox/internal/teamdocs"
)

const automaticConvergenceTimeout = 30 * time.Second

// teamSkillsTouched reports whether a team-context pull changed anything under a
// skills root.
//
// It reads the roots from teamdocs.SkillRoots — the same list discovery walks —
// so the legacy coworkers/skills location cannot be found by one and ignored by
// the other. Paths come from `git diff --name-only`, which always emits
// forward slashes regardless of platform.
func teamSkillsTouched(changed []string) bool {
	for _, p := range changed {
		clean := path.Clean(strings.TrimSpace(p))
		for _, root := range teamdocs.SkillRoots {
			if strings.HasPrefix(clean, root+"/") {
				return true
			}
		}
	}
	return false
}

// teamArtifactsTouched is the coordinator trigger. Skills retain their helper
// above because its canonical/legacy root parity is independently load-bearing;
// every other typed artifact joins the same event path here.
func teamArtifactsTouched(changed []string) bool {
	if teamSkillsTouched(changed) {
		return true
	}
	roots := []string{
		"agents/rules", "coworkers/rules",
		"docs",
		// Reserved for the reviewed typed surfaces. A release that starts
		// discovering them will already receive the same event trigger.
		"agents/tools", "agents/profiles", "agents/commands",
	}
	for _, changedPath := range changed {
		clean := path.Clean(strings.TrimSpace(changedPath))
		for _, root := range roots {
			if strings.HasPrefix(clean, root+"/") {
				return true
			}
		}
	}
	return false
}

// reconcileTeamSkills retains its historical name for call-site stability, but
// now sends every typed Team Context artifact through the shared coordinator on
// the sync tick that noticed an edit.
//
// WHY HERE. Team content arrives on exactly one schedule — the team-context pull
// — and this function runs off the result of that pull rather than off a timer
// of its own, so there is no second cadence to keep in step and no polling on a
// repo that has not changed. It is EVENT-DRIVEN in the strict sense: changed
// carries the diff of the pull that just landed, so a tick where the team repo
// was already current does no work at all.
//
// The 30-minute `skills-inventory-drift` autofix check remains an anti-entropy
// floor for skill projections. Rules use a hybrid delivery policy: native-safe
// rules are mirrored into ignored agent rule roots, while prime remains the
// fallback for formats that cannot preserve scope. Context stays indexed.
//
// NON-BLOCKING ON PURPOSE. This runs on the sync scheduler's goroutine, between a
// pull and the rest of the cycle. ReconcileUpdateNonBlocking yields
// ErrApplyInProgress after ~100ms rather than polling the project lock for ten
// seconds. A later ticket persists contention as pending work; until then the
// typed error outcome prevents the cycle from claiming convergence.
//
// STALENESS IS GUARDED, NOT ASSUMED. A running daemon executes the binary it was
// started with, so its built-in catalog can be older than the one that last wrote
// this repository (see retireStaleDaemonsAfterUpgrade). Plan's downgrade guard
// refuses exactly that case and returns a plan with no actions, which is why
// reconciling team content from here cannot pin a repo to a previous release's
// CLI skills.
func (s *SyncScheduler) reconcileTeamSkills(changed []string) {
	repoRoot := s.config.ProjectRoot
	if repoRoot == "" {
		return
	}
	team := config.FindRepoTeamContext(repoRoot)
	if team == nil || team.Path == "" {
		s.logger.Debug("team artifacts changed but repo has no Team Context", "repo", repoRoot)
		return
	}
	pending, pendingErr := teamconverge.LoadPending(repoRoot)
	if pendingErr != nil {
		s.logger.Warn("team convergence state unreadable", "repo", repoRoot, "error", pendingErr)
	}
	retryIncomplete := teamconverge.AutomaticRetryAllowed(pending, team.Path)
	if !teamArtifactsTouched(changed) && !retryIncomplete {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), automaticConvergenceTimeout)
	defer cancel()
	// RuleAppliesToRepo/SkillAppliesToRepo (internal/teamdocs) fail closed on an
	// empty slug, so only a canonical origin-derived identity may gate a repos:
	// filter here — matching prime's discoverTeamContext (cmd/ox/agent_prime.go).
	// RepoSlug's directory-name fallback is display-only and must not reach a
	// repos: decision, or convergence and prime disagree about "this repository".
	repoSlug, _ := repotools.RepoSlugFromRemote(repoRoot)
	report, err := teamconverge.Converge(ctx, teamconverge.Request{
		ProjectRoot: repoRoot,
		TeamPath:    team.Path,
		RepoSlug:    repoSlug,
		Mode:        teamconverge.ModeAutomatic,
	})
	if err != nil {
		logConvergenceError(s.logger, repoRoot, err)
		retryReport := teamconverge.Report{Snapshot: teamconverge.Snapshot{Path: team.Path}}
		if pending != nil {
			retryReport.Snapshot.Commit = pending.TeamCommit
		}
		if _, saveErr := teamconverge.SavePending(repoRoot, teamconverge.PendingRetry, retryReport, err.Error()); saveErr != nil {
			s.logger.Warn("could not persist pending team convergence", "repo", repoRoot, "error", saveErr)
		}
		return
	}
	if report.Converged() {
		if clearErr := teamconverge.ClearPending(repoRoot); clearErr != nil {
			s.logger.Warn("could not clear completed team convergence state", "repo", repoRoot, "error", clearErr)
		}
	} else {
		status := teamconverge.PendingStatusFor(report)
		if _, saveErr := teamconverge.SavePending(repoRoot, status, report, teamconverge.FailureReason(report)); saveErr != nil {
			s.logger.Warn("could not persist incomplete team convergence", "repo", repoRoot, "error", saveErr)
		}
	}

	counts := logConvergenceOutcomes(s.logger, repoRoot, report)
	s.logger.Info("team context convergence completed",
		"repo", repoRoot,
		"team_commit", report.Snapshot.Commit,
		"artifacts", len(report.Outcomes),
		"applied", counts[teamconverge.StateApplied],
		"indexed", counts[teamconverge.StateIndexed],
		"pending", counts[teamconverge.StatePending],
		"pending_approval", counts[teamconverge.StatePendingApproval],
		"unsupported", counts[teamconverge.StateUnsupported],
		"conflicts", counts[teamconverge.StateConflict],
		"errors", counts[teamconverge.StateError],
		"converged", report.Converged())
}

// logConvergenceError reports a convergence pass that produced no per-artifact
// outcomes. A live AI coworker session holding the snapshot is the designed
// steady state while anyone works, so it is INFO; anything else is a failure
// worth a WARN (ox#1242).
func logConvergenceError(logger *slog.Logger, repoRoot string, err error) {
	if errors.Is(err, teamconverge.ErrSessionHold) {
		logger.Info("team context convergence deferred", "repo", repoRoot, "reason", teamconverge.DeferralActiveSession)
		return
	}
	logger.Warn("team context convergence failed", "repo", repoRoot, "error", err)
}

// logConvergenceOutcomes reports what did not converge and returns the
// per-state counts for the cycle summary.
//
// Error and conflict are per-artifact WARNs: each needs a human, usually for
// its own reason. Everything that shares one reason is ONE line naming the
// artifacts, because a repository with N team skills hits that reason N times
// in the same pass:
//
//   - pending because a live AI coworker session holds the snapshot is the
//     designed steady state, so it is a single INFO summary (ox#1242: 90 WARN
//     lines in one pass, burying the real warnings around them);
//   - pending for any other reason is a stall, so it stays a WARN;
//   - unsupported is a skip this repository cannot take, WARN once per reason.
//
// Pending-approval is a deferral awaiting a human decision and stays at Debug.
func logConvergenceOutcomes(logger *slog.Logger, repoRoot string, report teamconverge.Report) map[teamconverge.OutcomeState]int {
	counts := map[teamconverge.OutcomeState]int{}
	held := 0
	pending := newReasonGroups()
	skipped := newReasonGroups()
	for _, outcome := range report.Outcomes {
		counts[outcome.State]++
		switch outcome.State {
		case teamconverge.StateError, teamconverge.StateConflict:
			logger.Warn("team artifact not converged",
				"repo", repoRoot, "kind", outcome.Kind, "name", outcome.Name,
				"state", outcome.State, "detail", outcome.Detail, "commit", outcome.SourceCommit)
		case teamconverge.StatePending:
			if outcome.Deferral == teamconverge.DeferralActiveSession {
				held++
				continue
			}
			pending.add(outcome)
		case teamconverge.StateUnsupported:
			skipped.add(outcome)
		case teamconverge.StatePendingApproval:
			logger.Debug("team artifact deferred",
				"repo", repoRoot, "kind", outcome.Kind, "name", outcome.Name,
				"state", outcome.State, "detail", outcome.Detail, "commit", outcome.SourceCommit)
		}
	}
	if held > 0 {
		logger.Info("team_artifacts_pending", "repo", repoRoot, "count", held,
			"reason", teamconverge.DeferralActiveSession, "commit", report.Snapshot.Commit)
	}
	pending.log(logger, "team artifacts pending", repoRoot, report.Snapshot.Commit)
	skipped.log(logger, "team artifacts skipped", repoRoot, report.Snapshot.Commit)
	return counts
}

// reasonGroups collects artifact names by outcome detail, keeping first-seen
// order so log lines are stable across passes.
type reasonGroups struct {
	order []string
	names map[string][]string
}

func newReasonGroups() *reasonGroups { return &reasonGroups{names: map[string][]string{}} }

func (g *reasonGroups) add(outcome teamconverge.Outcome) {
	if _, seen := g.names[outcome.Detail]; !seen {
		g.order = append(g.order, outcome.Detail)
	}
	g.names[outcome.Detail] = append(g.names[outcome.Detail], string(outcome.Kind)+"/"+outcome.Name)
}

func (g *reasonGroups) log(logger *slog.Logger, msg, repoRoot, commit string) {
	for _, reason := range g.order {
		names := g.names[reason]
		logger.Warn(msg, "repo", repoRoot, "count", len(names), "reason", reason,
			"artifacts", strings.Join(names, ","), "commit", commit)
	}
}
