package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `ox statusline --wrap` keeps ox's row at the bottom of the wrapped app's
// terminal. These tests cover the part that decides whether the app's own
// output can take that row back.

// TestWrapFilter_KeepsTheAppOffOxRow is the promise the row rests on.
// Failure prevented: the app resets the scroll region and its next scroll
// carries ox's row up into the transcript, leaving a stray status line in
// the scrollback on every screenful.
func TestWrapFilter_KeepsTheAppOffOxRow(t *testing.T) {
	tests := []struct {
		name, in, want string
		dirty          bool
	}{
		{"plain text passes untouched", "hello\r\nworld", "hello\r\nworld", false},
		{"color and cursor moves pass untouched", "\x1b[31mred\x1b[0m\x1b[5;10H", "\x1b[31mred\x1b[0m\x1b[5;10H", false},
		{"a region reset becomes ox's region", "\x1b[r", "\x1b[1;23r", false},
		{"so does the empty-parameter form", "\x1b[;r", "\x1b[1;23r", false},
		{"a region inside the app's rows is kept", "\x1b[3;20r", "\x1b[3;20r", false},
		{"a region reaching past the app's rows is cut back", "\x1b[2;24r", "\x1b[2;23r", false},
		{"a private-mode restore is not a region", "\x1b[?25r", "\x1b[?25r", false},
		{"clearing the screen means ox redraws", "\x1b[2J", "\x1b[2J", true},
		{"so does switching to the other screen", "\x1b[?1049h", "\x1b[?1049h", true},
		{"and leaving it", "\x1b[?1049l", "\x1b[?1049l", true},
		{"hiding the cursor does not", "\x1b[?25l", "\x1b[?25l", false},
		{"a full reset gets the region put back", "\x1bc", "\x1bc\x1b[1;23r", true},
		{"a title sequence ending in r is not a region", "\x1b]0;my r title\x07\x1b[r", "\x1b]0;my r title\x07\x1b[1;23r", false},
		{"nor one ended by ST", "\x1b]8;;http://x/[r\x1b\\ok", "\x1b]8;;http://x/[r\x1b\\ok", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &wrapFilter{rows: 23}
			assert.Equal(t, tt.want, string(f.write([]byte(tt.in))))
			assert.Equal(t, tt.dirty, f.dirty)
			assert.True(t, f.idle(), "a complete sequence leaves the filter between sequences")
		})
	}
}

// TestWrapFilter_SequencesSplitAcrossReads: a pseudo-terminal hands output
// over in arbitrary chunks.
// Failure prevented: a reset split across two reads slipping through, or ox
// drawing its row in the middle of the app's half-written sequence and
// corrupting the screen.
func TestWrapFilter_SequencesSplitAcrossReads(t *testing.T) {
	stream := "text\x1b[r more \x1b[2J\x1b]0;title\x07\x1b[10;1Hend\x1bc."
	whole := &wrapFilter{rows: 23}
	want := string(whole.write([]byte(stream)))

	for cut := 1; cut < len(stream); cut++ {
		f := &wrapFilter{rows: 23}
		got := string(f.write([]byte(stream[:cut])))
		midSequence := !f.idle()
		got += string(f.write([]byte(stream[cut:])))
		assert.Equal(t, want, got, "cut at %d", cut)
		assert.True(t, f.idle(), "cut at %d", cut)
		// Wherever the cut falls inside a sequence, ox must be told to wait.
		if tail := stream[:cut]; strings.HasSuffix(tail, "\x1b") || strings.HasSuffix(tail, "\x1b[") || strings.HasSuffix(tail, "\x1b[2") || strings.HasSuffix(tail, "\x1b]0;ti") {
			assert.True(t, midSequence, "cut at %d is inside a sequence", cut)
		}
	}
}

// TestWrapFilter_ResizeWaitsForTheAppsSequence: a resize must set the new
// scroll region, but never in the middle of something the app is writing.
// Failure prevented: ox's escape codes landing inside an unfinished title,
// link or image command and corrupting it.
func TestWrapFilter_ResizeWaitsForTheAppsSequence(t *testing.T) {
	const region = "\x1b7\x1b[1;29r\x1b8"

	idle := &wrapFilter{rows: 23}
	assert.Equal(t, region, idle.resized(29), "between sequences: written at once")
	assert.True(t, idle.dirty, "ox's row is redrawn at its new place")
	assert.Empty(t, idle.flushRegion(), "and only once")

	for _, start := range []string{"\x1b]0;a long ti", "\x1b", "\x1b[38;5", "\x1b]8;;https://exam", "\x1bPq#0;2"} {
		f := &wrapFilter{rows: 23}
		_ = f.write([]byte("text" + start))
		require.False(t, f.idle(), "%q is an unfinished sequence", start)
		assert.Empty(t, f.resized(29), "%q: nothing may be written inside it", start)
		assert.Empty(t, f.flushRegion(), "%q: still unfinished", start)
		assert.Equal(t, 29, f.rows, "the filter already clamps to the new height")

		// The app finishes its sequence; the region follows, once.
		_ = f.write([]byte("\x07"))
		if !f.idle() {
			_ = f.write([]byte("m"))
		}
		require.True(t, f.idle(), "%q", start)
		assert.Equal(t, region, f.flushRegion(), "%q", start)
		assert.Empty(t, f.flushRegion(), "%q", start)
	}
}

// Failure prevented: a runaway sequence making the filter hold output back forever.
func TestWrapFilter_OverlongSequenceIsFlushed(t *testing.T) {
	f := &wrapFilter{rows: 23}
	in := "\x1b[" + strings.Repeat("1;", 80)
	out := string(f.write([]byte(in + "visible")))
	assert.Contains(t, out, "visible")
	assert.True(t, f.idle())
}

// TestWrapRow_FitsTheTerminal: ox has exactly one row.
// Failure prevented: the row wrapping onto a second line and pushing the
// app's last line off screen.
func TestWrapRow_FitsTheTerminal(t *testing.T) {
	lane := func(columns int) string {
		if columns <= 0 {
			return "LANE"
		}
		return strings.Repeat("L", columns)
	}
	assert.Equal(t, "● ox  1 plan", wrapRow("● ox  1 plan", nil, 80), "no ox call yet: the status line alone")
	for width := 10; width <= 120; width += 7 {
		row := wrapRow("● ox  1 plan  ● rec", lane, width)
		assert.LessOrEqual(t, ansi.StringWidth(row), max(width, len("● ox  1 plan  ● rec")), "width %d", width)
	}
	assert.Equal(t, "● ox", wrapRow("● ox", lane, 20), "too narrow for a lane's track: the status line alone")
	assert.True(t, strings.HasPrefix(wrapRow("● ox", lane, 80), "● ox  L"))
}

// Failure prevented: ox's row moving the app's cursor, so the next thing the app types lands on ox's row.
func TestDrawWrapRow_SavesAndRestoresTheCursor(t *testing.T) {
	got := drawWrapRow(24, "● ox")
	assert.True(t, strings.HasPrefix(got, "\x1b7\x1b[24;1H\x1b[2K"), "%q", got)
	assert.True(t, strings.HasSuffix(got, "\x1b8"), "%q", got)
	assert.Contains(t, got, "● ox")
}

// TestNoteWrapSession_TellsTheWrapperItsSession: the row must follow the
// session in its own terminal.
// Failure prevented: two Codex terminals in one repo showing each other's
// counts, or a row stuck at "● ox" because nothing names its session.
func TestNoteWrapSession_TellsTheWrapperItsSession(t *testing.T) {
	path := newWrapSessionFile()
	require.NotEmpty(t, path)
	t.Cleanup(func() { _ = os.Remove(path) })
	assert.Empty(t, readWrapSession(path), "before any hook has run")

	t.Setenv(envStatuslineWrapSession, path)
	noteWrapSession("thread-1")
	assert.Equal(t, "thread-1", readWrapSession(path))
	noteWrapSession("") // a hook with no session changes nothing
	assert.Equal(t, "thread-1", readWrapSession(path))
	noteWrapSession("thread-2") // /new in the same terminal
	assert.Equal(t, "thread-2", readWrapSession(path))
}

// TestNoteWrapSession_WritesOnlyTheWrappersOwnFile: the variable is an
// environment value, so a hook must not trust it.
// Failure prevented: a planted OX_STATUSLINE_WRAP_SESSION making every hook
// overwrite or create a file of the planter's choosing.
func TestNoteWrapSession_WritesOnlyTheWrappersOwnFile(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name  string
		setup func(t *testing.T) string // returns the path to plant
	}{
		{"a file not named like the wrapper's", func(t *testing.T) string {
			p := filepath.Join(dir, "important.txt")
			require.NoError(t, os.WriteFile(p, []byte("keep"), 0o600))
			return p
		}},
		{"a wrapper-style name that does not exist", func(t *testing.T) string {
			return filepath.Join(dir, "ox-wrap-missing.session")
		}},
		{"a wrapper-style name that is a directory", func(t *testing.T) string {
			p := filepath.Join(dir, "ox-wrap-dir.session")
			require.NoError(t, os.Mkdir(p, 0o700))
			return p
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.setup(t)
			before, _ := os.ReadFile(p)
			info, _ := os.Lstat(p)
			t.Setenv(envStatuslineWrapSession, p)
			noteWrapSession("thread-1")
			after, _ := os.ReadFile(p)
			assert.Equal(t, before, after)
			infoAfter, err := os.Lstat(p)
			if info == nil {
				assert.True(t, os.IsNotExist(err), "nothing is created")
			} else {
				require.NoError(t, err)
				assert.Equal(t, info.IsDir(), infoAfter.IsDir())
			}
		})
	}
	// An absurdly long "session id" is not one.
	p := newWrapSessionFile()
	t.Cleanup(func() { _ = os.Remove(p) })
	t.Setenv(envStatuslineWrapSession, p)
	noteWrapSession(strings.Repeat("x", maxWrapSessionID+1))
	assert.Empty(t, readWrapSession(p))
}
