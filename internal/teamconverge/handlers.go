package teamconverge

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/sageox/ox/internal/teamrules"
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
	if request.Mode == ModeAutomatic {
		live, err := session.HasLiveRecording(request.ProjectRoot)
		if err != nil {
			return nil, &RetryableError{Err: fmt.Errorf("inspect active sessions: %w", err)}
		}
		if live {
			return nil, &RetryableError{Err: errors.New("active AI coworker session keeps the current skill snapshot stable")}
		}
	}
	configured := config.FindRepoTeamContext(request.ProjectRoot)
	if configured == nil || configured.Path == "" {
		return nil, &settledError{State: StateError, Err: fmt.Errorf("no Team Context is configured for this repository")}
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
		return nil, &settledError{State: StateError, Err: fmt.Errorf("configured Team Context %s does not match snapshot %s", got, want)}
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
		return nil, &settledError{State: StateError, Err: errors.New("inspect mode cannot apply Team Skills")}
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
		return nil, &settledError{State: StateError, Err: fmt.Errorf("skill reconcile refused: %s", strings.Join(plan.Warnings, "; "))}
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

type RuleHandler struct{}

func (RuleHandler) Kind() ArtifactKind { return KindRule }

func (RuleHandler) Converge(ctx context.Context, request Request, snapshot Snapshot, artifacts []Artifact) ([]Outcome, error) {
	hasNative := teamrules.HasNativeProjections(request.ProjectRoot)
	if len(artifacts) == 0 && !hasNative {
		return []Outcome{}, nil
	}
	if request.Mode == ModeAutomatic {
		live, err := session.HasLiveRecording(request.ProjectRoot)
		if err != nil {
			return nil, &RetryableError{Err: fmt.Errorf("inspect active sessions: %w", err)}
		}
		if live {
			return nil, &RetryableError{Err: errors.New("active AI coworker session keeps the current rule snapshot stable")}
		}
	}
	if request.Mode == ModeInspect {
		return nil, &settledError{State: StateError, Err: errors.New("inspect mode cannot apply Team Rules")}
	}

	// An absent rules root is not an authoritative empty set. Team Context uses
	// sparse checkout; if neither agents/ nor the legacy coworkers/ parent is on
	// disk, sweeping native files would turn a partial checkout into retirement.
	if !teamdocs.AnyRuleRootOnDisk(snapshot.Path) {
		if len(artifacts) == 0 && hasNative {
			return nil, &RetryableError{Err: errors.New("rules are not materialized in Team Context; retaining existing native Team Rules")}
		}
		outcomes := make([]Outcome, 0, len(artifacts))
		for _, artifact := range artifacts {
			outcomes = append(outcomes, outcomeFor(snapshot, artifact, StatePending, "",
				"no rules directory is materialized in the Team Context"))
		}
		return outcomes, nil
	}

	published, err := teamdocs.PublishedRules(snapshot.Path)
	if err != nil {
		return nil, fmt.Errorf("read Team Rules for projection: %w", err)
	}
	byName := make(map[string]teamdocs.TeamRule, len(published))
	for _, rule := range published {
		byName[rule.Name] = rule
	}
	wanted := make([]teamdocs.TeamRule, 0, len(artifacts))
	for _, artifact := range artifacts {
		if rule, ok := byName[artifact.Name]; ok {
			wanted = append(wanted, rule)
		}
	}

	result, err := teamrules.Reconcile(ctx, request.ProjectRoot, wanted)
	if err != nil {
		if errors.Is(err, teamrules.ErrProjectionConflict) {
			return nil, &settledError{State: StateConflict, Err: err}
		}
		return nil, err
	}
	outcomes := make([]Outcome, 0, len(artifacts))
	for _, artifact := range artifacts {
		native := result.NativeAgents[artifact.Name]
		fallbacks := result.Fallbacks[artifact.Name]
		detailParts := make([]string, 0, len(fallbacks))
		for _, fallback := range fallbacks {
			detailParts = append(detailParts, fallback.Agent+": "+fallback.Reason)
		}
		if len(native) > 0 {
			outcomes = append(outcomes, outcomeFor(snapshot, artifact, StateApplied,
				"native-rule:"+strings.Join(native, ","), strings.Join(detailParts, "; ")))
			continue
		}
		delivery := "prime-index"
		detail := strings.Join(detailParts, "; ")
		if len(artifact.Globs) == 0 && artifact.Visibility == teamdocs.VisibilityAlways {
			delivery = "prime-inline"
			detail = strings.TrimSpace(detail + " ready for injection at the next session boundary")
		}
		outcomes = append(outcomes, outcomeFor(snapshot, artifact, StateIndexed, delivery, detail))
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
	coordinator, err := New(FilesystemDiscovery{}, SkillHandler{}, RuleHandler{}, NewDiscoveryHandler(KindContext))
	if coordinator != nil {
		coordinator.lockSnapshot = true
	}
	return coordinator, err
}
