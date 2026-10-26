package pet

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. The embedded sheet renders every animation the mod can ask for ---

// TestOxyFrames_EveryAnimationRendersInBothEncodings verifies each manifest
// animation yields one decodable PNG per frame and one well-formed cell grid
// per frame and size.
// Failure prevented: the mod indexes a frame that isn't there, or the terminal
// refuses a Raster whose cell count doesn't match its box, and Oxy vanishes.
func TestOxyFrames_EveryAnimationRendersInBothEncodings(t *testing.T) {
	if testing.Short() {
		t.Skip("short: renders every Oxy animation")
	}
	frames, err := OxyFrames()
	require.NoError(t, err)
	m, err := OxyManifest()
	require.NoError(t, err)

	// the mod's animation names (hooks/logic.ts ANIMS) must all exist
	for _, name := range Animations {
		assert.Contains(t, frames.Animations, name)
	}
	require.Len(t, frames.Animations, len(m.Animations))
	require.Len(t, frames.Sizes, len(Sizes))
	assert.InDelta(t, float64(m.Cell)/float64(m.cellHeight()), frames.Aspect, 1e-9)

	for _, a := range m.Animations {
		got := frames.Animations[a.Name]
		require.Len(t, got.PNG, a.Frames, a.Name)
		require.Len(t, got.MS, a.Frames, a.Name)
		for _, ms := range got.MS {
			assert.Positive(t, ms, a.Name)
		}
		for i := range got.PNG {
			raw, err := base64.StdEncoding.DecodeString(got.PNG[i])
			require.NoError(t, err)
			img, err := png.Decode(bytes.NewReader(raw))
			require.NoError(t, err, "%s frame %d", a.Name, i)
			assert.Equal(t, image.Pt(m.Cell, m.cellHeight()), img.Bounds().Size(), "a frame is its cell, unscaled")
		}
		for _, s := range Sizes {
			grid := frames.Sizes[s.Name].Cells
			require.Len(t, got.Cells[s.Name], a.Frames, "%s %s", a.Name, s.Name)
			for i, b64 := range got.Cells[s.Name] {
				cells, err := base64.StdEncoding.DecodeString(b64)
				require.NoError(t, err)
				require.Len(t, cells, grid.Columns*grid.Rows*12, "%s %s frame %d", a.Name, s.Name, i)
				for c := 0; c < len(cells); c += 12 {
					cp := binary.LittleEndian.Uint32(cells[c:])
					assert.Contains(t, []uint32{space, upperHalfBlock, lowerHalfBlock}, cp)
				}
			}
		}
	}
}

// TestOxyFrames_SpriteIsVisible guards against an all-transparent render (a
// wrong crop offset or alpha threshold) that would draw an empty box, at
// every size the mod can draw.
func TestOxyFrames_SpriteIsVisible(t *testing.T) {
	if testing.Short() {
		t.Skip("short: renders every Oxy animation")
	}
	frames, err := OxyFrames()
	require.NoError(t, err)
	for _, s := range Sizes {
		grid := frames.Sizes[s.Name].Cells
		cells, err := base64.StdEncoding.DecodeString(frames.Animations[Animations[0]].Cells[s.Name][0])
		require.NoError(t, err)
		painted := 0
		for c := 0; c < len(cells); c += 12 {
			if binary.LittleEndian.Uint32(cells[c:]) != space {
				painted++
			}
		}
		// the stage keeps headroom for effects, so Oxy himself fills the lower part
		assert.Greater(t, painted, grid.Columns*grid.Rows/8, "%s: a good share of Oxy's box should be painted", s.Name)
	}
}

// TestBoxFor_FollowsTheArtsAspect verifies a size's width is derived from its
// rows and the frame's aspect, so a tall base image and a near-square atlas
// both fill their box without stretching. The half-block grid holds two
// pixels per cell, and the picture box is wide enough for a cell 2.25 times
// as tall as wide.
// Failure prevented: a sheet swap squashes Oxy into the old box shape,
// terminals without pictures draw him twice as big as the size asked for, or
// a picture box narrower than the art leaves a gap under Oxy in Ghostty.
func TestBoxFor_FollowsTheArtsAspect(t *testing.T) {
	tests := []struct {
		name   string
		aspect float64
		spec   SizeSpec
		want   Box
	}{
		{"tall base, small", 0.5, SizeSpec{"small", 4}, Box{Size{5, 4}, Size{4, 4}}},
		{"tall base, large", 0.5, SizeSpec{"large", 8}, Box{Size{9, 8}, Size{8, 8}}},
		{"codex atlas, small", 192.0 / 208, SizeSpec{"small", 4}, Box{Size{9, 4}, Size{7, 4}}},
		{"codex atlas, medium", 192.0 / 208, SizeSpec{"medium", 6}, Box{Size{13, 6}, Size{11, 6}}},
		{"square, large", 1, SizeSpec{"large", 8}, Box{Size{18, 8}, Size{16, 8}}},
		{"never narrower than a column", 0.01, SizeSpec{"small", 4}, Box{Size{1, 4}, Size{1, 4}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, BoxFor(tt.aspect, tt.spec))
		})
	}
}

// --- B. Cell encoding matches Claude Code's Raster contract ---

func TestEncodeCells_HalfBlocksAndTerminalDefault(t *testing.T) {
	red := color.NRGBA{R: 0xff, A: 0xff}
	blue := color.NRGBA{B: 0xff, A: 0xff}
	clear := color.NRGBA{}
	tests := []struct {
		name        string
		top, bottom color.NRGBA
		cp, fg, bg  uint32
	}{
		{"both opaque", red, blue, upperHalfBlock, 0xff0000, 0x0000ff},
		{"top only", red, clear, upperHalfBlock, 0xff0000, defaultColor},
		{"bottom only", clear, blue, lowerHalfBlock, 0x0000ff, defaultColor},
		{"neither", clear, clear, space, defaultColor, defaultColor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img := image.NewNRGBA(image.Rect(0, 0, 1, 2))
			img.SetNRGBA(0, 0, tt.top)
			img.SetNRGBA(0, 1, tt.bottom)
			raw, err := base64.StdEncoding.DecodeString(EncodeCells(img, 1, 1))
			require.NoError(t, err)
			require.Len(t, raw, 12)
			assert.Equal(t, tt.cp, binary.LittleEndian.Uint32(raw[0:]))
			assert.Equal(t, tt.fg, binary.LittleEndian.Uint32(raw[4:]))
			assert.Equal(t, tt.bg, binary.LittleEndian.Uint32(raw[8:]))
		})
	}
}

func TestBuildFrames_RejectsAnimationOutsideSheet(t *testing.T) {
	sheet := image.NewNRGBA(image.Rect(0, 0, 128, 64))
	tests := []struct {
		name string
		m    Manifest
	}{
		{"row past the bottom", Manifest{Cell: 64, Animations: []Animation{{Name: "x", Row: 1, Frames: 1, FPS: 1}}}},
		{"frames past the right edge", Manifest{Cell: 64, Animations: []Animation{{Name: "x", Row: 0, Frames: 3, FPS: 1}}}},
		{"tall cell past the bottom", Manifest{Cell: 32, CellHeight: 65, Animations: []Animation{{Name: "x", Row: 0, Frames: 1, FPS: 1}}}},
		{"no frames", Manifest{Cell: 64, Animations: []Animation{{Name: "x", Row: 0, Frames: 0, FPS: 1}}}},
		{"no fps and no durations", Manifest{Cell: 64, Animations: []Animation{{Name: "x", Row: 0, Frames: 1}}}},
		{"durations do not match frames", Manifest{Cell: 64, Animations: []Animation{{Name: "x", Row: 0, Frames: 2, MS: []int{100}}}}},
		{"a zero duration", Manifest{Cell: 64, Animations: []Animation{{Name: "x", Row: 0, Frames: 1, MS: []int{0}}}}},
		{"no cell", Manifest{Animations: []Animation{{Name: "x", Row: 0, Frames: 1, FPS: 1}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildFrames(sheet, tt.m)
			assert.Error(t, err)
		})
	}
}

// TestBuildFrames_RectangularCellsCutWholeFrames verifies a sheet whose cells
// are taller than wide is cut on its own grid: each frame is the full cell and
// the aspect follows it.
// Failure prevented: a 192x208 atlas cut as 192x192 loses Oxy's feet.
func TestBuildFrames_RectangularCellsCutWholeFrames(t *testing.T) {
	sheet := image.NewNRGBA(image.Rect(0, 0, 4, 6))
	sheet.SetNRGBA(1, 5, color.NRGBA{R: 0xff, A: 0xff}) // bottom row of the second frame's column... first cell, last row
	m := Manifest{Cell: 2, CellHeight: 3, Animations: []Animation{{Name: "idle", Row: 1, Frames: 2, MS: []int{700, 50}}}}
	frames, err := BuildFrames(sheet, m)
	require.NoError(t, err)
	assert.InDelta(t, 2.0/3, frames.Aspect, 1e-9)
	assert.Equal(t, []int{700, 50}, frames.Animations["idle"].MS, "per-frame timing passes through")
	raw, err := base64.StdEncoding.DecodeString(frames.Animations["idle"].PNG[0])
	require.NoError(t, err)
	img, err := png.Decode(bytes.NewReader(raw))
	require.NoError(t, err)
	assert.Equal(t, image.Pt(2, 3), img.Bounds().Size())
	assert.Equal(t, color.NRGBA{R: 0xff, A: 0xff}, color.NRGBAModel.Convert(img.At(1, 2)), "the cell's last row is part of the frame")
}

// --- C. Generated files fit what Claude Code will load ---

// TestFramesFiles_StayUnderClaudeLimits verifies the frames land outside the
// module graph: the only generated module is small, every frame is a PNG file
// the terminal can open by path, and each cells.json fits one `$.fs.read`.
// Failure prevented: `claude plugin validate` refuses the mod ("takes the
// module over 8388608 bytes in total") and enable fails for everyone.
func TestFramesFiles_StayUnderClaudeLimits(t *testing.T) {
	t.Run("tiny sheet", func(t *testing.T) { checkFramesFiles(t, tinyFrames) })
	t.Run("embedded art", func(t *testing.T) {
		if testing.Short() {
			t.Skip("short: renders every Oxy animation")
		}
		checkFramesFiles(t, OxyFrames)
	})
}

// svgSourceCap is Claude Code's limit on an Svg element's source;
// svgWrapperChars is the markup around a frame's base64 data.
const (
	svgSourceCap    = 131072
	svgWrapperChars = 300
)

func checkFramesFiles(t *testing.T, render func() (*Frames, error)) {
	t.Helper()
	frames, err := render()
	require.NoError(t, err)
	files, err := FramesFiles(frames)
	require.NoError(t, err)

	require.Contains(t, files, framesFile)
	assert.Less(t, len(files[framesFile]), 64<<10, "the index holds timing and sizes only")
	index := string(files[framesFile])
	pngs := 0
	for rel, data := range files {
		switch {
		case rel == framesFile:
		case strings.HasSuffix(rel, ".png"):
			pngs++
			_, err := png.Decode(bytes.NewReader(data))
			assert.NoError(t, err, rel)
			// The desktop and VS Code surfaces draw each frame as an Svg
			// element wrapping the PNG as a base64 data URI, and Claude Code
			// caps an Svg source at 131,072 chars (SvgProps in the plugin
			// API). A frame over it would vanish there while the terminal
			// still shows it. 300 covers the SVG wrapper and data-URI prefix.
			assert.Less(t, (len(data)+2)/3*4+svgWrapperChars, svgSourceCap,
				"%s is too large to draw as an Svg data URI on the desktop", rel)
		case strings.HasSuffix(rel, "/cells.json"):
			assert.Less(t, len(data), 4<<20, rel)
			var cells map[string][]string
			require.NoError(t, json.Unmarshal(data, &cells), rel)
			for _, s := range Sizes {
				assert.NotEmpty(t, cells[s.Name], "%s lacks %s cells", rel, s.Name)
			}
		default:
			t.Errorf("unexpected generated file %s", rel)
		}
	}
	want := 0
	for name, a := range frames.Animations {
		want += len(a.PNG)
		assert.Contains(t, files, framesDir+"/"+name+"/0.png")
		assert.Contains(t, files, framesDir+"/"+name+"/cells.json")
		ms, _ := json.Marshal(a.MS)
		assert.Contains(t, index, "\""+name+"\": { ms: "+string(ms)+" }")
	}
	assert.Equal(t, want, pngs, "one PNG file per frame")
	for _, s := range Sizes {
		box := frames.Sizes[s.Name]
		assert.Contains(t, index, s.Name+": { columns: "+itoa(box.Columns)+", rows: "+itoa(box.Rows)+", cells: { columns: "+itoa(box.Cells.Columns)+", rows: "+itoa(box.Cells.Rows)+" } }")
	}
}

func itoa(n int) string {
	data, _ := json.Marshal(n)
	return string(data)
}

// --- D. Writing the mod ---

// TestWriteMod_StampsVersionAndDropsStaleFiles verifies a rewrite after an ox
// upgrade replaces the whole folder: the new version is stamped and files the
// new mod no longer ships are gone.
// Failure prevented: a stale hooks module from an old ox keeps loading.
func TestWriteMod_StampsVersionAndDropsStaleFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pets", "claude-code", "oxy")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "hooks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks", "stale.ts"), []byte("old"), 0o644))

	require.NoError(t, Installer{Dir: dir, Version: "9.9.9", Frames: tinyFrames}.WriteMod())

	v, err := WrittenVersion(dir)
	require.NoError(t, err)
	assert.Equal(t, "9.9.9", v)
	assert.NoFileExists(t, filepath.Join(dir, "hooks", "stale.ts"))
	for _, rel := range []string{".claude-plugin/marketplace.json", "hooks/hooks.json", "hooks/register.tsx", "hooks/logic.ts", framesFile, "types/index.d.ts"} {
		assert.FileExists(t, filepath.Join(dir, filepath.FromSlash(rel)))
	}
	plugin, err := os.ReadFile(filepath.Join(dir, ".claude-plugin", "plugin.json"))
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(plugin), "{{VERSION}}"), "placeholder left in plugin.json")

	entries, err := os.ReadDir(filepath.Dir(dir))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "staging and backup folders must not be left behind")
}

// --- E. Importing art ---

// TestImportBase_TrimsAndListsEveryAnimation verifies one still picture
// becomes a one-cell sheet cut to Oxy's opaque bounds, scaled to the asked
// height at the picture's own aspect, and a manifest that gives every
// animation that frame.
// Failure prevented: a base image with wide transparent margins draws Oxy
// tiny in a mostly empty box, or an animation the mod asks for is missing.
func TestImportBase_TrimsAndListsEveryAnimation(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 20, 20))
	for y := 4; y < 16; y++ { // a 4-wide, 12-tall opaque bar with margins all round
		for x := 8; x < 12; x++ {
			src.SetNRGBA(x, y, color.NRGBA{G: 200, A: 255})
		}
	}
	src.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 5}) // a ghost pixel in the corner must not widen the trim
	var in bytes.Buffer
	require.NoError(t, png.Encode(&in, src))

	outPNG, outJSON, err := ImportBase(in.Bytes(), []string{"idle", "working"}, 6)
	require.NoError(t, err)

	img, err := png.Decode(bytes.NewReader(outPNG))
	require.NoError(t, err)
	assert.Equal(t, image.Pt(2, 6), img.Bounds().Size(), "trimmed to the bar, scaled to height 6 at aspect 1:3")
	assert.Equal(t, color.NRGBA{G: 200, A: 255}, color.NRGBAModel.Convert(img.At(0, 0)))

	var got Manifest
	require.NoError(t, json.Unmarshal(outJSON, &got))
	assert.Equal(t, Manifest{Cell: 2, CellHeight: 6, Columns: 1, Animations: []Animation{
		{Name: "idle", Row: 0, Frames: 1, MS: []int{1000}},
		{Name: "working", Row: 0, Frames: 1, MS: []int{1000}},
	}}, got)
}

func TestImportBase_RejectsEmptyPicture(t *testing.T) {
	var in bytes.Buffer
	require.NoError(t, png.Encode(&in, image.NewNRGBA(image.Rect(0, 0, 3, 3))))
	_, _, err := ImportBase(in.Bytes(), Animations, 8)
	assert.ErrorContains(t, err, "transparent")
}

// TestImportSheet_ShrinksAndRescalesManifest verifies a 2x import halves the
// sheet, halves the manifest's cell size, keeps every other manifest field, and
// averages color and alpha (soft edges survive).
// Failure prevented: an imported sheet whose cell size no longer matches its
// pixels, so every frame is cropped from the wrong place.
func TestImportSheet_ShrinksAndRescalesManifest(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	src.SetNRGBA(0, 0, color.NRGBA{R: 200, A: 255}) // one of four pixels in the first block
	for x := 2; x < 4; x++ {
		for y := 0; y < 2; y++ {
			src.SetNRGBA(x, y, color.NRGBA{B: 100, A: 255})
		}
	}
	var in bytes.Buffer
	require.NoError(t, png.Encode(&in, src))
	manifest := []byte(`{"cell": 2, "columns": 2, "animations": [{"name": "idle", "row": 0, "frames": 2, "fps": 6}]}`)

	outPNG, outJSON, err := ImportSheet(in.Bytes(), manifest, 2)
	require.NoError(t, err)

	img, err := png.Decode(bytes.NewReader(outPNG))
	require.NoError(t, err)
	require.Equal(t, image.Pt(2, 1), img.Bounds().Size())
	soft := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA)
	assert.Equal(t, color.NRGBA{R: 200, A: 63}, soft, "a quarter-covered block keeps its color at a quarter alpha")
	assert.Equal(t, color.NRGBA{B: 100, A: 255}, color.NRGBAModel.Convert(img.At(1, 0)))

	var got Manifest
	require.NoError(t, json.Unmarshal(outJSON, &got))
	assert.Equal(t, 1, got.Cell)
	assert.Equal(t, []Animation{{Name: "idle", Row: 0, Frames: 2, FPS: 6}}, got.Animations)
	fr, err := BuildFrames(image.NewNRGBA(image.Rect(0, 0, 2, 1)), got)
	require.NoError(t, err)
	assert.Equal(t, []int{166, 166}, fr.Animations["idle"].MS, "a steady fps becomes per-frame durations")
}

func TestImportSheet_RejectsBadFactors(t *testing.T) {
	var in bytes.Buffer
	require.NoError(t, png.Encode(&in, image.NewNRGBA(image.Rect(0, 0, 3, 3))))
	for _, k := range []int{0, 2} {
		_, _, err := ImportSheet(in.Bytes(), []byte(`{"cell": 3, "animations": []}`), k)
		assert.Error(t, err, "factor %d", k)
	}
	_, _, err := ImportSheet(in.Bytes(), []byte(`{"cell": 2, "cellHeight": 3, "animations": []}`), 2)
	assert.Error(t, err, "a cell height that does not divide")
}

// TestOxyManifest_ListsEveryAnimation reads the embedded manifest without
// rendering, so short runs still check the sheet ships every animation.
func TestOxyManifest_ListsEveryAnimation(t *testing.T) {
	m, err := OxyManifest()
	require.NoError(t, err)
	names := map[string]bool{}
	for _, a := range m.Animations {
		names[a.Name] = true
	}
	for _, want := range Animations {
		assert.True(t, names[want], want)
	}
}

func TestImport_RejectsMalformedInput(t *testing.T) {
	var good bytes.Buffer
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	img.SetNRGBA(1, 1, color.NRGBA{R: 1, A: 255})
	require.NoError(t, png.Encode(&good, img))
	for name, call := range map[string]func() error{
		"base: not a png":   func() error { _, _, err := ImportBase([]byte("x"), Animations, 8); return err },
		"base: no height":   func() error { _, _, err := ImportBase(good.Bytes(), Animations, 0); return err },
		"sheet: not a png":  func() error { _, _, err := ImportSheet([]byte("x"), []byte(`{"cell": 2}`), 1); return err },
		"sheet: bad json":   func() error { _, _, err := ImportSheet(good.Bytes(), []byte(`{`), 1); return err },
		"sheet: bad layout": func() error { _, _, err := ImportSheet(good.Bytes(), []byte(`{"cell": "x"}`), 1); return err },
	} {
		t.Run(name, func(t *testing.T) { assert.Error(t, call()) })
	}
}

// TestImportSheet_ShrinksRectangularCells verifies a shrink scales both cell
// dimensions of a rectangular sheet.
func TestImportSheet_ShrinksRectangularCells(t *testing.T) {
	var in bytes.Buffer
	require.NoError(t, png.Encode(&in, image.NewNRGBA(image.Rect(0, 0, 4, 8))))
	_, out, err := ImportSheet(in.Bytes(), []byte(`{"cell": 4, "cellHeight": 8, "animations": []}`), 2)
	require.NoError(t, err)
	var m Manifest
	require.NoError(t, json.Unmarshal(out, &m))
	assert.Equal(t, 2, m.Cell)
	assert.Equal(t, 4, m.CellHeight)
}
