package teamconverge

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type staticDiscovery struct {
	snapshot  Snapshot
	artifacts []Artifact
	err       error
}

func (d staticDiscovery) Discover(context.Context, Request) (Snapshot, []Artifact, error) {
	return d.snapshot, append([]Artifact(nil), d.artifacts...), d.err
}

type echoHandler struct {
	kind     ArtifactKind
	state    OutcomeState
	delivery string
	err      error
	drop     bool
}

func (h echoHandler) Kind() ArtifactKind { return h.kind }

func (h echoHandler) Converge(_ context.Context, _ Request, snapshot Snapshot, artifacts []Artifact) ([]Outcome, error) {
	if h.err != nil {
		return nil, h.err
	}
	if h.drop {
		return nil, nil
	}
	out := make([]Outcome, 0, len(artifacts))
	for _, artifact := range artifacts {
		out = append(out, outcomeFor(snapshot, artifact, h.state, h.delivery, ""))
	}
	return out, nil
}

func TestCoordinator_PackAndLooseArtifactsUseTheSameHandler(t *testing.T) {
	snapshot := Snapshot{Path: "/team", Commit: "abc123"}
	discovery := staticDiscovery{snapshot: snapshot, artifacts: []Artifact{
		{Kind: KindRule, Name: "loose", SourcePath: "agents/rules/loose.md", Origin: Origin{Kind: OriginLoose}, Applicable: true, Required: true},
		{Kind: KindRule, Name: "packed", SourcePath: "agents/rules/packed.md", Origin: Origin{Kind: OriginPack, Pack: "secure-defaults", PackVersion: "1.2.0"}, Applicable: true, Required: true},
		{Kind: KindRule, Name: "other-repo", SourcePath: "agents/rules/other.md", Origin: Origin{Kind: OriginLoose}, FilterReason: "other repository"},
		{Kind: KindTool, Name: "github", SourcePath: "agents/tools/github.json", Origin: Origin{Kind: OriginPack, Pack: "github"}, Applicable: true, Required: true},
	}}
	coordinator, err := New(discovery, echoHandler{kind: KindRule, state: StateIndexed, delivery: "prime-index"})
	require.NoError(t, err)

	report, err := coordinator.Converge(context.Background(), Request{ProjectRoot: "/repo", RepoSlug: "api"})
	require.NoError(t, err)
	require.Equal(t, snapshot, report.Snapshot)
	require.Len(t, report.Outcomes, 4)

	byName := map[string]Outcome{}
	for _, outcome := range report.Outcomes {
		byName[outcome.Name] = outcome
		require.Equal(t, snapshot.Commit, outcome.SourceCommit)
	}
	require.Equal(t, StateIndexed, byName["loose"].State)
	require.Equal(t, StateIndexed, byName["packed"].State)
	require.Equal(t, OriginPack, byName["packed"].Origin.Kind)
	require.Equal(t, StateFiltered, byName["other-repo"].State)
	require.Equal(t, StateUnsupported, byName["github"].State)
	require.False(t, report.Converged(), "a required unsupported tool was reported as fully converged")
}

func TestCoordinator_FailsClosedOnHandlerGaps(t *testing.T) {
	artifact := Artifact{Kind: KindSkill, Name: "deploy", SourcePath: "agents/skills/deploy", Origin: Origin{Kind: OriginLoose}, Applicable: true, Required: true}
	tests := []struct {
		name    string
		handler Handler
		detail  string
	}{
		{name: "handler error", handler: echoHandler{kind: KindSkill, err: errors.New("apply failed")}, detail: "apply failed"},
		{name: "missing outcome", handler: echoHandler{kind: KindSkill, drop: true}, detail: "expected exactly one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coordinator, err := New(staticDiscovery{snapshot: Snapshot{Commit: "abc"}, artifacts: []Artifact{artifact}}, tt.handler)
			require.NoError(t, err)
			report, err := coordinator.Converge(context.Background(), Request{})
			require.NoError(t, err)
			require.Len(t, report.Outcomes, 1)
			require.Equal(t, StateError, report.Outcomes[0].State)
			require.Contains(t, report.Outcomes[0].Detail, tt.detail)
			require.False(t, report.Converged())
		})
	}
}

func TestCoordinator_DuplicateOwnershipIsAConflictBeforeDelivery(t *testing.T) {
	artifacts := []Artifact{
		{Kind: KindRule, Name: "security", SourcePath: "agents/rules/security.md", Origin: Origin{Kind: OriginLoose}, Applicable: true, Required: true},
		{Kind: KindRule, Name: "security", SourcePath: "packs/security.md", Origin: Origin{Kind: OriginPack, Pack: "secure"}, Applicable: true, Required: true},
	}
	coordinator, err := New(staticDiscovery{snapshot: Snapshot{Commit: "abc"}, artifacts: artifacts},
		echoHandler{kind: KindRule, state: StateIndexed})
	require.NoError(t, err)
	report, err := coordinator.Converge(context.Background(), Request{})
	require.NoError(t, err)
	require.Len(t, report.Outcomes, 1)
	require.Equal(t, StateConflict, report.Outcomes[0].State)
	require.False(t, report.Converged())
}

func TestCoordinator_OptionalUnsupportedArtifactDoesNotFailConvergence(t *testing.T) {
	artifact := Artifact{Kind: KindTool, Name: "optional", SourcePath: "agents/tools/optional.json", Origin: Origin{Kind: OriginLoose}, Applicable: true, Required: false}
	coordinator, err := New(staticDiscovery{snapshot: Snapshot{Commit: "abc"}, artifacts: []Artifact{artifact}})
	require.NoError(t, err)
	report, err := coordinator.Converge(context.Background(), Request{})
	require.NoError(t, err)
	require.True(t, report.Converged())
}

func TestCoordinator_RejectsUnsafeArtifactIdentityBeforeDelivery(t *testing.T) {
	artifacts := []Artifact{
		{Kind: KindRule, Name: "escape", SourcePath: "../outside.md", Origin: Origin{Kind: OriginLoose}, Applicable: true, Required: true},
		{Kind: KindRule, Name: "Security", SourcePath: "agents/rules/one.md", Origin: Origin{Kind: OriginLoose}, Applicable: true, Required: true},
		{Kind: KindRule, Name: "security", SourcePath: "agents/rules/two.md", Origin: Origin{Kind: OriginPack, Pack: "secure"}, Applicable: true, Required: true},
	}
	coordinator, err := New(staticDiscovery{snapshot: Snapshot{Commit: "abc"}, artifacts: artifacts},
		echoHandler{kind: KindRule, state: StateIndexed})
	require.NoError(t, err)
	report, err := coordinator.Converge(context.Background(), Request{})
	require.NoError(t, err)
	require.Len(t, report.Outcomes, 2)
	byState := map[OutcomeState]Outcome{}
	for _, outcome := range report.Outcomes {
		byState[outcome.State] = outcome
	}
	require.Contains(t, byState[StateError].Detail, "not normalized")
	require.Equal(t, "Security", byState[StateConflict].Name)
	require.False(t, report.Converged())
}

func TestWriteText_ExplainsOriginDeliveryAndFailure(t *testing.T) {
	report := Report{Snapshot: Snapshot{Commit: "0123456789abcdef"}, Outcomes: []Outcome{
		{Kind: KindRule, Name: "security", State: StateInjected, Delivery: "prime-inline", Origin: Origin{Kind: OriginPack, Pack: "secure", PackVersion: "1.2.0"}},
		{Kind: KindTool, Name: "github", State: StateUnsupported, Origin: Origin{Kind: OriginLoose}, Detail: "no handler"},
	}}
	var out bytes.Buffer
	require.NoError(t, WriteText(&out, report))
	require.Contains(t, out.String(), "0123456789ab")
	require.Contains(t, out.String(), "rule/security: injected via prime-inline (pack secure@1.2.0)")
	require.Contains(t, out.String(), "tool/github: unsupported (loose) — no handler")
}
