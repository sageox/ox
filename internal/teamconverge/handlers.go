package teamconverge

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// DiscoveryHandler records delivery through prime's existing discovery path.
// It does not copy rules or context into agent-native roots, preventing the same
// artifact from being delivered both natively and through prime.
type DiscoveryHandler struct {
	kind ArtifactKind
}

func NewDiscoveryHandler(kind ArtifactKind) DiscoveryHandler { return DiscoveryHandler{kind: kind} }

func (h DiscoveryHandler) Kind() ArtifactKind { return h.kind }

func (h DiscoveryHandler) Converge(_ context.Context, _ Request, snapshot Snapshot, artifacts []Artifact) ([]Outcome, error) {
	outcomes := make([]Outcome, 0, len(artifacts))
	for _, artifact := range artifacts {
		state := StateIndexed
		delivery := "prime-index"
		detail := ""
		if artifact.Kind == KindRule && artifact.Visibility == teamdocs.VisibilityAlways {
			delivery = "prime-inline"
			detail = "ready for injection at the next session boundary"
		}
		outcomes = append(outcomes, outcomeFor(snapshot, artifact, state, delivery, detail))
	}
	return outcomes, nil
}

type SkillHandler struct{}

func (SkillHandler) Kind() ArtifactKind { return KindSkill }

func (SkillHandler) Converge(_ context.Context, request Request, snapshot Snapshot, artifacts []Artifact) ([]Outcome, error) {
	configured := config.FindRepoTeamContext(request.ProjectRoot)
	if configured == nil || configured.Path == "" {
		return nil, fmt.Errorf("no Team Context is configured for this repository")
	}
	want, err := filepath.Abs(snapshot.Path)
	if err != nil {
		return nil, err
	}
	got, err := filepath.Abs(configured.Path)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(want) != filepath.Clean(got) {
		return nil, fmt.Errorf("configured Team Context %s does not match snapshot %s", got, want)
	}
	if _, _, selected := skillmanager.InstalledSource(request.ProjectRoot); !selected {
		outcomes := make([]Outcome, 0, len(artifacts))
		for _, artifact := range artifacts {
			outcomes = append(outcomes, outcomeFor(snapshot, artifact, StateUnsupported, "",
				"this repository has no native skill target; run `ox init`"))
		}
		return outcomes, nil
	}

	identity := func(desired skillmanager.DesiredSkills, targets []adapterprotocol.SkillTarget) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
		return desired, targets, nil
	}
	var plan *skillmanager.ReconcilePlan
	switch request.Mode {
	case ModeInspect:
		return nil, fmt.Errorf("inspect mode cannot apply Team Skills")
	case ModeExplicit:
		plan, err = skillmanager.ReconcileUpdate(request.ProjectRoot, version.Version, identity)
	default:
		plan, err = skillmanager.ReconcileUpdateNonBlocking(request.ProjectRoot, version.Version, identity)
	}
	if err != nil {
		if errors.Is(err, skillmanager.ErrApplyInProgress) {
			return nil, &RetryableError{Err: err}
		}
		return nil, err
	}
	if plan == nil {
		return nil, fmt.Errorf("skill reconcile returned no plan")
	}
	if len(plan.Warnings) > 0 {
		return nil, fmt.Errorf("skill reconcile refused: %s", strings.Join(plan.Warnings, "; "))
	}

	decisions := make(map[string]skillmanager.TeamSkillDecision, len(plan.TeamSkills))
	for _, decision := range plan.TeamSkills {
		decisions[decision.Name] = decision
	}
	outcomes := make([]Outcome, 0, len(artifacts))
	for _, artifact := range artifacts {
		decision, ok := decisions[artifact.Name]
		if !ok {
			outcomes = append(outcomes, outcomeFor(snapshot, artifact, StateError, "", "skill reconcile did not report this artifact"))
			continue
		}
		outcome := outcomeFor(snapshot, artifact, StateApplied, "native-skill", decision.Reason)
		outcome.InstalledAs = decision.InstalledAs
		switch {
		case skillConflicted(plan, decision.InstalledAs):
			outcome.State = StateConflict
			outcome.Detail = "projected skill conflicts with existing repository content"
		case decision.NeedsApprove:
			outcome.State = StatePendingApproval
		case decision.InstalledAs == "":
			outcome.State = StateUnsupported
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes, nil
}

func skillConflicted(plan *skillmanager.ReconcilePlan, installedAs string) bool {
	if installedAs == "" {
		return false
	}
	needle := "/skills/" + installedAs + "/"
	for _, conflict := range plan.Conflicts {
		if strings.Contains("/"+filepath.ToSlash(conflict.Path), needle) {
			return true
		}
	}
	return false
}

func NewDefault() (*Coordinator, error) {
	return New(FilesystemDiscovery{}, SkillHandler{}, NewDiscoveryHandler(KindRule), NewDiscoveryHandler(KindContext))
}
