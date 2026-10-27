package pet

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hiresFixture is a synthetic desktop set: per animation a palette strip of
// len(ms) frames, 3x5 px cropped from a 6x10 cell, frame i colored by palette
// entry 1+i%3, and the manifest carrying those durations.
func hiresFixture(t testing.TB, ms map[string][]int) ([]byte, map[string][]byte) {
	m := HiresManifest{Scale: 1.5, Figure: Rect{X: 1, Y: 2, Width: 3, Height: 5}, Animations: map[string]HiresAnimation{}}
	m.Cell.Width, m.Cell.Height = 6, 10
	pal := color.Palette{color.NRGBA{}, color.NRGBA{R: 0xe0, A: 0xff}, color.NRGBA{G: 0xe0, A: 0xff}, color.NRGBA{B: 0xe0, A: 0x80}}
	strips := map[string][]byte{}
	for name, d := range ms {
		n := len(d)
		m.Animations[name] = HiresAnimation{MS: d, Rect: Rect{X: 1, Y: 2, Width: 3, Height: 5}}
		img := image.NewPaletted(image.Rect(0, 0, 3*n, 5), pal)
		for i := 0; i < n; i++ {
			for y := 0; y < 5; y++ {
				for x := 0; x < 3; x++ {
					img.SetColorIndex(i*3+x, y, uint8(1+i%3))
				}
			}
		}
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, img))
		strips[name] = buf.Bytes()
	}
	data, err := json.Marshal(m)
	require.NoError(t, err)
	return data, strips
}

func fromMap(strips map[string][]byte) func(string) ([]byte, error) {
	return func(name string) ([]byte, error) {
		data, ok := strips[name]
		if !ok {
			return nil, fmt.Errorf("no strip %s", name)
		}
		return data, nil
	}
}

// tinyHiresFrames is tinyFrames with a synthetic desktop set attached that,
// like the real one, runs at a higher frame rate: each 100 ms pixel frame is
// three hi-res frames of 33, 33 and 34 ms.
var tinyHiresFrames = sync.OnceValues(func() (*Frames, error) {
	base, err := tinyFrames()
	if err != nil {
		return nil, err
	}
	pixel, hires := map[string][]int{}, map[string][]int{}
	for name, a := range base.Animations {
		pixel[name] = a.MS
		hires[name] = []int{33, 33, 34}
	}
	manifest, strips := hiresFixture(&testing.T{}, hires)
	f := *base
	f.Hires, err = BuildHires(manifest, pixel, fromMap(strips))
	return &f, err
})

// TestBuildHires_SlicesStripIntoPaletteFrames verifies each frame is its own
// slice of the strip, still a palette PNG, with its colors intact, and that
// the hi-res set keeps its own frame count and timing.
// Failure prevented: frames shift by a column, re-encode as RGBA and blow the
// desktop's per-frame Svg cap, or play at the pixel set's slower rate.
func TestBuildHires_SlicesStripIntoPaletteFrames(t *testing.T) {
	manifest, strips := hiresFixture(t, map[string][]int{"wave": {40, 40, 40, 40, 40, 40}})
	h, err := BuildHires(manifest, map[string][]int{"wave": {120, 120}}, fromMap(strips))
	require.NoError(t, err)
	require.Len(t, h.PNG["wave"], 6)
	for i, data := range h.PNG["wave"] {
		img, err := png.Decode(bytes.NewReader(data))
		require.NoError(t, err)
		p, ok := img.(*image.Paletted)
		require.True(t, ok, "frame %d decodes as %T, want a palette PNG", i, img)
		assert.Equal(t, image.Rect(0, 0, 3, 5), p.Bounds())
		assert.Equal(t, uint8(1+i%3), p.ColorIndexAt(2, 4), "frame %d", i)
	}
	assert.Equal(t, []int{40, 40, 40, 40, 40, 40}, h.Manifest.Animations["wave"].MS)
	assert.Equal(t, 6, h.Manifest.Animations["wave"].Frames)
	assert.Equal(t, 1.5, h.Manifest.Scale)
	assert.Equal(t, Rect{X: 1, Y: 2, Width: 3, Height: 5}, h.Manifest.Figure)
}

// TestBuildHires_WithoutMSBorrowsPixelTiming verifies an older set with no
// per-frame durations still loads when it matches the pixel set frame for
// frame. Failure prevented: a set built before hi-res timing breaks enable.
func TestBuildHires_WithoutMSBorrowsPixelTiming(t *testing.T) {
	manifest, strips := hiresFixture(t, map[string][]int{"wave": {1, 1}})
	var m HiresManifest
	require.NoError(t, json.Unmarshal(manifest, &m))
	m.Animations["wave"] = HiresAnimation{Frames: 2, Rect: m.Animations["wave"].Rect}
	old, _ := json.Marshal(m)

	h, err := BuildHires(old, map[string][]int{"wave": {90, 1400}}, fromMap(strips))
	require.NoError(t, err)
	assert.Equal(t, []int{90, 1400}, h.Manifest.Animations["wave"].MS)

	_, err = BuildHires(old, map[string][]int{"wave": {90, 90, 90}}, fromMap(strips))
	assert.ErrorContains(t, err, "without ms")
}

// TestBuildHires_RejectsSetsThatDisagreeWithThePixelSet verifies the desktop
// set must name the pixel set's animations, loop for about as long, have
// positive durations, and fit its own cell and strip.
// Failure prevented: the mod plays the wrong art in a slot, or indexes a
// hi-res frame the folder lacks and Oxy blinks out on the desktop.
func TestBuildHires_RejectsSetsThatDisagreeWithThePixelSet(t *testing.T) {
	manifest, strips := hiresFixture(t, map[string][]int{"wave": {50, 50}})

	_, err := BuildHires(manifest, map[string][]int{"wave": {100}, "blink": {100}}, fromMap(strips))
	assert.ErrorContains(t, err, "animations")

	// 100 ms against 109 is inside the 10% tolerance; against 112 it is not.
	_, err = BuildHires(manifest, map[string][]int{"wave": {109}}, fromMap(strips))
	assert.NoError(t, err)
	_, err = BuildHires(manifest, map[string][]int{"wave": {112}}, fromMap(strips))
	assert.ErrorContains(t, err, `desktop animation "wave" loops for 100 ms, the pixel set for 112`)

	var m HiresManifest
	require.NoError(t, json.Unmarshal(manifest, &m))
	rect := m.Animations["wave"].Rect
	try := func(a HiresAnimation) error {
		m.Animations["wave"] = a
		bad, _ := json.Marshal(m)
		_, err := BuildHires(bad, map[string][]int{"wave": {100}}, fromMap(strips))
		return err
	}
	assert.ErrorContains(t, try(HiresAnimation{MS: []int{100, 0}, Rect: rect}), "positive")
	assert.ErrorContains(t, try(HiresAnimation{Frames: 3, MS: []int{50, 50}, Rect: rect}), "ms has 2")
	assert.ErrorContains(t, try(HiresAnimation{MS: []int{50, 50}, Rect: Rect{X: 4, Y: 2, Width: 3, Height: 5}}), "outside")
	assert.ErrorContains(t, try(HiresAnimation{MS: []int{50, 50}, Rect: Rect{X: 1, Y: 2, Width: 2, Height: 5}}), "want 2 frames")
	assert.ErrorContains(t, try(HiresAnimation{MS: []int{25, 25, 25, 25}, Rect: rect}), "want 4 frames")
}
