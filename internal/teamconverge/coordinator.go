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

type Coordinator struct {
	discovery    Discovery
	handlers     map[ArtifactKind]Handler
	lockSnapshot bool
}

func New(discovery Discovery, handlers ...Handler) (*Coordinator, error) {
	if discovery == nil {
		return nil, fmt.Errorf("team context discovery is required")
	}
	c := &Coordinator{discovery: discovery, handlers: map[ArtifactKind]Handler{}}
	for _, handler := range handlers {
		if handler == nil {
			return nil, fmt.Errorf("nil Team Context convergence handler")
		}
		kind := handler.Kind()
		if kind == "" {
			return nil, fmt.Errorf("team context convergence handler has no artifact kind")
		}
		if _, exists := c.handlers[kind]; exists {
			return nil, fmt.Errorf("duplicate Team Context convergence handler for %s", kind)
		}
		c.handlers[kind] = handler
	}
	return c, nil
}

func (c *Coordinator) Converge(ctx context.Context, request Request) (Report, error) {
	if !c.lockSnapshot {
		return c.convergeLocked(ctx, request)
	}
	if request.TeamPath == "" {
		return newReport(request), fmt.Errorf("team context path is required")
	}

	// Pulls, Team Context publishers, and convergence share this per-clone
	// lease. Holding it through both discovery and handler byte loading makes
	// the report one immutable HEAD/worktree snapshot instead of a mix of two
	// commits. Automatic work only waits briefly; contention is durable pending
	// work, never a reason to stall the daemon scheduler.
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
		report, convergeErr = c.convergeLocked(ctx, request)
		return convergeErr
	})
	if err != nil && !acquired && gitutil.IsRepoLockBusy(err) {
		return report, &RetryableError{Err: fmt.Errorf("team context snapshot is busy: %w", err)}
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

func (c *Coordinator) convergeLocked(ctx context.Context, request Request) (Report, error) {
	report := newReport(request)
	snapshot, artifacts, err := c.discovery.Discover(ctx, request)
	if err != nil {
		return report, err
	}
	report.Snapshot = snapshot

	artifacts = append(artifacts, request.Additional...)
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

	var kinds []ArtifactKind
	for kind := range grouped {
		kinds = append(kinds, kind)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	for _, kind := range kinds {
		items := grouped[kind]
		handler := c.handlers[kind]
		if handler == nil {
			for _, artifact := range items {
				report.Outcomes = append(report.Outcomes, outcomeFor(snapshot, artifact, StateUnsupported, "",
					"no delivery handler supports this artifact type"))
			}
			continue
		}
		outcomes, handleErr := handler.Converge(ctx, request, snapshot, items)
		if handleErr != nil {
			state := StateError
			var retryable *RetryableError
			if errors.As(handleErr, &retryable) {
				state = StatePending
			}
			for _, artifact := range items {
				report.Outcomes = append(report.Outcomes, outcomeFor(snapshot, artifact, state, "", handleErr.Error()))
			}
			continue
		}
		report.Outcomes = append(report.Outcomes, validateHandlerOutcomes(snapshot, items, outcomes)...)
	}

	sort.Slice(report.Outcomes, func(i, j int) bool {
		if report.Outcomes[i].Kind != report.Outcomes[j].Kind {
			return report.Outcomes[i].Kind < report.Outcomes[j].Kind
		}
		return report.Outcomes[i].Name < report.Outcomes[j].Name
	})
	return report, nil
}

func artifactClaimKey(artifact Artifact) string {
	return strings.ToLower(string(artifact.Kind)) + "\x00" + strings.ToLower(artifact.Name)
}

func validateArtifact(artifact Artifact) error {
	switch artifact.Kind {
	case KindSkill, KindRule, KindContext, KindTool:
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
	switch artifact.Origin.Kind {
	case OriginLoose:
	case OriginPack:
		if artifact.Origin.Pack == "" {
			return fmt.Errorf("pack-owned artifact has no Pack identity")
		}
	default:
		return fmt.Errorf("artifact has unknown origin %q", artifact.Origin.Kind)
	}
	return nil
}

func validateHandlerOutcomes(snapshot Snapshot, artifacts []Artifact, outcomes []Outcome) []Outcome {
	byName := make(map[string][]Outcome, len(outcomes))
	for _, outcome := range outcomes {
		byName[outcome.Name] = append(byName[outcome.Name], outcome)
	}
	validated := make([]Outcome, 0, len(artifacts))
	for _, artifact := range artifacts {
		matches := byName[artifact.Name]
		if len(matches) != 1 {
			validated = append(validated, outcomeFor(snapshot, artifact, StateError, "",
				fmt.Sprintf("delivery handler returned %d outcomes; expected exactly one", len(matches))))
			continue
		}
		outcome := matches[0]
		outcome.Kind = artifact.Kind
		outcome.Name = artifact.Name
		outcome.SourcePath = artifact.SourcePath
		outcome.SourceCommit = snapshot.Commit
		outcome.Origin = artifact.Origin
		outcome.Required = artifact.Required
		validated = append(validated, outcome)
	}
	return validated
}

func outcomeFor(snapshot Snapshot, artifact Artifact, state OutcomeState, delivery, detail string) Outcome {
	return Outcome{
		Kind: artifact.Kind, Name: artifact.Name, SourcePath: artifact.SourcePath,
		SourceCommit: snapshot.Commit, Origin: artifact.Origin, State: state,
		Delivery: delivery, Detail: detail, Required: artifact.Required,
	}
}
