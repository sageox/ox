package recap

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/pkg/sessionsummary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Build (end-to-end wiring) ---
//
// Failure prevented: the miners are individually correct but Build wires
// the wrong window/scope into one of them, or forgets to populate a section
// of Output that the renderer/guidance contract depends on.

func TestBuild_LedgerAllTimeVsSessionsInWindow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	since := fixtureNow.Add(-7 * 24 * time.Hour)
	until := fixtureNow

	f.WriteSession("in-window", WithCreatedAt(fixtureNow.Add(-1*time.Hour)))
	f.WriteSession("outside-window", WithCreatedAt(fixtureNow.Add(-30*24*time.Hour)))

	out := Build(BuildInput{
		LedgerPath: f.Ledger,
		Identity:   defaultIdentity(),
		Since:      since,
		Until:      until,
		Now:        fixtureNow,
	})

	assert.Equal(t, 1, out.Coverage.SessionsInWindow, "only the in-window session should count toward the window")
	assert.Equal(t, 2, out.Coverage.LedgerAllTime, "both sessions ever recorded should count toward all-time depth, regardless of window")
}

func TestBuild_ArtifactsWiredFromTracesAndTeamPath(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.WriteTeamDoc("principles.md", "# The Constitution\n\nClarity beats cleverness.\n")

	f.WriteSession("s1", WithCreatedAt(fixtureNow))
	f.WriteTrace("s1", contexttrace.Event{
		Type: contexttrace.EventProvided, Timestamp: fixtureNow.Format(time.RFC3339),
		Source: contexttrace.SourceTeamDocs, Doc: "principles.md",
	})

	out := Build(BuildInput{
		LedgerPath: f.Ledger,
		TeamPath:   f.Team,
		Identity:   defaultIdentity(),
		Since:      fixtureNow.Add(-24 * time.Hour),
		Until:      fixtureNow.Add(24 * time.Hour),
		Now:        fixtureNow,
	})

	require.Len(t, out.ArtifactsReached, 1)
	assert.Equal(t, "The Constitution", out.ArtifactsReached[0].Title)
}

func TestBuild_GuidanceAndHintsAlwaysSet(t *testing.T) {
	t.Parallel()
	f := newFixture(t) // completely empty ledger

	out := Build(BuildInput{
		LedgerPath: f.Ledger,
		Identity:   defaultIdentity(),
		Since:      fixtureNow.Add(-24 * time.Hour),
		Until:      fixtureNow,
		Now:        fixtureNow,
	})

	assert.NotEmpty(t, out.Guidance, "guidance must always be set — it's the load-bearing narration contract for the calling agent")
	require.NotNil(t, out.Hints)
	assert.NotEmpty(t, out.Hints.Drilldown)
	assert.NotEmpty(t, out.Hints.Verify)
	assert.Equal(t, "personal", out.Scope)
}

func TestBuild_ColdStart_NextActionsPointsAtPrime(t *testing.T) {
	t.Parallel()
	f := newFixture(t) // no sessions at all

	out := Build(BuildInput{
		LedgerPath: f.Ledger,
		Identity:   defaultIdentity(),
		Since:      fixtureNow.Add(-24 * time.Hour),
		Until:      fixtureNow,
		Now:        fixtureNow,
	})

	assert.Equal(t, 0, out.Coverage.LedgerAllTime)
	require.NotEmpty(t, out.NextActions)
	assert.Equal(t, "ox agent prime", out.NextActions[0].Action)
}

func TestBuild_DecisionsAndWorkWiredFromLedger(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.GitInit()

	f.WriteSession("s1", WithSessionID("ses_build01"), WithCreatedAt(fixtureNow), WithTitle("Shipped a fix"))
	f.WriteSummary("s1", sessionsummary.SummarizeResponse{
		AgentSummary: &sessionsummary.AgentSummary{
			Decisions: []sessionsummary.Decision{{What: "Use a fixed timestamp fixture"}},
		},
	})
	sha := f.GitCommit("Shipped a fix\n\nSageOx-Session: https://sageox.ai/c/ses_build01")

	out := Build(BuildInput{
		LedgerPath:  f.Ledger,
		ProjectRoot: f.Project,
		Identity:    defaultIdentity(),
		Since:       fixtureNow.Add(-24 * time.Hour),
		Until:       fixtureNow.Add(24 * time.Hour),
		Now:         fixtureNow,
	})

	require.Len(t, out.YourWork, 1)
	assert.Equal(t, "Shipped a fix", out.YourWork[0].Title)
	require.Len(t, out.YourWork[0].Commits, 1)
	assert.Contains(t, out.YourWork[0].Commits[0], sha[:7])

	require.Len(t, out.SettledDecisions, 1)
	assert.Equal(t, "Use a fixed timestamp fixture", out.SettledDecisions[0].What)
}

// --- nextActions (the branchy prescription logic) ---

func TestNextActions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		in         nextActionInput
		wantAction string // "" means expect nil/empty
		wantNone   bool
	}{
		{
			name:       "true cold start with no team context points at prime and then recording a discussion",
			in:         nextActionInput{hasLedger: false, team: teamBuilt{}},
			wantAction: "ox agent prime",
		},
		{
			name:       "solo with ledger but no artifacts and no team context invites recording without scolding",
			in:         nextActionInput{hasLedger: true, artifactCount: 0, team: teamBuilt{}},
			wantAction: "Record a discussion at sageox.ai, or invite a teammate",
		},
		{
			name:       "team context exists but never reached a session points at prime as the delivery moment",
			in:         nextActionInput{hasLedger: true, artifactCount: 0, team: teamBuilt{docCount: 3}},
			wantAction: "Run `ox agent prime` at the start of each session",
		},
		{
			name:       "window work with no captured decisions suggests recording them",
			in:         nextActionInput{hasLedger: true, hasWindowWork: true, artifactCount: 1, decisionCount: 0, planCount: 1, team: teamBuilt{docCount: 1}},
			wantAction: "Record decisions as you make them (score the session at stop, or `ox agent <id> session context-trace`)",
		},
		{
			name:       "window work with no enriched plans suggests plan mode",
			in:         nextActionInput{hasLedger: true, hasWindowWork: true, artifactCount: 1, decisionCount: 1, planCount: 0, team: teamBuilt{docCount: 1}},
			wantAction: "Draft in plan mode, then `ox plan enrich`",
		},
		{
			name: "everything present on both axes yields no actions",
			in: nextActionInput{
				hasLedger: true, hasWindowWork: true,
				artifactCount: 1, decisionCount: 1, planCount: 1,
				team: teamBuilt{docCount: 1},
			},
			wantNone: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := nextActions(tt.in)
			if tt.wantNone {
				assert.Empty(t, got)
				return
			}
			require.NotEmpty(t, got)
			assert.Equal(t, tt.wantAction, got[0].Action)
			assert.NotEmpty(t, got[0].Why, "every next action must explain the value it unlocks")
		})
	}
}

func TestNextActions_ColdStartWithPopulatedTeamSkipsSecondAction(t *testing.T) {
	t.Parallel()
	got := nextActions(nextActionInput{hasLedger: false, team: teamBuilt{docCount: 2}})
	require.Len(t, got, 1, "when team context is already populated, cold start should only prescribe priming — not also inviting a discussion")
	assert.Equal(t, "ox agent prime", got[0].Action)
}

// --- humaneWindow ---

func TestHumaneWindow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		since time.Time
		now   time.Time
		want  string
	}{
		{"30 days", now.Add(-30 * 24 * time.Hour), now, "last 30 days"},
		{"1 day rounds to last 24 hours", now.Add(-24 * time.Hour), now, "last 24 hours"},
		{"zero now falls back to absolute date", now.Add(-30 * 24 * time.Hour), time.Time{}, "since " + now.Add(-30*24*time.Hour).Format("2006-01-02")},
		{"since not before now falls back to absolute date", now, now, "since " + now.Format("2006-01-02")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, humaneWindow(tt.since, tt.now))
		})
	}
}

// --- sinceLabel ---

func TestSinceLabel(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		since time.Time
		now   time.Time
		want  string
	}{
		{"30 days", now.Add(-30 * 24 * time.Hour), now, "30.days"},
		{"sub-day rounds up to 1 day minimum", now.Add(-1 * time.Hour), now, "1.days"},
		{"zero now falls back to absolute date", now.Add(-30 * 24 * time.Hour), time.Time{}, now.Add(-30 * 24 * time.Hour).Format("2006-01-02")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, sinceLabel(tt.since, tt.now))
		})
	}
}

// --- itoa ---

func TestItoa(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{7, "7"},
		{42, "42"},
		{-5, "-5"},
		{12345, "12345"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, itoa(tt.in))
	}
}

// --- inWindowSessions ---

func TestInWindowSessions(t *testing.T) {
	t.Parallel()

	since := fixtureNow.Add(-24 * time.Hour)
	until := fixtureNow.Add(24 * time.Hour)
	facts := []SessionFacts{
		{Name: "in", CreatedAt: fixtureNow},
		{Name: "before", CreatedAt: fixtureNow.Add(-48 * time.Hour)},
		{Name: "after", CreatedAt: fixtureNow.Add(48 * time.Hour)},
	}
	got := inWindowSessions(facts, since, until)
	require.Len(t, got, 1)
	assert.Equal(t, "in", got[0].Name)
}
