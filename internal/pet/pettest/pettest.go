// Package pettest gives tests a tiny Oxy sheet, so writing the mod takes
// milliseconds instead of rendering every frame of the real art.
package pettest

import (
	"image"
	"image/color"
	"sync"

	"github.com/sageox/ox/internal/pet"
)

// Frames renders a sheet with every animation the mod needs, one small
// frame each: plug it into pet.Installer.Frames.
func Frames() (*pet.Frames, error) {
	return frames()
}

var frames = sync.OnceValues(func() (*pet.Frames, error) {
	const w, h = 4, 8
	sheet := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 2; y < h; y++ {
		for x := 1; x < w-1; x++ {
			sheet.SetNRGBA(x, y, color.NRGBA{R: 0xe0, G: 0x70, B: 0x20, A: 0xff})
		}
	}
	m := pet.Manifest{Cell: w, CellHeight: h, Columns: 1}
	for _, name := range pet.Animations {
		m.Animations = append(m.Animations, pet.Animation{Name: name, Row: 0, Frames: 1, MS: []int{100}})
	}
	return pet.BuildFrames(sheet, m)
})
