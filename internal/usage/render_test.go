package usage

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/session/contexttrace"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files")

// goldenReport is a busy session: every section populated.
func goldenReport(width int) Report {
	prime := primeEvent()
	prime.FullBytes, prime.Deferred = 14200, []string{"team-docs"}
	q := invoked("searched team memory", "query", 900, 7)
	q.Query, q.RefType = "auth token refresh", "query"
	events := []contexttrace.Event{
		prime,
		{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamContext, Docs: []string{"AGENTS.md"}},
		memoryProvided(),
		invoked("checked murmurs", "murmur list", 300, 3),
		invoked("checked murmurs", "murmur list", 300, 5),
		invoked("checked murmurs", "murmur list", 300, 9),
		q,
		invoked("pulled plan", "plan view", 680, 11),
		whisper(),
		selfReport("MEMORY.md"),
	}
	return Build(Input{
		TraceSource: TraceSourceSession, SessionName: "2026-10-23T10-00-dev-OxAb12",
		AgentID: "OxAb12", Harness: "codex", Recording: true, Turns: 31,
		ScoreCategory: "moderate", ProducedCommits: 2, Events: events, Width: width,
	})
}

// TestDisplay_Golden pins the dense layout at 80 and 120 columns. Failure
// prevented: the block both harnesses print verbatim drifts, wraps past the
// terminal, grows past one screen, or leaks color into an AI coworker's text.
func TestDisplay_Golden(t *testing.T) {
	for _, width := range []int{80, 120} {
		t.Run(filepath.Base(goldenName(width)), func(t *testing.T) {
			got := goldenReport(width).Display
			assert.NotContains(t, got, "\x1b")
			lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
			assert.LessOrEqual(t, len(lines), maxDisplayLines)
			for _, l := range lines {
				assert.LessOrEqual(t, ansi.StringWidth(l), width, "line overflows %d cols: %q", width, l)
			}

			path := goldenName(width)
			if *updateGolden {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
			}
			want, err := os.ReadFile(path)
			require.NoError(t, err, "run with -update to create %s", path)
			assert.Equal(t, string(want), got)
		})
	}
}

func goldenName(width int) string {
	return filepath.Join("testdata", "display_"+map[int]string{80: "80", 120: "120"}[width]+".golden")
}

// TestHuman_NoColorMatchesDisplay: with NO_COLOR the terminal path prints
// exactly the display text. Failure prevented: NO_COLOR users get escapes.
func TestHuman_NoColorMatchesDisplay(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	r := goldenReport(80)
	assert.Equal(t, r.Display, Human(r, 80))
}

func TestFmtK(t *testing.T) {
	for n, want := range map[int]string{0: "0", 87: "87", 220: "0.2k", 6400: "6.4k", 14200: "14.2k", 142000: "142k"} {
		assert.Equal(t, want, FmtK(n), "n=%d", n)
	}
}
