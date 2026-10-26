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
// frames 3x5 px cropped from a 6x10 cell, frame i colored by palette entry i.
func hiresFixture(t testing.TB, frames map[string]int) ([]byte, map[string][]byte) {
	m := HiresManifest{Scale: 1.5, Figure: Rect{X: 1, Y: 2, Width: 3, Height: 5}, Animations: map[string]HiresAnimation{}}
	m.Cell.Width, m.Cell.Height = 6, 10
	pal := color.Palette{color.NRGBA{}, color.NRGBA{R: 0xe0, A: 0xff}, color.NRGBA{G: 0xe0, A: 0xff}, color.NRGBA{B: 0xe0, A: 0x80}}
	strips := map[string][]byte{}
	for name, n := range frames {
		m.Animations[name] = HiresAnimation{Frames: n, Rect: Rect{X: 1, Y: 2, Width: 3, Height: 5}}
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

// tinyHiresFrames is tinyFrames with a synthetic desktop set attached.
var tinyHiresFrames = sync.OnceValues(func() (*Frames, error) {
	base, err := tinyFrames()
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for name, a := range base.Animations {
		counts[name] = len(a.PNG)
	}
	manifest, strips := hiresFixture(&testing.T{}, counts)
	f := *base
	f.Hires, err = BuildHires(manifest, counts, fromMap(strips))
	return &f, err
})

// TestBuildHires_SlicesStripIntoPaletteFrames verifies each frame is its own
// slice of the strip, still a palette PNG, with its colors intact.
// Failure prevented: frames shift by a column, or re-encode as RGBA and blow
// the desktop's per-frame Svg cap.
func TestBuildHires_SlicesStripIntoPaletteFrames(t *testing.T) {
	manifest, strips := hiresFixture(t, map[string]int{"wave": 3})
	h, err := BuildHires(manifest, map[string]int{"wave": 3}, fromMap(strips))
	require.NoError(t, err)
	require.Len(t, h.PNG["wave"], 3)
	for i, data := range h.PNG["wave"] {
		img, err := png.Decode(bytes.NewReader(data))
		require.NoError(t, err)
		p, ok := img.(*image.Paletted)
		require.True(t, ok, "frame %d decodes as %T, want a palette PNG", i, img)
		assert.Equal(t, image.Rect(0, 0, 3, 5), p.Bounds())
		assert.Equal(t, uint8(1+i%3), p.ColorIndexAt(2, 4), "frame %d", i)
	}
	assert.Equal(t, 1.5, h.Manifest.Scale)
	assert.Equal(t, Rect{X: 1, Y: 2, Width: 3, Height: 5}, h.Manifest.Figure)
}

// TestBuildHires_RejectsSetsThatDisagreeWithThePixelSet verifies the desktop
// set must match the pixel set frame for frame and fit its own cell.
// Failure prevented: the mod indexes a hi-res frame the pixel timing has but
// the folder lacks, and Oxy blinks out on the desktop mid-animation.
func TestBuildHires_RejectsSetsThatDisagreeWithThePixelSet(t *testing.T) {
	manifest, strips := hiresFixture(t, map[string]int{"wave": 2})
	_, err := BuildHires(manifest, map[string]int{"wave": 3}, fromMap(strips))
	assert.ErrorContains(t, err, "frames")
	_, err = BuildHires(manifest, map[string]int{"wave": 2, "blink": 1}, fromMap(strips))
	assert.ErrorContains(t, err, "animations")

	var m HiresManifest
	require.NoError(t, json.Unmarshal(manifest, &m))
	m.Animations["wave"] = HiresAnimation{Frames: 2, Rect: Rect{X: 4, Y: 2, Width: 3, Height: 5}}
	bad, _ := json.Marshal(m)
	_, err = BuildHires(bad, map[string]int{"wave": 2}, fromMap(strips))
	assert.ErrorContains(t, err, "outside")

	m.Animations["wave"] = HiresAnimation{Frames: 2, Rect: Rect{X: 1, Y: 2, Width: 2, Height: 5}}
	bad, _ = json.Marshal(m)
	_, err = BuildHires(bad, map[string]int{"wave": 2}, fromMap(strips))
	assert.ErrorContains(t, err, "want 2 frames")
}
