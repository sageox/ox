package teamconverge

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/gitutil"
)

const automaticSnapshotLockWait = 250 * time.Millisecond

// Converge discovers this repository's applicable Team Context artifacts and
// delivers them through each kind's one production mechanism: Team Skills
// reconcile natively (convergeSkills), Team Rules project natively or fall
// back to prime (convergeRules), and Team Context docs index for prime
// (indexForPrime).
func Converge(ctx context.Context, request Request) (Report, error) {
	return convergeLocked(ctx, request, FilesystemDiscovery{})
}

// convergeLocked wraps converge with the per-clone git lease. Pulls, Team
// Context publishers, and convergence share this lease. Holding it through
// both discovery and delivery makes a report one immutable HEAD/worktree
// snapshot instead of a mix of two commits. Automatic work only waits
// briefly; contention is durable pending work, never a reason to stall the
// daemon scheduler.
//
// Split from converge so tests can exercise the lease against a fake
// Discovery without a real git Team Context, and so the pure validation and
// delivery-dispatch logic in converge can be tested without paying for a
// lock (or a TeamPath) at all.
func convergeLocked(ctx context.Context, request Request, discovery Discovery) (Report, error) {
	if request.TeamPath == "" {
		return newReport(request), fmt.Errorf("team context path is required")
	}

	lockCtx := ctx
	cancel := func() {}
	if request.Mode == ModeAutomatic {
		lockCtx, cancel = context.WithTimeout(ctx, automaticSnapshotLockWait)
	}
	defer cancel()

	report := newReport(request)
	acquired := false
	err := gitutil.WithRepoLock(lockCtx, request.TeamPath, func() error {
		acquired = true
		var convergeErr error
		report, convergeErr = converge(ctx, request, discovery)
		return convergeErr
	})
	if err != nil && !acquired && gitutil.IsRepoLockBusy(err) {
		return report, fmt.Errorf("team context snapshot is busy: %w", err)
	}
	return report, err
}

func newReport(request Request) Report {
	return Report{
		SchemaVersion: ReportSchemaVersion,
		ProjectRoot:   request.ProjectRoot,
		RepoSlug:      request.RepoSlug,
		Outcomes:      []Outcome{},
	}
}

// converge is the pure discover-validate-deliver core, with no git lease.
func converge(ctx context.Context, request Request, discovery Discovery) (Report, error) {
	report := newReport(request)
	snapshot, artifacts, err := discovery.Discover(ctx, request)
	if err != nil {
		return report, err
	}
	report.Snapshot = snapshot

	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].Kind != artifacts[j].Kind {
			return artifacts[i].Kind < artifacts[j].Kind
		}
		if artifacts[i].Name != artifacts[j].Name {
			return artifacts[i].Name < artifacts[j].Name
		}
		return artifacts[i].SourcePath < artifacts[j].SourcePath
	})

	claimCounts := map[string]int{}
	for _, artifact := range artifacts {
		claimCounts[artifactClaimKey(artifact)]++
	}
	conflictsReported := map[string]bool{}
	grouped := map[ArtifactKind][]Artifact{}
	for _, artifact := range artifacts {
		if validationErr := validateArtifact(artifact); validationErr != nil {
			report.Outcomes = append(report.Outcomes, outcomeFor(snapshot, artifact, StateError, "", validationErr.Error()))
			continue
		}
		claimKey := artifactClaimKey(artifact)
		if claimCounts[claimKey] > 1 {
			if !conflictsReported[claimKey] {
				report.Outcomes = append(report.Outcomes, outcomeFor(snapshot, artifact, StateConflict,
					"", fmt.Sprintf("multiple Team Context artifacts claim %s/%s", artifact.Kind, artifact.Name)))
				conflictsReported[claimKey] = true
			}
			continue
		}
		if !artifact.Applicable {
			report.Outcomes = append(report.Outcomes, outcomeFor(snapshot, artifact, StateFiltered, "", artifact.FilterReason))
		} else {
			grouped[artifact.Kind] = append(grouped[artifact.Kind], artifact)
		}
	}

	// Each kind's delivery function runs unconditionally, even with an empty
	// desired set: otherwise a deleted or newly-filtered Team Skill/Rule
	// leaves its last native projection on disk forever, since there would be
	// no applicable artifact to trigger retirement.
	//
	// Dispatch order is alphabetical by kind (context, rule, skill) to match
	// the prior handler-map coordinator, which iterated a sorted kindSet. When
	// two kinds both hard-abort on an empty desired set in the same call (e.g.
	// a live session blocks both Team Skills and Team Rules retirement), this
	// order decides which one's error reaches the caller.
	for _, kind := range [...]struct {
		kind ArtifactKind
		fn   func(context.Context, Request, Snapshot, []Artifact) ([]Outcome, error)
	}{
		{KindContext, indexForPrime},
		{KindRule, convergeRules},
		{KindSkill, convergeSkills},
	} {
		items := grouped[kind.kind]
		outcomes, handleErr := kind.fn(ctx, request, snapshot, items)
		if handleErr != nil && len(items) == 0 {
			// Empty desired state is still lifecycle work: retiring the last
			// rule or skill. With no artifact row to attach an error to,
			// swallowing it would falsely report convergence and discard the
			// only retry signal.
			return report, handleErr
		}
		report.Outcomes = append(report.Outcomes, classify(snapshot, items, outcomes, handleErr)...)
	}

	sort.Slice(report.Outcomes, func(i, j int) bool {
		if report.Outcomes[i].Kind != report.Outcomes[j].Kind {
			return report.Outcomes[i].Kind < report.Outcomes[j].Kind
		}
		return report.Outcomes[i].Name < report.Outcomes[j].Name
	})
	return report, nil
}

// classify turns one kind's delivery result into report outcomes. An
// unclassified error defaults every item to StatePending — recoverable by
// default — while a *settledError names its own terminal state.
func classify(snapshot Snapshot, items []Artifact, outcomes []Outcome, err error) []Outcome {
	if err == nil {
		return outcomes
	}
	state := StatePending
	var settled *settledError
	if errors.As(err, &settled) {
		state = settled.State
	}
	result := make([]Outcome, 0, len(items))
	for _, artifact := range items {
		result = append(result, outcomeFor(snapshot, artifact, state, "", err.Error()))
	}
	return result
}

func artifactClaimKey(artifact Artifact) string {
	return strings.ToLower(string(artifact.Kind)) + "\x00" + strings.ToLower(artifact.Name)
}

func validateArtifact(artifact Artifact) error {
	switch artifact.Kind {
	case KindSkill, KindRule, KindContext:
	default:
		return fmt.Errorf("unsupported artifact kind %q", artifact.Kind)
	}
	if strings.TrimSpace(artifact.Name) == "" || artifact.Name != strings.TrimSpace(artifact.Name) {
		return fmt.Errorf("artifact name must be non-empty and trimmed")
	}
	source := artifact.SourcePath
	if source == "" || strings.Contains(source, `\`) || strings.HasPrefix(source, "/") ||
		path.Clean(source) != source || source == "." || strings.HasPrefix(source, "../") {
		return fmt.Errorf("artifact source path %q is not normalized and Team Context-relative", source)
	}
	if artifact.Origin.Kind != OriginLoose {
		return fmt.Errorf("artifact has unknown origin %q", artifact.Origin.Kind)
	}
	return nil
}

func outcomeFor(snapshot Snapshot, artifact Artifact, state OutcomeState, delivery, detail string) Outcome {
	return Outcome{
		Kind: artifact.Kind, Name: artifact.Name, SourcePath: artifact.SourcePath,
		SourceCommit: snapshot.Commit, Origin: artifact.Origin, State: state,
		Delivery: delivery, Detail: detail, Required: artifact.Required,
	}
}
