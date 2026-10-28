// Package pet installs Oxy, SageOx's mascot, as an animated pet in Claude
// Code's thinking line.
//
// Claude Code's mods run in a sandbox with no image decoder, so ox does the
// pixel work here at install time: it slices the embedded sprite sheet into
// frames and pre-encodes each one twice — as a PNG for terminals that speak
// the kitty graphics protocol (Ghostty, kitty, cmux) and as a grid of
// half-block cells for every other truecolor terminal. The mod only picks
// frames and swaps them.
package pet

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"sync"
)

//go:embed assets/oxy/pixel
var oxyAssets embed.FS

// styleName is the one sprite sheet Oxy is drawn from, under assets/oxy.
const styleName = "pixel"

// Animations are the ones the mod can ask for (hooks/logic.ts ANIMS): what
// Oxy does while Claude works, and one per ox command. Every sheet must
// provide each of them, even if only as one still frame.
var Animations = []string{
	"blink", "wag", "wag-big", "wave", "laptop", "robot", "float", "think", "chatter", "game", "sleep",
	"jump", "confetti", "fail",
	"murmur", "doctor", "prime", "query", "plan-checklist", "plan-flowchart", "plan-kanban",
	"juggle", "laptop-rage", "laptop-tabs", "laptop-bug", "laptop-deploy", "laptop-loading", "laptop-doze",
}

// SizeSpec is a named size the mod can switch to, fixed by its height in
// terminal rows; the width follows the art's aspect.
type SizeSpec struct {
	Name string
	Rows int
}

// Sizes are the boxes `/oxy small|medium|large` draw in, small first.
var Sizes = []SizeSpec{{"small", 8}, {"medium", 12}, {"large", 16}}

// Codepoints and colors of the Raster cell encoding (Claude Code RasterProps).
const (
	upperHalfBlock = 0x2580
	lowerHalfBlock = 0x2584
	space          = 0x20
	defaultColor   = 0x01000000
)

// Manifest is the sprite sheet's layout: cells of Cell by CellHeight pixels
// (square when CellHeight is 0), one animation per row.
type Manifest struct {
	Cell       int         `json:"cell"`
	CellHeight int         `json:"cellHeight,omitempty"`
	Columns    int         `json:"columns"`
	Animations []Animation `json:"animations"`
}

func (m Manifest) cellHeight() int {
	if m.CellHeight > 0 {
		return m.CellHeight
	}
	return m.Cell
}

// Animation is one row of the sheet. Frames show for MS milliseconds each,
// or at a steady FPS when MS is empty.
type Animation struct {
	Name   string `json:"name"`
	Row    int    `json:"row"`
	Frames int    `json:"frames"`
	FPS    int    `json:"fps,omitempty"`
	MS     []int  `json:"ms,omitempty"`
}

// durations is how long each frame shows, in milliseconds, or an error when
// the row's timing is missing or malformed.
func (a Animation) durations() ([]int, error) {
	if a.Frames <= 0 {
		return nil, fmt.Errorf("animation %q: frames must be positive", a.Name)
	}
	if len(a.MS) > 0 {
		if len(a.MS) != a.Frames {
			return nil, fmt.Errorf("animation %q: %d durations for %d frames", a.Name, len(a.MS), a.Frames)
		}
		for _, ms := range a.MS {
			if ms <= 0 {
				return nil, fmt.Errorf("animation %q: durations must be positive", a.Name)
			}
		}
		return a.MS, nil
	}
	if a.FPS <= 0 {
		return nil, fmt.Errorf("animation %q: fps must be positive", a.Name)
	}
	out := make([]int, a.Frames)
	for i := range out {
		out[i] = max(1, 1000/a.FPS)
	}
	return out, nil
}

// Size is a box in terminal cells.
type Size struct {
	Columns int `json:"columns"`
	Rows    int `json:"rows"`
}

// Box is one named size: the picture's box in terminal cells, and the
// half-block grid (each cell shows two pixels stacked, so the grid is
// Cells.Columns x 2*Cells.Rows pixels). Both share the rows; the picture
// box is a little wider, see pictureCellRatio.
type Box struct {
	Size
	Cells Size `json:"cells"`
}

// Frames is what the mod draws from: every animation's frames in both
// encodings, and the boxes they are drawn in. A PNG frame is scaled by the
// terminal to its box; cells are pre-rendered per size.
type Frames struct {
	// Aspect is a frame's width over its height.
	Aspect     float64                  `json:"aspect"`
	Sizes      map[string]Box           `json:"sizes"`
	Animations map[string]AnimationData `json:"animations"`
	// Hires is the desktop's high-resolution set, index-aligned with
	// Animations; nil draws the pixel set everywhere.
	Hires *Hires `json:"-"`
}

// AnimationData holds one animation's frames, index-aligned across encodings.
type AnimationData struct {
	// MS is how long each frame shows, in milliseconds.
	MS []int `json:"ms"`
	// PNG is each frame as a base64 PNG, for an Image element.
	PNG []string `json:"png"`
	// Cells is each frame as a base64 Raster cells payload, per size name.
	Cells map[string][]string `json:"cells"`
}

// OxyManifest parses the embedded sheet manifest.
func OxyManifest() (Manifest, error) {
	data, err := oxyAssets.ReadFile("assets/oxy/" + styleName + "/sheet.json")
	if err != nil {
		return Manifest{}, fmt.Errorf("oxy sheet: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse oxy sheet manifest: %w", err)
	}
	return m, nil
}

// OxyFrames renders every animation of the embedded sheet. The sheet is
// compiled in, so the render is done once per process.
func OxyFrames() (*Frames, error) {
	return oxyFrames()
}

var oxyFrames = sync.OnceValues(func() (*Frames, error) {
	m, err := OxyManifest()
	if err != nil {
		return nil, err
	}
	data, err := oxyAssets.ReadFile("assets/oxy/" + styleName + "/sheet.png")
	if err != nil {
		return nil, fmt.Errorf("oxy sheet: %w", err)
	}
	sheet, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode oxy sheet: %w", err)
	}
	frames, err := BuildFrames(sheet, m)
	if err != nil {
		return nil, fmt.Errorf("oxy: %w", err)
	}
	if frames.Hires, err = oxyHires(m); err != nil {
		return nil, err
	}
	return frames, nil
})

// pictureCellRatio is how many times taller than wide the picture box
// assumes a terminal cell is. Ghostty's default font draws cells about 2.2
// times as tall as wide. The terminal fits a picture inside its box and
// centers whatever is left over, so a box narrower than the art left spare
// rows above and below Oxy. Assuming a slightly tall cell keeps the art
// height-limited on common fonts: Oxy fills every row, his feet sit on the
// caption's row, and any spare space falls at his sides instead.
const pictureCellRatio = 2.25

// BoxFor is the box a frame of the given aspect fills at a size, never
// narrower than one column. Half-block cells are exact: each shows two
// pixels stacked, so the grid holds as many columns as the rows' height at
// that aspect and the art fills it edge to edge. The picture box is widened
// to pictureCellRatio, so the terminal's fit leaves no gap under Oxy.
func BoxFor(aspect float64, s SizeSpec) Box {
	rows := float64(s.Rows)
	cells := Size{Columns: max(1, int(math.Round(rows*2*aspect))), Rows: s.Rows}
	picture := Size{Columns: max(1, int(math.Ceil(rows*pictureCellRatio*aspect))), Rows: s.Rows}
	return Box{Size: picture, Cells: cells}
}

// BuildFrames slices sheet per m and encodes every frame: the PNG at the
// cell's own size, and half-block cells for each of Sizes.
func BuildFrames(sheet image.Image, m Manifest) (*Frames, error) {
	cw, ch := m.Cell, m.cellHeight()
	if cw <= 0 || ch <= 0 {
		return nil, fmt.Errorf("sheet cell size must be positive, got %dx%d", cw, ch)
	}
	b := sheet.Bounds()
	out := &Frames{
		Aspect:     float64(cw) / float64(ch),
		Sizes:      make(map[string]Box, len(Sizes)),
		Animations: make(map[string]AnimationData, len(m.Animations)),
	}
	for _, s := range Sizes {
		out.Sizes[s.Name] = BoxFor(out.Aspect, s)
	}
	for _, a := range m.Animations {
		ms, err := a.durations()
		if err != nil {
			return nil, err
		}
		if (a.Row+1)*ch > b.Dy() || a.Frames*cw > b.Dx() {
			return nil, fmt.Errorf("animation %q: row %d with %d frames exceeds the %dx%d sheet", a.Name, a.Row, a.Frames, b.Dx(), b.Dy())
		}
		data := AnimationData{MS: ms, Cells: make(map[string][]string, len(Sizes))}
		for i := 0; i < a.Frames; i++ {
			frame := crop(sheet, image.Rect(i*cw, a.Row*ch, (i+1)*cw, (a.Row+1)*ch).Add(b.Min))
			pngB64, err := encodePNG(frame)
			if err != nil {
				return nil, fmt.Errorf("animation %q frame %d: %w", a.Name, i, err)
			}
			data.PNG = append(data.PNG, pngB64)
			for name, box := range out.Sizes {
				grid := box.Cells
				data.Cells[name] = append(data.Cells[name], EncodeCells(downscale(frame, grid.Columns, grid.Rows*2), grid.Columns, grid.Rows))
			}
		}
		out.Animations[a.Name] = data
	}
	return out, nil
}

// crop copies r out of src into a fresh NRGBA anchored at the origin.
func crop(src image.Image, r image.Rectangle) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(dst, dst.Bounds(), src, r.Min, draw.Src)
	return dst
}

func encodePNG(img image.Image) (string, error) {
	var buf bytes.Buffer
	// Default compression: BestCompression saves ~3% of bytes at 7x the time.
	enc := png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// downscale area-averages src into a w x h image. Colors are averaged over
// opaque coverage; a target pixel less than half covered is transparent, so
// thin outlines survive without smearing the background into the sprite.
func downscale(src *image.NRGBA, w, h int) *image.NRGBA {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for ty := 0; ty < h; ty++ {
		y0, y1 := ty*sh/h, (ty+1)*sh/h
		for tx := 0; tx < w; tx++ {
			x0, x1 := tx*sw/w, (tx+1)*sw/w
			var r, g, b, a, n uint64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					c := src.NRGBAAt(x, y)
					n++
					if c.A == 0 {
						continue
					}
					a += uint64(c.A)
					r += uint64(c.R) * uint64(c.A)
					g += uint64(c.G) * uint64(c.A)
					b += uint64(c.B) * uint64(c.A)
				}
			}
			if n == 0 || a*2 < n*255 {
				continue
			}
			dst.SetNRGBA(tx, ty, color.NRGBA{R: uint8(r / a), G: uint8(g / a), B: uint8(b / a), A: 255})
		}
	}
	return dst
}

// ImportBase turns one picture of Oxy into the sheet ox embeds: a single
// cell, trimmed to the picture's opaque bounds and scaled to height pixels,
// that every animation shows as its one frame. It is the starting point
// before an animated atlas exists. Used by `go run ./internal/pet/importsheet -base`.
func ImportBase(basePNG []byte, anims []string, height int) ([]byte, []byte, error) {
	if height <= 0 {
		return nil, nil, fmt.Errorf("height must be positive, got %d", height)
	}
	src, err := png.Decode(bytes.NewReader(basePNG))
	if err != nil {
		return nil, nil, fmt.Errorf("decode base image: %w", err)
	}
	img := toNRGBA(src)
	bounds, ok := opaqueBounds(img)
	if !ok {
		return nil, nil, fmt.Errorf("base image is fully transparent")
	}
	trimmed := crop(img, bounds)
	width := max(1, int(math.Round(float64(bounds.Dx())*float64(height)/float64(bounds.Dy()))))
	cell := resample(trimmed, width, height)

	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, cell); err != nil {
		return nil, nil, fmt.Errorf("encode sheet: %w", err)
	}
	m := Manifest{Cell: width, CellHeight: height, Columns: 1}
	for _, name := range anims {
		m.Animations = append(m.Animations, Animation{Name: name, Row: 0, Frames: 1, MS: []int{1000}})
	}
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("encode sheet manifest: %w", err)
	}
	return out.Bytes(), append(manifest, '\n'), nil
}

func toNRGBA(src image.Image) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, src.Bounds().Dx(), src.Bounds().Dy()))
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
	return dst
}

// opaqueBounds is the smallest rectangle holding every pixel that is more
// than faintly visible, and false when there is none.
func opaqueBounds(img *image.NRGBA) (image.Rectangle, bool) {
	const visible = 16
	b := img.Bounds()
	r := image.Rectangle{Min: b.Max, Max: b.Min}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if img.NRGBAAt(x, y).A < visible {
				continue
			}
			r.Min.X, r.Min.Y = min(r.Min.X, x), min(r.Min.Y, y)
			r.Max.X, r.Max.Y = max(r.Max.X, x+1), max(r.Max.Y, y+1)
		}
	}
	return r, r.Min.X < r.Max.X
}

// resample box-averages src into a w x h image, alpha included, so smooth
// edges stay soft. Each target pixel averages the source block it covers.
func resample(src *image.NRGBA, w, h int) *image.NRGBA {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for ty := 0; ty < h; ty++ {
		y0, y1 := ty*sh/h, max(ty*sh/h+1, (ty+1)*sh/h)
		for tx := 0; tx < w; tx++ {
			x0, x1 := tx*sw/w, max(tx*sw/w+1, (tx+1)*sw/w)
			var r, g, b, a uint64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					c := src.NRGBAAt(x, y)
					a += uint64(c.A)
					r += uint64(c.R) * uint64(c.A)
					g += uint64(c.G) * uint64(c.A)
					b += uint64(c.B) * uint64(c.A)
				}
			}
			if a == 0 {
				continue
			}
			n := uint64((y1 - y0) * (x1 - x0))
			dst.SetNRGBA(tx, ty, color.NRGBA{R: uint8(r / a), G: uint8(g / a), B: uint8(b / a), A: uint8(a / n)})
		}
	}
	return dst
}

// ImportSheet turns an artist's sheet into the asset ox embeds, shrinking it
// by an integer factor when the source is drawn larger than a frame needs.
// The manifest's cell size is divided to match. Used by
// `go run ./internal/pet/importsheet`.
func ImportSheet(sheetPNG, sheetJSON []byte, shrinkBy int) ([]byte, []byte, error) {
	if shrinkBy < 1 {
		return nil, nil, fmt.Errorf("shrink factor must be at least 1, got %d", shrinkBy)
	}
	var m map[string]any
	if err := json.Unmarshal(sheetJSON, &m); err != nil {
		return nil, nil, fmt.Errorf("parse sheet manifest: %w", err)
	}
	var layout Manifest
	if err := json.Unmarshal(sheetJSON, &layout); err != nil {
		return nil, nil, fmt.Errorf("parse sheet manifest: %w", err)
	}
	if layout.Cell <= 0 || layout.Cell%shrinkBy != 0 || layout.cellHeight()%shrinkBy != 0 {
		return nil, nil, fmt.Errorf("cell size %dx%d does not divide by %d", layout.Cell, layout.cellHeight(), shrinkBy)
	}
	src, err := png.Decode(bytes.NewReader(sheetPNG))
	if err != nil {
		return nil, nil, fmt.Errorf("decode sheet: %w", err)
	}
	sheet := toNRGBA(src)
	if shrinkBy > 1 {
		sheet = resample(sheet, sheet.Bounds().Dx()/shrinkBy, sheet.Bounds().Dy()/shrinkBy)
	}
	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, sheet); err != nil {
		return nil, nil, fmt.Errorf("encode sheet: %w", err)
	}
	m["cell"] = layout.Cell / shrinkBy
	if layout.CellHeight > 0 {
		m["cellHeight"] = layout.CellHeight / shrinkBy
	}
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("encode sheet manifest: %w", err)
	}
	return out.Bytes(), append(manifest, '\n'), nil
}

// EncodeCells packs img (columns x 2*rows pixels) into Claude Code's Raster
// cells payload: row-major little-endian u32 triplets
// [codePoint, foreground, background], base64. Each cell shows two pixels
// stacked with half blocks; a transparent pixel takes the terminal's default.
func EncodeCells(img *image.NRGBA, columns, rows int) string {
	buf := make([]byte, 0, columns*rows*12)
	put := func(v uint32) { buf = binary.LittleEndian.AppendUint32(buf, v) }
	for row := 0; row < rows; row++ {
		for col := 0; col < columns; col++ {
			top, topOK := pixel(img, col, row*2)
			bottom, bottomOK := pixel(img, col, row*2+1)
			switch {
			case topOK && bottomOK:
				put(upperHalfBlock)
				put(top)
				put(bottom)
			case topOK:
				put(upperHalfBlock)
				put(top)
				put(defaultColor)
			case bottomOK:
				put(lowerHalfBlock)
				put(bottom)
				put(defaultColor)
			default:
				put(space)
				put(defaultColor)
				put(defaultColor)
			}
		}
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// pixel returns the 0x00RRGGBB color at (x, y) and whether it is opaque.
func pixel(img *image.NRGBA, x, y int) (uint32, bool) {
	if !(image.Point{X: x, Y: y}.In(img.Bounds())) {
		return 0, false
	}
	c := img.NRGBAAt(x, y)
	if c.A < 128 {
		return 0, false
	}
	return uint32(c.R)<<16 | uint32(c.G)<<8 | uint32(c.B), true
}
