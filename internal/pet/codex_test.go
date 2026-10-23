package pet

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codexPetFile is pet.json as Codex reads it.
type codexPetFile struct {
	ID              string `json:"id"`
	SpritesheetPath string `json:"spritesheetPath"`
	Frame           struct {
		Width, Height, Columns, Rows int
	} `json:"frame"`
	Animations map[string]struct {
		Frames   []int  `json:"frames"`
		FPS      int    `json:"fps"`
		Loop     bool   `json:"loop"`
		Fallback string `json:"fallback"`
	} `json:"animations"`
}

func buildOxyCodex(t *testing.T) (*CodexSheet, *image.NRGBA, codexPetFile) {
	t.Helper()
	if testing.Short() {
		t.Skip("short: renders Oxy's Codex sheet")
	}
	sheet, err := OxyCodexSheet("1.2.3")
	require.NoError(t, err)
	img, err := png.Decode(bytes.NewReader(sheet.PNG))
	require.NoError(t, err)
	var p codexPetFile
	require.NoError(t, json.Unmarshal(sheet.Manifest, &p))
	return sheet, toNRGBA(img), p
}

func codexCell(i int) image.Rectangle {
	x, y := (i%CodexColumns)*CodexFrameWidth, (i/CodexColumns)*CodexFrameHeight
	return image.Rect(x, y, x+CodexFrameWidth, y+CodexFrameHeight)
}

// TestOxyCodexSheet_MatchesCodexContract verifies the sheet and pet.json meet
// the dimensions and references Codex validates.
// Failure prevented: Codex rejects the pet (wrong sheet size, frame grid that
// does not tile it, an index or fallback that points nowhere) and Oxy never
// appears.
func TestOxyCodexSheet_MatchesCodexContract(t *testing.T) {
	sheet, img, p := buildOxyCodex(t)

	assert.Equal(t, image.Pt(CodexSheetWidth, CodexSheetHeight), img.Bounds().Size())
	assert.Equal(t, CodexSheetWidth, p.Frame.Width*p.Frame.Columns)
	assert.Equal(t, CodexSheetHeight, p.Frame.Height*p.Frame.Rows)
	assert.LessOrEqual(t, p.Frame.Columns*p.Frame.Rows, 256)
	assert.Equal(t, "oxy", p.ID)
	assert.Equal(t, "spritesheet.png", p.SpritesheetPath)

	for _, name := range []string{
		"idle", "running", "waiting", "review", "failed",
		"running-right", "running-left", "move_right", "move_left",
		"waving", "wave", "jumping", "bounce", "sad",
	} {
		assert.Contains(t, p.Animations, name)
	}
	require.Len(t, p.Animations, len(CodexAnimations))
	cells := p.Frame.Columns * p.Frame.Rows
	for name, a := range p.Animations {
		require.NotEmpty(t, a.Frames, name)
		for _, i := range a.Frames {
			assert.Less(t, i, cells, name)
			assert.GreaterOrEqual(t, i, 0, name)
		}
		assert.Contains(t, p.Animations, a.Fallback, "%s fallback", name)
		assert.Equal(t, codexFPS, a.FPS, name)
	}
	assert.False(t, p.Animations["review"].Loop)
	assert.False(t, p.Animations["failed"].Loop)
	assert.True(t, p.Animations["idle"].Loop)

	v, err := CodexManifestVersion(sheet.Manifest)
	require.NoError(t, err)
	assert.Equal(t, "1.2.3", v)
}

// TestOxyCodexSheet_CellsHoldOxyAndNothingElse checks every used cell has
// art standing on the cell floor and every spare cell is empty.
// Failure prevented: a wrong cell offset draws Oxy floating, clipped, or
// bleeding into a neighbor cell Codex shows as a ghost frame.
func TestOxyCodexSheet_CellsHoldOxyAndNothingElse(t *testing.T) {
	_, img, p := buildOxyCodex(t)

	used := map[int]bool{}
	for _, a := range p.Animations {
		for _, i := range a.Frames {
			used[i] = true
		}
	}
	require.Len(t, used, 137)
	for i := 0; i < 137; i++ {
		require.True(t, used[i], "cells are packed from 0, cell %d unused", i)
	}
	for i := 137; i < CodexColumns*CodexRows; i++ {
		_, ok := opaqueBounds(crop(img, codexCell(i)))
		assert.False(t, ok, "spare cell %d must be transparent", i)
	}

	for _, i := range p.Animations["idle"].Frames {
		got, ok := opaqueBounds(crop(img, codexCell(i)))
		require.True(t, ok, "idle cell %d is empty", i)
		assert.GreaterOrEqual(t, got.Max.Y, CodexFrameHeight-1, "idle cell %d: Oxy stands on the floor", i)
		assert.LessOrEqual(t, got.Max.Y, CodexFrameHeight)
		assert.GreaterOrEqual(t, got.Min.X, 0)
		assert.LessOrEqual(t, got.Max.X, CodexFrameWidth)
	}
}

// TestOxyCodexSheet_ReviewKeepsConfettiTiming checks the repeated-index runs
// replay each confetti frame for about as long as the source sheet asks.
// Failure prevented: the celebration plays at the wrong speed in Codex.
func TestOxyCodexSheet_ReviewKeepsConfettiTiming(t *testing.T) {
	_, _, p := buildOxyCodex(t)
	m, err := OxyManifest()
	require.NoError(t, err)
	var confetti Animation
	for _, a := range m.Animations {
		if a.Name == "confetti" {
			confetti = a
		}
	}
	ms, err := confetti.durations()
	require.NoError(t, err)

	var runs []int
	frames := p.Animations["review"].Frames
	for i, idx := range frames {
		if i > 0 && idx == frames[i-1] {
			runs[len(runs)-1]++
			continue
		}
		runs = append(runs, 1)
	}
	require.Len(t, runs, len(ms))
	tick := 1000 / codexFPS
	for i, n := range runs {
		assert.InDelta(t, ms[i], n*tick, float64(tick)/2, "confetti frame %d", i)
	}
}

func TestBuildCodexSheet(t *testing.T) {
	// one frame, drawn larger than a codex cell (downscale only shrinks),
	// whose art fills its bottom three quarters edge to edge
	const w, h = 200, 260
	sheet := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := h / 4; y < h; y++ {
		for x := 0; x < w; x++ {
			sheet.SetNRGBA(x, y, color.NRGBA{R: 0xe0, G: 0x70, B: 0x20, A: 0xff})
		}
	}
	all := Manifest{Cell: w, CellHeight: h, Columns: 1}
	for _, name := range []string{"blink", "laptop", "wave", "confetti", "fail"} {
		all.Animations = append(all.Animations, Animation{Name: name, Row: 0, Frames: 1, MS: []int{90}})
	}

	tests := []struct {
		name    string
		m       Manifest
		wantErr string
	}{
		{name: "every source present", m: all},
		{name: "missing source", m: Manifest{Cell: w, CellHeight: h, Columns: 1, Animations: all.Animations[:4]}, wantErr: `no "fail" animation`},
		{name: "zero cell", m: Manifest{Animations: all.Animations}, wantErr: "cell size must be positive"},
		{name: "row past sheet", m: func() Manifest {
			m := all
			m.Animations = append([]Animation(nil), all.Animations...)
			m.Animations[0].Row = 5
			return m
		}(), wantErr: "exceeds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildCodexSheet(sheet, tt.m, "dev")
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			var p codexPetFile
			require.NoError(t, json.Unmarshal(got.Manifest, &p))
			// 90 ms at a 50 ms tick shows twice; five sources fill cells 0..4
			assert.Equal(t, []int{0, 0}, p.Animations["idle"].Frames)
			assert.Equal(t, []int{4, 4}, p.Animations["sad"].Frames)

			img, err := png.Decode(bytes.NewReader(got.PNG))
			require.NoError(t, err)
			b, ok := opaqueBounds(crop(img, codexCell(0)))
			require.True(t, ok)
			assert.Equal(t, CodexFrameHeight, b.Max.Y, "art rests on the cell floor")
			center := float64(b.Min.X+b.Max.X) / 2
			assert.InDelta(t, float64(CodexFrameWidth)/2, center, 1, "art is centered")
			assert.Less(t, math.Abs(float64(b.Dy())-float64(codexArtHeight)*3/4), 2.0, "art is scaled to fit the cell's art box")
		})
	}
}

func TestCodexManifestVersion(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "stamped", in: `{"id":"oxy","oxVersion":"0.18.0"}`, want: "0.18.0"},
		{name: "unstamped", in: `{"id":"oxy"}`, want: ""},
		{name: "malformed", in: `{`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CodexManifestVersion([]byte(tt.in))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
