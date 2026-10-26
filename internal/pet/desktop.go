package pet

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io/fs"
	"sort"
	"strings"
)

// The high-resolution set the Claude desktop app and VS Code draw: one
// palette PNG strip per animation plus manifest.json, built by
// assets/oxy/desktop/make-desktop-set.py. The terminal keeps the pixel set;
// half-block cells and kitty frames gain nothing from more pixels.
//
//go:embed assets/oxy/desktop
var desktopAssets embed.FS

const desktopDir = "assets/oxy/desktop"

// Rect is a box in uncropped hi-res cell pixels.
type Rect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// HiresAnimation is one animation's crop: the union of its frames' visible
// pixels, so the figure stays anchored, placed in the uncropped cell so every
// animation shares one ground line.
type HiresAnimation struct {
	Frames int `json:"frames"`
	Rect
}

// HiresManifest is assets/oxy/desktop/manifest.json.
type HiresManifest struct {
	Cell struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"cell"`
	// Scale is hi-res pixels per pixel-set pixel.
	Scale float64 `json:"scale"`
	// Figure is Oxy alone (blink, wag, wag-big): the mod sizes him by its
	// height so he keeps one scale whatever prop an animation adds.
	Figure     Rect                      `json:"figure"`
	Animations map[string]HiresAnimation `json:"animations"`
}

// Hires is the desktop set sliced into frames, each a paletted PNG.
type Hires struct {
	Manifest HiresManifest
	PNG      map[string][][]byte
}

// oxyHires loads the embedded desktop set, or nil when the folder holds no
// manifest (a checkout without the art): the mod then draws the pixel set on
// the desktop too, which frames.ts signals by leaving out `hires`.
func oxyHires(pixel Manifest) (*Hires, error) {
	manifest, err := desktopAssets.ReadFile(desktopDir + "/manifest.json")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("oxy desktop set: %w", err)
	}
	want := make(map[string]int, len(pixel.Animations))
	for _, a := range pixel.Animations {
		want[a.Name] = a.Frames
	}
	return BuildHires(manifest, want, func(name string) ([]byte, error) {
		return desktopAssets.ReadFile(desktopDir + "/" + name + ".png")
	})
}

// BuildHires slices each animation's strip into frames. frames is each
// animation's frame count in the pixel set: the two sets share timing, so a
// count that differs, or an animation either set lacks, is an error.
func BuildHires(manifestJSON []byte, frames map[string]int, strip func(name string) ([]byte, error)) (*Hires, error) {
	var m HiresManifest
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		return nil, fmt.Errorf("parse desktop manifest: %w", err)
	}
	if m.Cell.Width <= 0 || m.Cell.Height <= 0 || m.Scale <= 0 || m.Figure.Height <= 0 {
		return nil, fmt.Errorf("desktop manifest: cell, scale and figure must be positive")
	}
	if len(m.Animations) != len(frames) {
		return nil, fmt.Errorf("desktop manifest has %d animations, the pixel set %d", len(m.Animations), len(frames))
	}
	h := &Hires{Manifest: m, PNG: make(map[string][][]byte, len(frames))}
	for name, n := range frames {
		a, ok := m.Animations[name]
		if !ok || a.Frames != n {
			return nil, fmt.Errorf("desktop animation %q: %d frames, the pixel set has %d", name, a.Frames, n)
		}
		if a.Width <= 0 || a.Height <= 0 || a.X < 0 || a.Y < 0 || a.X+a.Width > m.Cell.Width || a.Y+a.Height > m.Cell.Height {
			return nil, fmt.Errorf("desktop animation %q: crop %+v falls outside the %dx%d cell", name, a.Rect, m.Cell.Width, m.Cell.Height)
		}
		data, err := strip(name)
		if err != nil {
			return nil, fmt.Errorf("desktop animation %q: %w", name, err)
		}
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("decode desktop strip %q: %w", name, err)
		}
		b := img.Bounds()
		if b.Dx() != a.Width*n || b.Dy() != a.Height {
			return nil, fmt.Errorf("desktop strip %q is %dx%d, want %d frames of %dx%d", name, b.Dx(), b.Dy(), n, a.Width, a.Height)
		}
		sub, ok := img.(interface {
			SubImage(image.Rectangle) image.Image
		})
		if !ok {
			return nil, fmt.Errorf("desktop strip %q: unsliceable %T", name, img)
		}
		for i := 0; i < n; i++ {
			// A *image.Paletted slice re-encodes as a palette PNG with the
			// strip's palette, so a frame stays as small as its share of the
			// strip (about 4x smaller than RGBA).
			var buf bytes.Buffer
			frame := sub.SubImage(image.Rect(b.Min.X+i*a.Width, b.Min.Y, b.Min.X+(i+1)*a.Width, b.Max.Y))
			if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, frame); err != nil {
				return nil, fmt.Errorf("encode desktop %q frame %d: %w", name, i, err)
			}
			h.PNG[name] = append(h.PNG[name], buf.Bytes())
		}
	}
	return h, nil
}

// hiresFiles is the desktop set as mod files, keyed relative to the mod
// folder, and the `hires` field frames.ts carries for it.
func hiresFiles(h *Hires, out map[string][]byte) string {
	names := make([]string, 0, len(h.PNG))
	for name := range h.PNG {
		names = append(names, name)
	}
	sort.Strings(names)
	anims := make([]string, 0, len(names))
	for _, name := range names {
		for i, data := range h.PNG[name] {
			out[fmt.Sprintf("%s/%s/%d.png", hiresDir, name, i)] = data
		}
		anims = append(anims, fmt.Sprintf("%q: %s", name, rectTS(h.Manifest.Animations[name].Rect)))
	}
	m := h.Manifest
	return fmt.Sprintf("hires: { cell: { width: %d, height: %d }, scale: %g, figure: %s, animations: { %s } }",
		m.Cell.Width, m.Cell.Height, m.Scale, rectTS(m.Figure), strings.Join(anims, ", "))
}

func rectTS(r Rect) string {
	return fmt.Sprintf("{ x: %d, y: %d, width: %d, height: %d }", r.X, r.Y, r.Width, r.Height)
}
