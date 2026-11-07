package pet

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"
)

// Codex CLI custom pet layout (openai/codex codex-rs/tui/src/pets/model.rs):
// the spritesheet must be exactly CodexSheetWidth x CodexSheetHeight pixels,
// cut into CodexColumns x CodexRows cells of CodexFrameWidth x
// CodexFrameHeight, indexed row-major from the top left.
const (
	CodexSheetWidth  = 1536
	CodexSheetHeight = 1872
	CodexFrameWidth  = 128
	CodexFrameHeight = 156
	CodexColumns     = 12
	CodexRows        = 12

	// codexFPS plays every animation on a 50 ms tick, the Claude mod's TICK_MS.
	codexFPS    = 20
	codexTickMS = 1000 / codexFPS
	// codexArtWidth and codexArtHeight bound Oxy inside a cell, leaving a
	// little margin at the sides and top.
	codexArtWidth  = 120
	codexArtHeight = 155
)

// CodexAnimation maps one animation name Codex asks for onto one of Oxy's
// animations in the embedded sheet. Loop false plays once, then hands off to
// Fallback.
type CodexAnimation struct {
	Name     string
	Source   string
	Loop     bool
	Fallback string
}

// CodexAnimations is every animation Oxy's pet.json defines: Codex's five
// states first (idle is the default; running while a turn is in progress;
// waiting on approval or input; review when a turn finishes; failed on an
// error), then aliases for the names in Codex's built-in default table, so
// none of them points at a cell that holds no art.
var CodexAnimations = []CodexAnimation{
	{Name: "idle", Source: "blink", Loop: true, Fallback: "idle"},
	{Name: "running", Source: "laptop", Loop: true, Fallback: "idle"},
	{Name: "waiting", Source: "wave", Loop: true, Fallback: "idle"},
	{Name: "review", Source: "confetti", Loop: false, Fallback: "idle"},
	{Name: "failed", Source: "fail", Loop: false, Fallback: "idle"},

	{Name: "running-right", Source: "laptop", Loop: true, Fallback: "idle"},
	{Name: "running-left", Source: "laptop", Loop: true, Fallback: "idle"},
	{Name: "move_right", Source: "laptop", Loop: true, Fallback: "idle"},
	{Name: "move_left", Source: "laptop", Loop: true, Fallback: "idle"},
	{Name: "waving", Source: "wave", Loop: true, Fallback: "idle"},
	{Name: "wave", Source: "wave", Loop: true, Fallback: "idle"},
	{Name: "jumping", Source: "wave", Loop: true, Fallback: "idle"},
	{Name: "bounce", Source: "wave", Loop: true, Fallback: "idle"},
	{Name: "sad", Source: "fail", Loop: false, Fallback: "idle"},
}

// CodexSheet is Oxy as a Codex custom pet: the spritesheet and the pet.json
// that describes it, written side by side in the pet's directory.
type CodexSheet struct {
	// PNG is the CodexSheetWidth x CodexSheetHeight spritesheet.
	PNG []byte
	// Manifest is pet.json, 2-space indented with a trailing newline.
	Manifest []byte
}

// codexPet is pet.json. Field order is the order written; Codex ignores
// OxVersion, which ox reads back to tell whether an install is current.
type codexPet struct {
	ID              string                    `json:"id"`
	DisplayName     string                    `json:"displayName"`
	Description     string                    `json:"description"`
	SpritesheetPath string                    `json:"spritesheetPath"`
	Frame           codexFrame                `json:"frame"`
	Animations      map[string]codexAnimation `json:"animations"`
	OxVersion       string                    `json:"oxVersion"`
}

type codexFrame struct {
	Width   int `json:"width"`
	Height  int `json:"height"`
	Columns int `json:"columns"`
	Rows    int `json:"rows"`
}

type codexAnimation struct {
	Frames   []int  `json:"frames"`
	FPS      int    `json:"fps"`
	Loop     bool   `json:"loop"`
	Fallback string `json:"fallback"`
}

// OxyCodexSheet builds Oxy's Codex pet from the embedded pixel art, stamping
// version into pet.json's oxVersion.
func OxyCodexSheet(version string) (*CodexSheet, error) {
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
	out, err := BuildCodexSheet(sheet, m, version)
	if err != nil {
		return nil, fmt.Errorf("oxy codex pet: %w", err)
	}
	return out, nil
}

// BuildCodexSheet repacks the frames of sheet (laid out per m) that
// CodexAnimations use into a Codex spritesheet, and writes the pet.json
// that plays them. Each source animation's frames get their own cells once,
// however many Codex names alias it; each frame is scaled to fit
// codexArtWidth x codexArtHeight, centered horizontally and resting on the
// cell's floor. Frames are first folded to Codex's 50 ms tick and, when the
// sources outgrow the fixed grid, evenly thinned (fitCells). A cell shown for
// ms milliseconds repeats round(ms/50) times (at least once) at 20 fps.
// Cells left over stay transparent.
func BuildCodexSheet(sheet image.Image, m Manifest, version string) (*CodexSheet, error) {
	cw, ch := m.Cell, m.cellHeight()
	if cw <= 0 || ch <= 0 {
		return nil, fmt.Errorf("sheet cell size must be positive, got %dx%d", cw, ch)
	}
	byName := make(map[string]Animation, len(m.Animations))
	for _, a := range m.Animations {
		byName[a.Name] = a
	}

	scale := math.Min(float64(codexArtWidth)/float64(cw), float64(codexArtHeight)/float64(ch))
	w := min(codexArtWidth, max(1, int(math.Round(float64(cw)*scale))))
	h := min(codexArtHeight, max(1, int(math.Round(float64(ch)*scale))))
	offX, offY := (CodexFrameWidth-w)/2, CodexFrameHeight-h

	b := sheet.Bounds()
	// Gather each source animation once, however many Codex names alias it,
	// folded to Codex's tick, then trim the longest until they fit the grid.
	var sources []Animation
	var plans [][]codexTick
	seen := make(map[string]bool)
	for _, ca := range CodexAnimations {
		if seen[ca.Source] {
			continue
		}
		seen[ca.Source] = true
		a, ok := byName[ca.Source]
		if !ok {
			return nil, fmt.Errorf("codex animation %q: sheet has no %q animation", ca.Name, ca.Source)
		}
		ms, err := a.durations()
		if err != nil {
			return nil, err
		}
		if (a.Row+1)*ch > b.Dy() || a.Frames*cw > b.Dx() {
			return nil, fmt.Errorf("animation %q: row %d with %d frames exceeds the %dx%d sheet", a.Name, a.Row, a.Frames, b.Dx(), b.Dy())
		}
		sources = append(sources, a)
		plans = append(plans, foldToTick(ms, codexTickMS))
	}
	if len(plans) > CodexColumns*CodexRows {
		return nil, fmt.Errorf("%d codex source animations overflow the %d codex cells", len(plans), CodexColumns*CodexRows)
	}
	plans = fitCells(plans, CodexColumns*CodexRows)

	out := image.NewNRGBA(image.Rect(0, 0, CodexSheetWidth, CodexSheetHeight))
	frames := make(map[string][]int) // source animation -> frame index list
	next := 0
	for k, a := range sources {
		var list []int
		for _, tk := range plans[k] {
			src := crop(sheet, image.Rect(tk.Frame*cw, a.Row*ch, (tk.Frame+1)*cw, (a.Row+1)*ch).Add(b.Min))
			art := downscale(src, w, h)
			cell := image.Pt((next%CodexColumns)*CodexFrameWidth+offX, (next/CodexColumns)*CodexFrameHeight+offY)
			draw.Draw(out, image.Rectangle{Min: cell, Max: cell.Add(art.Bounds().Size())}, art, image.Point{}, draw.Src)
			repeats := max(1, int(math.Round(float64(tk.MS)/codexTickMS)))
			for r := 0; r < repeats; r++ {
				list = append(list, next)
			}
			next++
		}
		frames[a.Name] = list
	}

	pet := codexPet{
		ID:              "oxy",
		DisplayName:     "Oxy",
		Description:     "SageOx's mascot: a Highland-cow space pioneer in a cream-and-green suit and bubble helmet.",
		SpritesheetPath: "spritesheet.png",
		Frame:           codexFrame{Width: CodexFrameWidth, Height: CodexFrameHeight, Columns: CodexColumns, Rows: CodexRows},
		Animations:      make(map[string]codexAnimation, len(CodexAnimations)),
		OxVersion:       version,
	}
	for _, ca := range CodexAnimations {
		pet.Animations[ca.Name] = codexAnimation{Frames: frames[ca.Source], FPS: codexFPS, Loop: ca.Loop, Fallback: ca.Fallback}
	}
	manifest, err := json.MarshalIndent(pet, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode codex pet.json: %w", err)
	}

	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.DefaultCompression}).Encode(&buf, out); err != nil {
		return nil, fmt.Errorf("encode codex spritesheet: %w", err)
	}
	return &CodexSheet{PNG: buf.Bytes(), Manifest: append(manifest, '\n')}, nil
}

// CodexManifestVersion reads the ox version a Codex pet.json was built by,
// or "" when it carries none.
func CodexManifestVersion(petJSON []byte) (string, error) {
	var p struct {
		OxVersion string `json:"oxVersion"`
	}
	if err := json.Unmarshal(petJSON, &p); err != nil {
		return "", fmt.Errorf("parse codex pet.json: %w", err)
	}
	return p.OxVersion, nil
}

// codexTick is one Codex cell: the source frame it shows and for how long.
type codexTick struct{ Frame, MS int }

// foldToTick merges frames too short for Codex's tick into the one before
// them, summing durations, while that one is itself still shorter than tick.
// Codex plays at a fixed 20 fps, so a 40 ms frame would round up to a 50 ms
// tick and play slow; and the Claude sheet keeps 40 ms in-betweens that would
// otherwise each spend one of Codex's 144 cells. Folding only while the
// previous tick is short (the rule make-sheet.py used) keeps a run of short
// frames to pairs instead of collapsing it into one long frame.
func foldToTick(ms []int, tick int) []codexTick {
	var out []codexTick
	for i, d := range ms {
		if n := len(out); n > 0 && out[n-1].MS < tick {
			out[n-1].MS += d
			continue
		}
		out = append(out, codexTick{Frame: i, MS: d})
	}
	return out
}

// fitCells thins the plans until their cells total at most cells: each pass
// takes one cell from whichever animation currently has the most (the first
// on a tie), then each animation is evenly subsampled to its share. Codex's
// grid is a fixed 12x12, so a denser source sheet must lose frames here, and
// spreading the loss over the longest animations keeps every one readable.
// Requires len(plans) <= cells.
func fitCells(plans [][]codexTick, cells int) [][]codexTick {
	counts := make([]int, len(plans))
	total := 0
	for i, p := range plans {
		counts[i] = len(p)
		total += len(p)
	}
	for ; total > cells; total-- {
		most := 0
		for i, c := range counts {
			if c > counts[most] {
				most = i
			}
		}
		counts[most]--
	}
	out := make([][]codexTick, len(plans))
	for i, p := range plans {
		out[i] = subsample(p, counts[i])
	}
	return out
}

// subsample keeps n of ticks spread evenly, always the first and the last, and
// gives each dropped tick's time to the kept tick before it, so the animation
// runs as long as before.
func subsample(ticks []codexTick, n int) []codexTick {
	if n >= len(ticks) {
		return ticks
	}
	keep := make([]int, n) // indices into ticks, ascending
	for i := range keep {
		if n > 1 {
			keep[i] = int(math.Round(float64(i*(len(ticks)-1)) / float64(n-1)))
		}
	}
	out := make([]codexTick, n)
	for i, k := range keep {
		end := len(ticks)
		if i+1 < n {
			end = keep[i+1]
		}
		out[i] = codexTick{Frame: ticks[k].Frame}
		for _, t := range ticks[k:end] {
			out[i].MS += t.MS
		}
	}
	return out
}
