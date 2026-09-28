package teamconverge

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
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

// leaseProbeDiscovery proves converge holds the per-clone git lease across
// discovery and delivery: it snapshots the team file once entering Discover,
// signals an external writer to attempt the same lease, and snapshots again
// after giving that writer time to run. Both reads must match if the lease
// actually excluded the writer.
type leaseProbeDiscovery struct {
	file            string
	entered         chan struct{}
	writerAttempted chan struct{}
	first           string
	second          string
}

func (d *leaseProbeDiscovery) Discover(context.Context, Request) (Snapshot, []Artifact, error) {
	first, err := os.ReadFile(d.file)
	if err != nil {
		return Snapshot{}, nil, err
	}
	d.first = string(first)
	close(d.entered)
	<-d.writerAttempted
	// Give the writer ample time to acquire the lease if convergence failed to
	// take it first. The writer is local filesystem I/O, so this is not timing
	// the lock itself; it only exposes a missing lock as a deterministic change.
	time.Sleep(50 * time.Millisecond)
	second, err := os.ReadFile(d.file)
	if err != nil {
		return Snapshot{}, nil, err
	}
	d.second = string(second)
	if d.first != d.second {
		return Snapshot{}, nil, errors.New("mixed Team Context snapshot")
	}
	// KindContext: indexForPrime echoes StateIndexed for any artifact
	// regardless of what is materialized on disk, so this fixture can prove
	// the lease without a real rules/skills layout under the probed file.
	return Snapshot{Path: filepath.Dir(d.file), Commit: "snapshot-a"}, []Artifact{{
		Kind: KindContext, Name: "security", SourcePath: "docs/security.md",
		Origin: Origin{Kind: OriginLoose}, Applicable: true, Required: true,
	}}, nil
}

func TestConvergeLocked_HoldsOneTeamContextSnapshotLeaseThroughDelivery(t *testing.T) {
	team := t.TempDir()
	file := filepath.Join(team, "security.md")
	require.NoError(t, os.WriteFile(file, []byte("snapshot-a"), 0o644))
	project := t.TempDir()
	wireHandlerTeamContext(t, project, team)
	discovery := &leaseProbeDiscovery{
		file: file, entered: make(chan struct{}), writerAttempted: make(chan struct{}),
	}

	writerDone := make(chan error, 1)
	go func() {
		<-discovery.entered
		close(discovery.writerAttempted)
		writerDone <- gitutil.WithRepoLock(context.Background(), team, func() error {
			return os.WriteFile(file, []byte("snapshot-b"), 0o644)
		})
	}()

	report, err := convergeLocked(context.Background(), Request{ProjectRoot: project, TeamPath: team, Mode: ModeExplicit}, discovery)
	require.NoError(t, err)
	require.True(t, report.Converged(), "%+v", report.Outcomes)
	require.Equal(t, "snapshot-a", discovery.first)
	require.Equal(t, "snapshot-a", discovery.second)
	require.NoError(t, <-writerDone)
	final, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "snapshot-b", string(final), "writer should proceed after convergence releases the lease")
}

func TestConvergeLocked_AutomaticSnapshotContentionIsRetryable(t *testing.T) {
	team := t.TempDir()
	discovery := staticDiscovery{snapshot: Snapshot{Path: team, Commit: "abc"}}

	held := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = gitutil.WithRepoLock(context.Background(), team, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	started := time.Now()
	// Lock contention is decided before Discover ever runs, so this never
	// reaches convergeSkills/convergeRules/indexForPrime — no project wiring
	// is needed for this Request.
	_, err := convergeLocked(context.Background(), Request{TeamPath: team, Mode: ModeAutomatic}, discovery)
	close(release)
	require.Error(t, err)
	require.Contains(t, err.Error(), "busy")
	require.Less(t, time.Since(started), time.Second)
}

func TestConvergeLocked_RequiresTeamPath(t *testing.T) {
	_, err := convergeLocked(context.Background(), Request{}, staticDiscovery{})
	require.ErrorContains(t, err, "team context path is required")
}

func TestConverge_PropagatesDiscoveryError(t *testing.T) {
	_, err := converge(context.Background(), Request{}, staticDiscovery{err: errors.New("discovery failed")})
	require.ErrorContains(t, err, "discovery failed")
}

func TestClassify_DefaultsUnknownErrorsToPendingAndHonorsSettledState(t *testing.T) {
	snapshot := Snapshot{Commit: "abc"}
	items := []Artifact{{Kind: KindSkill, Name: "deploy"}}

	t.Run("nil error returns the delivery outcomes unchanged", func(t *testing.T) {
		outcomes := []Outcome{{Kind: KindSkill, Name: "deploy", State: StateApplied}}
		require.Equal(t, outcomes, classify(snapshot, items, outcomes, nil))
	})
	t.Run("unclassified error defaults every item to pending", func(t *testing.T) {
		got := classify(snapshot, items, nil, errors.New("apply failed"))
		require.Len(t, got, 1)
		require.Equal(t, StatePending, got[0].State)
		require.Equal(t, "apply failed", got[0].Detail)
	})
	t.Run("settled error keeps its own state", func(t *testing.T) {
		got := classify(snapshot, items, nil, &settledError{State: StateConflict, Err: errors.New("collision")})
		require.Len(t, got, 1)
		require.Equal(t, StateConflict, got[0].State)
		require.Equal(t, "collision", got[0].Detail)
	})
}

// TestConverge_ClassifiesRealDeliveryFailureThroughSettledError proves the
// classify wiring end to end (not just the pure classify unit test above):
// a real convergeSkills settled failure — no Team Context configured — lands
// in the report as StateError with the failure's own detail.
func TestConverge_ClassifiesRealDeliveryFailureThroughSettledError(t *testing.T) {
	artifact := Artifact{Kind: KindSkill, Name: "deploy", SourcePath: "agents/skills/deploy", Origin: Origin{Kind: OriginLoose}, Applicable: true, Required: true}
	report, err := converge(context.Background(), Request{ProjectRoot: t.TempDir()},
		staticDiscovery{snapshot: Snapshot{Commit: "abc"}, artifacts: []Artifact{artifact}})
	require.NoError(t, err)
	require.Len(t, report.Outcomes, 1)
	require.Equal(t, StateError, report.Outcomes[0].State)
	require.Contains(t, report.Outcomes[0].Detail, "no Team Context is configured")
	require.False(t, report.Converged())
}

func TestConverge_DuplicateOwnershipIsAConflictBeforeDelivery(t *testing.T) {
	project, team := t.TempDir(), t.TempDir()
	wireHandlerTeamContext(t, project, team)
	artifacts := []Artifact{
		{Kind: KindRule, Name: "security", SourcePath: "agents/rules/security.md", Origin: Origin{Kind: OriginLoose}, Applicable: true, Required: true},
		{Kind: KindRule, Name: "security", SourcePath: "packs/security.md", Origin: Origin{Kind: OriginLoose}, Applicable: true, Required: true},
	}
	report, err := converge(context.Background(), Request{ProjectRoot: project},
		staticDiscovery{snapshot: Snapshot{Path: team, Commit: "abc"}, artifacts: artifacts})
	require.NoError(t, err)
	require.Len(t, report.Outcomes, 1)
	require.Equal(t, StateConflict, report.Outcomes[0].State)
	require.False(t, report.Converged())
}

func TestConverge_RejectsUnsafeArtifactIdentityBeforeDelivery(t *testing.T) {
	project, team := t.TempDir(), t.TempDir()
	wireHandlerTeamContext(t, project, team)
	artifacts := []Artifact{
		{Kind: KindRule, Name: "escape", SourcePath: "../outside.md", Origin: Origin{Kind: OriginLoose}, Applicable: true, Required: true},
		{Kind: KindRule, Name: "Security", SourcePath: "agents/rules/one.md", Origin: Origin{Kind: OriginLoose}, Applicable: true, Required: true},
		{Kind: KindRule, Name: "security", SourcePath: "agents/rules/two.md", Origin: Origin{Kind: OriginLoose}, Applicable: true, Required: true},
	}
	report, err := converge(context.Background(), Request{ProjectRoot: project},
		staticDiscovery{snapshot: Snapshot{Path: team, Commit: "abc"}, artifacts: artifacts})
	require.NoError(t, err)
	require.Len(t, report.Outcomes, 2)
	byState := map[OutcomeState]Outcome{}
	for _, outcome := range report.Outcomes {
		byState[outcome.State] = outcome
	}
	require.Contains(t, byState[StateError].Detail, "not normalized")
	require.Equal(t, "Security", byState[StateConflict].Name, "case-insensitive claim collision reports the first sorted name")
	require.False(t, report.Converged())
}

func TestConverge_RejectsEveryInvalidArtifactIdentity(t *testing.T) {
	project, team := t.TempDir(), t.TempDir()
	wireHandlerTeamContext(t, project, team)
	artifacts := []Artifact{
		{Kind: "unknown", Name: "x", SourcePath: "x", Origin: Origin{Kind: OriginLoose}, Applicable: true},
		{Kind: KindRule, Name: " x", SourcePath: "x", Origin: Origin{Kind: OriginLoose}, Applicable: true},
		{Kind: KindRule, Name: "unknown-origin", SourcePath: "x", Origin: Origin{Kind: "unknown"}, Applicable: true},
	}
	report, err := converge(context.Background(), Request{ProjectRoot: project},
		staticDiscovery{snapshot: Snapshot{Path: team, Commit: "abc"}, artifacts: artifacts})
	require.NoError(t, err)
	require.Len(t, report.Outcomes, len(artifacts))
	for _, outcome := range report.Outcomes {
		require.Equal(t, StateError, outcome.State)
	}
}

func TestReport_ConvergedTreatsOptionalUnsupportedAsSuccessButRequiredAsFailure(t *testing.T) {
	optional := Report{Outcomes: []Outcome{{State: StateUnsupported, Required: false}}}
	require.True(t, optional.Converged())
	required := Report{Outcomes: []Outcome{{State: StateUnsupported, Required: true}}}
	require.False(t, required.Converged())
}

func TestWriteText_ExplainsDeliveryAndFailure(t *testing.T) {
	report := Report{Snapshot: Snapshot{Commit: "0123456789abcdef"}, Outcomes: []Outcome{
		{Kind: KindRule, Name: "security", State: StateIndexed, Delivery: "prime-inline"},
		{Kind: KindSkill, Name: "deploy", State: StateUnsupported, Detail: "no native skill target"},
	}}
	var out bytes.Buffer
	require.NoError(t, WriteText(&out, report))
	require.Contains(t, out.String(), "0123456789ab")
	require.Contains(t, out.String(), "rule/security: indexed via prime-inline")
	require.Contains(t, out.String(), "skill/deploy: unsupported — no native skill target")
}
