package recap

import (
	"regexp"
	"strings"
	"testing"
	"time"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fullOutput returns a bundle with every section populated, so honesty/width
// audits exercise every render* function in one pass.
func fullOutput() *Output {
	return &Output{
		User:  "ryan-snodgrass",
		Scope: "personal",
		Since: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
		Coverage: Coverage{
			SessionsInWindow: 5,
			LedgerAllTime:    42,
			WithTraces:       5,
			TracesDehydrated: 2,
		},
		ArtifactsReached: []ArtifactReach{
			{
				Doc: "principles.md", Title: "The SageOx Constitution",
				Snippet:  "Clarity beats cleverness in every review.",
				Sessions: 3, SampleWork: []string{"Fixed the sync bug", "Refactored the daemon"},
			},
			{Doc: "glossary.md", Title: "Glossary", Sessions: 1},
		},
		SettledDecisions: []Decision{
			{What: "Use errgroup for fan-out", Owner: "ryan", Session: "Fixed the sync bug"},
		},
		PlansEnriched: []PlanEnriched{
			{Slug: "recap-feature", Topic: "Recap feature", Collisions: 1, PriorArt: 2},
		},
		YourWork: []WorkItem{
			{Session: "s1", Title: "Fixed the sync bug", Commits: []string{"abc1234 fix sync bug"}},
		},
		TeamContextBuilt: []TeamArtifact{
			{Doc: "onboarding.md", Title: "Onboarding Guide", Kind: "doc"},
			{Doc: "2026-06-01-alice", Title: "Roadmap sync", Kind: "discussion"},
		},
		NextActions: []NextAction{
			{Action: "Draft in plan mode, then `ox plan enrich`", Why: "Flags collisions before you write code."},
		},
		WindowLabel: "last 14 days",
	}
}

// ansiEscape matches any ANSI CSI escape sequence.
var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;]*[a-zA-Z]")

func stripANSI(s string) string {
	return ansiEscape.ReplaceAllString(s, "")
}

// --- Honesty audit ---
//
// This is the design invariant the whole package exists to enforce: never
// lead with a bare statistic about SageOx itself. Failure prevented: a
// future edit reintroducing a headline like "47 sessions primed" that reads
// as marketing rather than evidence.

func TestRenderHuman_NoLineStartsWithABareDigit(t *testing.T) {
	t.Parallel()

	out := fullOutput()
	// Every styled sub-line (e.g. a StyleDim-wrapped qualifier) opens with an
	// ANSI escape sequence that sits between the leading indent and the first
	// visible character. Checking the raw string would let a digit hide right
	// behind that escape and slip past the regex undetected — strip ANSI first
	// so the check inspects what a reader actually sees.
	rendered := stripANSI(RenderHuman(out, 80))

	digitStart := regexp.MustCompile(`^\s*[★·]?\s*\d`)
	for _, line := range strings.Split(rendered, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		assert.False(t, digitStart.MatchString(line), "line must not lead with a bare count: %q", line)
	}
}

func TestRenderHuman_ColdStartLeadsWithNextActionsProse(t *testing.T) {
	t.Parallel()

	out := &Output{
		Coverage:    Coverage{LedgerAllTime: 0},
		NextActions: []NextAction{{Action: "ox agent prime", Why: "Starts your searchable ledger."}},
		WindowLabel: "last 30 days",
	}
	rendered := RenderHuman(out, 80)
	stripped := stripANSI(rendered)

	assert.Contains(t, stripped, "value starts the moment you begin")
	assert.Contains(t, stripped, "Do this next:")
	assert.Contains(t, stripped, "ox agent prime")
}

func TestRenderHuman_SoloLedgerSectionRendersWithoutTeamArtifacts(t *testing.T) {
	t.Parallel()

	out := &Output{
		Coverage:    Coverage{LedgerAllTime: 12, SessionsInWindow: 3},
		WindowLabel: "last 30 days",
	}
	rendered := RenderHuman(out, 80)
	stripped := stripANSI(rendered)

	assert.Contains(t, stripped, "Your ledger is compounding your own memory.")
	assert.Contains(t, stripped, "12 recorded sessions")
	assert.NotContains(t, stripped, "Your team's knowledge has been reaching your work.", "with zero artifacts reached, the team-reach section must not render")
}

// --- Width discipline ---
//
// Measured with ANSI stripped, which is the visible width regardless of
// color mode: lipgloss v2's Style.Render() always emits truecolor escapes
// from RenderHuman's own perspective (there is no profile/NO_COLOR check at
// this layer — see the NO_COLOR note below), so stripANSI is what makes the
// column budget assertion meaningful here rather than measuring escape-code
// bytes as if they were visible characters.

func TestRenderHuman_RespectsRequestedWidth(t *testing.T) {
	t.Parallel()

	const width = 80
	rendered := RenderHuman(fullOutput(), width)

	for _, line := range strings.Split(rendered, "\n") {
		visible := lipgloss.Width(stripANSI(line))
		assert.LessOrEqualf(t, visible, width, "line exceeds %d columns: %q", width, line)
	}
}

func TestRenderHuman_ZeroOrNegativeWidthDefaultsTo80(t *testing.T) {
	t.Parallel()

	rendered := RenderHuman(fullOutput(), 0)
	for _, line := range strings.Split(rendered, "\n") {
		assert.LessOrEqual(t, lipgloss.Width(stripANSI(line)), 80)
	}
}

// --- NO_COLOR ---
//
// There is deliberately no "RenderHuman output has zero ESC bytes under
// NO_COLOR=1" test in this package. lipgloss v2's Style.Render() always
// emits ANSI regardless of NO_COLOR (see the comment on the global
// ansiStripper pipe in cmd/ox/main.go's init) — stripping happens by
// replacing os.Stdout with an ANSI-stripping pipe whenever output isn't a
// TTY, which is plumbing that lives in cmd/ox, not internal/recap. Asserting
// "no ESC bytes" against RenderHuman's raw return value would be testing a
// premise that is false by design and would need reverting the moment
// lipgloss's behavior here is confirmed (it was verified empirically while
// writing this suite). The end-to-end guarantee — a real `ox recap` run
// under NO_COLOR=1/non-TTY stays escape-free — is covered by the compiled
// binary in cmd/ox and the documented dogfood check, not a package test.

// --- Section presence ---

func TestRenderHuman_AllSectionsRenderWhenPopulated(t *testing.T) {
	t.Parallel()

	rendered := stripANSI(RenderHuman(fullOutput(), 80))

	assert.Contains(t, rendered, "The SageOx Constitution")
	assert.Contains(t, rendered, "Clarity beats cleverness")
	assert.Contains(t, rendered, "Use errgroup for fan-out")
	assert.Contains(t, rendered, "Recap feature")
	assert.Contains(t, rendered, "Fixed the sync bug")
	assert.Contains(t, rendered, "abc1234")
	assert.Contains(t, rendered, "Onboarding Guide")
	assert.Contains(t, rendered, "Roadmap sync")
	assert.Contains(t, rendered, "Draft in plan mode")
	assert.Contains(t, rendered, "session traces live in LFS")
}

func TestRenderHuman_EmptySectionsOmitted(t *testing.T) {
	t.Parallel()

	out := &Output{
		Coverage:    Coverage{LedgerAllTime: 3, SessionsInWindow: 1},
		WindowLabel: "last 30 days",
	}
	rendered := stripANSI(RenderHuman(out, 80))

	assert.NotContains(t, rendered, "Decisions you inherited")
	assert.NotContains(t, rendered, "Caught before you wrote code")
	assert.NotContains(t, rendered, "What you shipped")
	assert.NotContains(t, rendered, "What your team has built")
	assert.NotContains(t, rendered, "To get more value")
}

// --- countPhrase ---

func TestCountPhrase(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "1 session", countPhrase(1, "session", "sessions"))
	assert.Equal(t, "5 sessions", countPhrase(5, "session", "sessions"))
	assert.Equal(t, "0 sessions", countPhrase(0, "session", "sessions"))
}

// --- reachLead ---

func TestReachLead(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Was in your context during your work.", reachLead(ArtifactReach{Sessions: 1}))
	assert.Equal(t, "Was in your context during your work.", reachLead(ArtifactReach{Sessions: 0}))
	assert.Equal(t, "Was in your context across 3 of your sessions.", reachLead(ArtifactReach{Sessions: 3}))
}

// --- planCaught ---

func TestPlanCaught(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		p    PlanEnriched
		want string
	}{
		{"collisions only", PlanEnriched{Collisions: 1}, "1 collision with open work"},
		{"prior art only, plural", PlanEnriched{PriorArt: 2}, "2 prior-art matches"},
		{"expert routes only", PlanEnriched{ExpertRoutes: 1}, "1 expert route"},
		{"all three joined", PlanEnriched{Collisions: 1, PriorArt: 1, ExpertRoutes: 1}, "1 collision with open work, 1 prior-art match, 1 expert route"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, planCaught(tt.p))
		})
	}
}

// --- quote ---

func TestQuote(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "“hello”", quote("hello"))
}

// --- wrapVisible ---

func TestWrapVisible(t *testing.T) {
	t.Parallel()

	t.Run("empty text yields one empty line", func(t *testing.T) {
		t.Parallel()
		got := wrapVisible("", 40)
		require.Equal(t, []string{""}, got)
	})

	t.Run("short text fits on one line", func(t *testing.T) {
		t.Parallel()
		got := wrapVisible("hello world", 40)
		require.Equal(t, []string{"hello world"}, got)
	})

	t.Run("long text wraps at word boundaries", func(t *testing.T) {
		t.Parallel()
		got := wrapVisible("one two three four five six seven eight nine ten", 12)
		for _, line := range got {
			assert.LessOrEqual(t, lipgloss.Width(line), 12)
		}
		assert.Equal(t, "one two three four five six seven eight nine ten", strings.Join(got, " "))
	})
}

// --- windowLabel / headerScope ---

func TestWindowLabel(t *testing.T) {
	t.Parallel()

	withLabel := &Output{WindowLabel: "last 7 days"}
	assert.Equal(t, "last 7 days", windowLabel(withLabel))

	withoutLabel := &Output{Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	assert.Equal(t, "since 2026-01-01", windowLabel(withoutLabel))
}

func TestHeaderScope(t *testing.T) {
	t.Parallel()

	withUser := &Output{User: "ryan", WindowLabel: "last 7 days"}
	assert.Equal(t, "ryan · last 7 days", headerScope(withUser))

	withoutUser := &Output{WindowLabel: "last 7 days"}
	assert.Equal(t, "you · last 7 days", headerScope(withoutUser))
}
