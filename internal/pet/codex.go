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
	codexFPS = 20
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
// cell's floor. A frame shown for ms milliseconds repeats round(ms/50)
// times (at least once) at 20 fps. Cells left over stay transparent.
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
	out := image.NewNRGBA(image.Rect(0, 0, CodexSheetWidth, CodexSheetHeight))
	frames := make(map[string][]int) // source animation -> frame index list
	next := 0
	for _, ca := range CodexAnimations {
		if _, done := frames[ca.Source]; done {
			continue
		}
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
		if next+a.Frames > CodexColumns*CodexRows {
			return nil, fmt.Errorf("animation %q: %d frames overflow the %d codex cells", a.Name, a.Frames, CodexColumns*CodexRows)
		}
		var list []int
		for i := 0; i < a.Frames; i++ {
			src := crop(sheet, image.Rect(i*cw, a.Row*ch, (i+1)*cw, (a.Row+1)*ch).Add(b.Min))
			art := downscale(src, w, h)
			cell := image.Pt((next%CodexColumns)*CodexFrameWidth+offX, (next/CodexColumns)*CodexFrameHeight+offY)
			draw.Draw(out, image.Rectangle{Min: cell, Max: cell.Add(art.Bounds().Size())}, art, image.Point{}, draw.Src)
			repeats := max(1, int(math.Round(float64(ms[i])*codexFPS/1000)))
			for r := 0; r < repeats; r++ {
				list = append(list, next)
			}
			next++
		}
		frames[ca.Source] = list
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
