package pet

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/theme"
)

// withProfile swaps the package-level theme profile. Not parallel-safe: the
// tests that call it run serially.
func withProfile(t *testing.T, p colorprofile.Profile) {
	t.Helper()
	prev := theme.Profile
	theme.Profile = p
	t.Cleanup(func() { theme.Profile = prev })
}

// TestTerminalFrame_DrawsAFigureInTheRequestedBox verifies the art is exactly
// rows tall and Columns wide on every line, and that something is drawn.
// Failure prevented: a ragged or empty picture that shears the success block
// it sits beside, or an all-transparent crop that prints blank rows.
func TestTerminalFrame_DrawsAFigureInTheRequestedBox(t *testing.T) {
	withProfile(t, colorprofile.TrueColor)

	for _, rows := range []int{4, 6, 8} {
		art, err := TerminalFrame("confetti", 0.35, rows)
		require.NoError(t, err)
		require.Len(t, art.Lines, rows)
		assert.Positive(t, art.Columns)

		var drawn int
		for i, line := range art.Lines {
			assert.Equal(t, art.Columns, ansi.StringWidth(line), "rows=%d line %d is the box's width", rows, i)
			drawn += strings.Count(ansi.Strip(line), "▀") + strings.Count(ansi.Strip(line), "▄")
		}
		assert.Greater(t, drawn, art.Columns, "rows=%d: a figure fills more than one row of cells", rows)
		assert.Contains(t, art.Lines[0]+art.Lines[rows/2], "\x1b[", "truecolor cells carry color")
	}
}

// TestTerminalFrame_PicksTheFrameByPosition verifies at selects different
// frames: the standing start of the confetti loop is not its arms-up middle.
// Failure prevented: a position that is ignored, so every caller draws frame 0.
func TestTerminalFrame_PicksTheFrameByPosition(t *testing.T) {
	withProfile(t, colorprofile.TrueColor)

	first, err := TerminalFrame("confetti", 0, 8)
	require.NoError(t, err)
	middle, err := TerminalFrame("confetti", 0.35, 8)
	require.NoError(t, err)
	again, err := TerminalFrame("confetti", 0.35, 8)
	require.NoError(t, err)

	assert.NotEqual(t, first.Lines, middle.Lines)
	assert.Equal(t, middle, again, "the same request draws the same picture")
}

// TestTerminalFrame_DegradesColorWithTheTerminal verifies a 256-color terminal
// gets palette codes, never 24-bit ones.
// Failure prevented: a 24-bit code on a terminal that reads its channels as
// background colors, the unreadable-block bug internal/theme exists to prevent.
func TestTerminalFrame_DegradesColorWithTheTerminal(t *testing.T) {
	withProfile(t, colorprofile.ANSI256)

	art, err := TerminalFrame("confetti", 0.35, 6)
	require.NoError(t, err)
	joined := strings.Join(art.Lines, "\n")
	assert.NotContains(t, joined, "38;2;", "no 24-bit foreground on a 256-color terminal")
	assert.NotContains(t, joined, "48;2;", "no 24-bit background on a 256-color terminal")
	assert.Contains(t, joined, "38;5;")
}

func TestTerminalFrame_RejectsBadRequests(t *testing.T) {
	withProfile(t, colorprofile.TrueColor)

	tests := []struct {
		name      string
		animation string
		at        float64
		rows      int
	}{
		{"no rows", "confetti", 0.5, 0},
		{"negative rows", "confetti", 0.5, -2},
		{"position below the loop", "confetti", -0.1, 6},
		{"position past the loop", "confetti", 1.1, 6},
		{"unknown animation", "no-such-animation", 0.5, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			art, err := TerminalFrame(tt.animation, tt.at, tt.rows)
			require.Error(t, err)
			assert.Nil(t, art)
		})
	}
}

const (
	fakeManifestPath = desktopDir + "/manifest.json"
	fakeStripPath    = desktopDir + "/wave.png"
)

// desktopSet is a two-frame desktop set small enough to write out in full: one
// animation, "wave", 8x8 pixels a frame, with Oxy filling its top-left corner.
// editManifest changes the manifest before it is written; a test then edits the
// returned files to break the set another way.
func desktopSet(t *testing.T, editManifest func(m *HiresManifest)) fstest.MapFS {
	t.Helper()
	strip := image.NewNRGBA(image.Rect(0, 0, 16, 8))
	for y := 0; y < 4; y++ {
		for x := 0; x < 16; x += 8 {
			for dx := 0; dx < 4; dx++ {
				strip.SetNRGBA(x+dx, y, color.NRGBA{R: 200, G: 120, B: 40, A: 255})
			}
		}
	}
	var encoded bytes.Buffer
	require.NoError(t, png.Encode(&encoded, strip))

	manifest := HiresManifest{
		Figure:     Rect{X: 0, Y: 0, Width: 4, Height: 4},
		Animations: map[string]HiresAnimation{"wave": {MS: []int{100, 100}, Rect: Rect{Width: 8, Height: 8}}},
	}
	if editManifest != nil {
		editManifest(&manifest)
	}
	raw, err := json.Marshal(manifest)
	require.NoError(t, err)
	return fstest.MapFS{
		fakeManifestPath: {Data: raw},
		fakeStripPath:    {Data: encoded.Bytes()},
	}
}

// TestTerminalFrameFrom_DrawsTheSetItIsGiven verifies the fake set is a sound
// baseline, and that a set without per-frame durations is counted by Frames.
func TestTerminalFrameFrom_DrawsTheSetItIsGiven(t *testing.T) {
	withProfile(t, colorprofile.TrueColor)

	tests := []struct {
		name string
		edit func(m *HiresManifest)
	}{
		{"durations give the frame count", nil},
		{"an older set without durations is counted by Frames", func(m *HiresManifest) {
			m.Animations["wave"] = HiresAnimation{Frames: 2, Rect: Rect{Width: 8, Height: 8}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			art, err := terminalFrameFrom(desktopSet(t, tt.edit), "wave", 1, 3)
			require.NoError(t, err)
			require.Len(t, art.Lines, 3)
			assert.Contains(t, strings.Join(art.Lines, ""), "▀", "the figure is drawn")
		})
	}
}

// TestTerminalFrameFrom_NamesWhatIsWrongWithADamagedSet verifies each way the
// embedded art can be wrong comes back as an error that says which, never a
// panic or a blank picture.
// Failure prevented: a bad art re-import crashing `ox login` at its last step,
// after the person has already signed in.
func TestTerminalFrameFrom_NamesWhatIsWrongWithADamagedSet(t *testing.T) {
	withProfile(t, colorprofile.TrueColor)

	tests := []struct {
		name         string
		editManifest func(m *HiresManifest)
		editFiles    func(files fstest.MapFS)
		wantErr      string
	}{
		{
			name:      "no manifest",
			editFiles: func(files fstest.MapFS) { delete(files, fakeManifestPath) },
			wantErr:   "oxy desktop set",
		},
		{
			name:      "manifest is not JSON",
			editFiles: func(files fstest.MapFS) { files[fakeManifestPath] = &fstest.MapFile{Data: []byte("{not json")} },
			wantErr:   "parse desktop manifest",
		},
		{
			name: "animation has no frames",
			editManifest: func(m *HiresManifest) {
				m.Animations["wave"] = HiresAnimation{Rect: Rect{Width: 8, Height: 8}}
			},
			wantErr: "no frames to draw",
		},
		{
			name:         "animation has no size",
			editManifest: func(m *HiresManifest) { m.Animations["wave"] = HiresAnimation{MS: []int{100}} },
			wantErr:      "no frames to draw",
		},
		{
			name:         "figure sits outside the animation's crop",
			editManifest: func(m *HiresManifest) { m.Figure = Rect{X: 500, Y: 500, Width: 4, Height: 4} },
			wantErr:      "does not show the figure",
		},
		{
			name:      "strip is missing",
			editFiles: func(files fstest.MapFS) { delete(files, fakeStripPath) },
			wantErr:   `desktop animation "wave"`,
		},
		{
			name:      "strip is not a PNG",
			editFiles: func(files fstest.MapFS) { files[fakeStripPath] = &fstest.MapFile{Data: []byte("not a png")} },
			wantErr:   "decode desktop strip",
		},
		{
			name: "strip is not frames x size",
			editManifest: func(m *HiresManifest) {
				m.Animations["wave"] = HiresAnimation{MS: []int{100, 100, 100}, Rect: Rect{Width: 8, Height: 8}}
			},
			wantErr: "want 3 frames of 8x8",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := desktopSet(t, tt.editManifest)
			if tt.editFiles != nil {
				tt.editFiles(files)
			}
			art, err := terminalFrameFrom(files, "wave", 0.5, 3)
			require.Error(t, err)
			assert.Nil(t, art)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestTerminalFrame_EveryAnimationShowsTheFigure verifies each animation the
// mod can ask for has a desktop strip whose crop still holds Oxy.
// Failure prevented: a re-imported desktop set whose crop box moves off the
// figure, which would print blank rows only for the animation that shifted.
func TestTerminalFrame_EveryAnimationShowsTheFigure(t *testing.T) {
	if testing.Short() {
		t.Skip("short: decodes every desktop strip")
	}
	withProfile(t, colorprofile.TrueColor)

	for _, name := range Animations {
		art, err := TerminalFrame(name, 0.5, 6)
		require.NoError(t, err, name)
		var drawn int
		for _, line := range art.Lines {
			text := ansi.Strip(line)
			drawn += strings.Count(text, "▀") + strings.Count(text, "▄")
		}
		assert.Positive(t, drawn, name)
	}
}
