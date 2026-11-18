package flowlane

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/oxactivity"
)

func search() Lane {
	return Lane{
		Direction: oxactivity.DirectionIn, Label: "searched team memory", Step: "searching team discussions",
		Tokens: 1180, Items: 5, ItemsNoun: "discussions", Duration: 800 * time.Millisecond,
	}
}

func murmur() Lane {
	return Lane{Direction: oxactivity.DirectionOut, Label: "posted murmur", Step: "posting murmur", Tokens: 64, Duration: 400 * time.Millisecond}
}

func with(l Lane, f func(*Lane)) Lane {
	f(&l)
	return l
}

const settled = AnimDuration + time.Millisecond

// --- A. The grammar shared with the Claude Code plugin ---

// TestRender_GoldenRows pins the rows internal/usagepane/mod/tests/textlane.test.tsx
// also asserts ("golden rows (shared with internal/flowlane)").
// Failure prevented: the plugin's terminal lane and `ox statusline --watch`
// drift into two different drawings of the same call.
func TestRender_GoldenRows(t *testing.T) {
	tests := []struct {
		name string
		lane Lane
		want string
	}{
		// 2.4s into a running call: dots every six cells, on their way in.
		{"running", with(search(), func(l *Lane) { l.Running = true }), "SAGEOX ●┄┄•┄┄┄┄┄•┄┄┄┄┄•┄○ SESSION  searching team discussions"},
		{"settled in 1k+", search(), "SAGEOX ●━━━━━━━━━━━━━━━━● SESSION  searched team memory · ~1.2k from 5 discussions · 0.8s"},
		{"settled in under 1k", with(search(), func(l *Lane) { l.Tokens, l.Items = 420, 0 }), "SAGEOX ●────────────────● SESSION  searched team memory · ~420 · 0.8s"},
		{"settled out", murmur(), "SAGEOX ●────────────────● SESSION  posted murmur · ~64 · 0.4s"},
		{"failed", with(search(), func(l *Lane) { l.Failed, l.Tokens, l.Duration = true, 0, 1200*time.Millisecond }), "SAGEOX ●╶─              ● SESSION  failed while searching team discussions · 1.2s"},
		{"nothing came back", with(search(), func(l *Lane) { l.Tokens, l.Items, l.Duration = 0, 0, 300*time.Millisecond }), "SAGEOX ●╶─              ● SESSION  searched team memory · nothing came back · 0.3s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Render(tt.lane, settled, Options{Columns: 120}))
		})
	}
}

// TestRender_FlightFrames pins mid-flight tracks to the plugin's frames.
// Failure prevented: dots travel the wrong way, or an outbound call is drawn
// as context arriving.
func TestRender_FlightFrames(t *testing.T) {
	in := with(search(), func(l *Lane) { l.Tokens = 3000 })
	out := with(search(), func(l *Lane) { l.Direction, l.Tokens = oxactivity.DirectionOut, 2000 })
	trackOf := func(l Lane, since time.Duration) string {
		return firstTrack(Render(l, since, Options{Columns: Track + 2}))
	}
	assert.Equal(t, "●━•━•━━━━┄┄┄┄┄┄┄┄●", trackOf(in, 300*time.Millisecond))
	assert.Equal(t, "●━━━━━━•━•━•━━━━━●", trackOf(in, 900*time.Millisecond))
	assert.Equal(t, "●━━━━━◦━◦━━━━━━━━●", trackOf(out, 900*time.Millisecond))
	assert.NotContains(t, trackOf(out, 900*time.Millisecond), "•")
}

// TestRender_RunningMarchesTowardTheDestination pins a running lane's frames
// to the plugin's ("a running lane marches").
// Failure prevented: a call in progress looks idle, or a push is drawn moving
// the same way as a pull.
func TestRender_RunningMarchesTowardTheDestination(t *testing.T) {
	in := with(search(), func(l *Lane) { l.Running = true })
	out := with(murmur(), func(l *Lane) { l.Running = true })
	trackOf := func(l Lane, since time.Duration) string {
		return firstTrack(Render(l, since, Options{Columns: Track + 2}))
	}
	// A pull: filled dots leave SageOx (left) one cell per 120ms.
	assert.Equal(t, "●•┄┄┄┄┄•┄┄┄┄┄•┄┄┄○", trackOf(in, 0))
	assert.Equal(t, "●┄•┄┄┄┄┄•┄┄┄┄┄•┄┄○", trackOf(in, 120*time.Millisecond))
	assert.Equal(t, "●┄┄•┄┄┄┄┄•┄┄┄┄┄•┄○", trackOf(in, 240*time.Millisecond))
	// A push: the mirror, hollow rings leaving the session (right).
	assert.Equal(t, "○┄┄┄◦┄┄┄┄┄◦┄┄┄┄┄◦●", trackOf(out, 0))
	assert.Equal(t, "○┄┄◦┄┄┄┄┄◦┄┄┄┄┄◦┄●", trackOf(out, 120*time.Millisecond))
	assert.NotContains(t, trackOf(out, 120*time.Millisecond), "•")
	// Asked for no motion, or a start too old to believe: the plain dotted line.
	plain := "●┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄○"
	assert.Equal(t, plain, firstTrack(Render(in, 240*time.Millisecond, Options{Columns: Track + 2, Still: true})))
	assert.Equal(t, plain, trackOf(in, MarchMax))
}

// firstTrack is the leading Track runes of a row drawn without labels.
func firstTrack(row string) string {
	return string([]rune(row)[:Track])
}

// --- B. Motion ---

// Failure prevented: the watch pane ticks fast forever, or a failed call animates as if it delivered.
func TestMoving(t *testing.T) {
	assert.True(t, Moving(search(), 100*time.Millisecond))
	assert.False(t, Moving(search(), AnimDuration))
	// A running call moves until its start is too old to believe.
	assert.True(t, Moving(with(search(), func(l *Lane) { l.Running = true }), 100*time.Millisecond))
	assert.False(t, Moving(with(search(), func(l *Lane) { l.Running = true }), MarchMax))
	assert.False(t, Moving(with(search(), func(l *Lane) { l.Failed = true }), 100*time.Millisecond))
	assert.False(t, Moving(with(search(), func(l *Lane) { l.Tokens = 0 }), 100*time.Millisecond))
	// Still is the settled frame.
	assert.Equal(t, Render(search(), settled, Options{}), Render(search(), 100*time.Millisecond, Options{Still: true}))
}

// Failure prevented: a huge call floods the lane with dots that merge into a bar.
func TestDotsCapped(t *testing.T) {
	big := with(search(), func(l *Lane) { l.Tokens = 500_000 })
	most := 0
	frames := map[string]bool{}
	for since := time.Duration(0); since < AnimDuration; since += 25 * time.Millisecond {
		tr := firstTrack(Render(big, since, Options{Columns: Track + 2}))
		frames[tr] = true
		most = max(most, strings.Count(tr, "•"))
	}
	assert.Equal(t, MaxDots, most)
	assert.Greater(t, len(frames), 10, "the lane must actually move")
}

// --- C. Fitting the row ---

// Failure prevented: a lane wraps in a narrow pane and breaks the in-place redraw.
func TestRender_NeverWiderThanColumns(t *testing.T) {
	lanes := []Lane{search(), murmur(), with(search(), func(l *Lane) { l.Failed = true }),
		with(search(), func(l *Lane) { l.Label, l.Tokens = "a very long label that names a great many things at once", 123_456 })}
	for cols := Track; cols <= 140; cols += 3 {
		for _, l := range lanes {
			for _, color := range []bool{false, true} {
				row := Render(l, settled, Options{Columns: cols, Color: color})
				assert.LessOrEqual(t, ansi.StringWidth(row), cols, "cols=%d row=%q", cols, ansi.Strip(row))
			}
		}
	}
}

func TestRender_NarrowDropsLabelsKeepsTrack(t *testing.T) {
	wide := Render(search(), settled, Options{Columns: NarrowColumns})
	narrow := Render(search(), settled, Options{Columns: NarrowColumns - 1})
	assert.True(t, strings.HasPrefix(wide, "SAGEOX "))
	assert.True(t, strings.HasPrefix(narrow, "●"))
	assert.Equal(t, Track, utf8.RuneCountInString(firstTrack(narrow)))
}

func TestFit_LabelShrinksThenGoesNumbersStay(t *testing.T) {
	text := func(room int) string {
		var b strings.Builder
		for _, s := range fit(digest(search()), room) {
			b.WriteString(s.text)
		}
		return b.String()
	}
	assert.Equal(t, "searched team memory · ~1.2k from 5 discussions · 0.8s", text(60))
	assert.Equal(t, "searched team m… · ~1.2k from 5 discussions · 0.8s", text(50))
	assert.Equal(t, "~1.2k from 5 discussions · 0.8s", text(34))
	assert.True(t, strings.HasSuffix(text(10), "…"))
	assert.Equal(t, "", text(0))
}

// --- D. Words ---

func TestDigest(t *testing.T) {
	join := func(l Lane) string {
		var b strings.Builder
		for _, s := range digest(l) {
			b.WriteString(s.text)
		}
		return b.String()
	}
	// A harness with no pre-tool hook measures no duration: it is left off, not shown as 0.
	assert.Equal(t, "searched team memory · ~1.2k from 5 discussions", join(with(search(), func(l *Lane) { l.Duration = 0 })))
	assert.Equal(t, "saved plan · size unknown · 1.6s", join(Lane{Direction: oxactivity.DirectionOut, Label: "saved plan", Unsized: true, Duration: 1600 * time.Millisecond}))
	assert.Equal(t, "posted murmur · nothing sent", join(with(murmur(), func(l *Lane) { l.Tokens, l.Duration = 0, 0 })))
	assert.Equal(t, "saved plan · ~3.4k in 1 plan · 1.6s", join(Lane{Direction: oxactivity.DirectionOut, Label: "saved plan", Tokens: 3400, Items: 1, ItemsNoun: "plan", Duration: 1600 * time.Millisecond}))
}

func TestFormat(t *testing.T) {
	for n, want := range map[int]string{0: "~0", 80: "~80", 999: "~999", 1000: "~1k", 1180: "~1.2k", 9960: "~10k", 15_400: "~15k"} {
		assert.Equal(t, want, FormatTokens(n), n)
	}
	for d, want := range map[time.Duration]string{0: "0.1s", 800 * time.Millisecond: "0.8s", 9960 * time.Millisecond: "10s", 12 * time.Second: "12s", 72 * time.Second: "1m 12s"} {
		assert.Equal(t, want, FormatDuration(d), d)
	}
}

// --- E. Color ---

// Failure prevented: NO_COLOR output carries escape bytes, or color changes the text.
func TestRender_ColorOnlyAddsEscapes(t *testing.T) {
	for _, l := range []Lane{search(), murmur(), with(search(), func(l *Lane) { l.Failed = true }), with(search(), func(l *Lane) { l.Running = true })} {
		plain := Render(l, 500*time.Millisecond, Options{Columns: 100})
		colored := Render(l, 500*time.Millisecond, Options{Columns: 100, Color: true})
		require.NotContains(t, plain, "\x1b")
		assert.Equal(t, plain, ansi.Strip(colored))
	}
	assert.Contains(t, Render(search(), settled, Options{Color: true}), "\x1b")
}

// Failure prevented: a failed call's lane is as grey as one that simply found
// nothing, so the failure goes unseen; or an empty result is painted as an error.
func TestRender_FailedTrackIsRed(t *testing.T) {
	red := badStyle.Render("●╶─")
	trackOf := func(l Lane) string {
		row := Render(l, settled, Options{Columns: Track + 2, Color: true})
		return row[:strings.LastIndex(row, "●")+len("●")+len("\x1b[m")]
	}
	failed := trackOf(with(search(), func(l *Lane) { l.Failed, l.Tokens = true, 0 }))
	assert.True(t, strings.HasPrefix(failed, red), "stub and origin in the error color: %q", failed)
	assert.True(t, strings.HasSuffix(failed, badStyle.Render("●")), "far end too: %q", failed)

	empty := trackOf(with(search(), func(l *Lane) { l.Tokens, l.Items = 0, 0 }))
	assert.NotContains(t, empty, "196", "nothing came back is not a failure")
	assert.Equal(t, quietStyle.Render("●╶─              ●"), empty)
}
