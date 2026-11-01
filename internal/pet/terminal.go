package pet

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/theme"
)

// TerminalArt is one still of Oxy drawn as half-block text, ready to
// sit beside a few lines of CLI output.
type TerminalArt struct {
	// Lines holds one string per terminal row. Every line is Columns cells wide,
	// and each cell resets its own style, so the caller may append text to it.
	Lines []string
	// Columns is the width of every line, in terminal cells.
	Columns int
}

// armsReach is how far past the standing figure, in desktop-set pixels, a pose
// with raised arms (confetti, jump, wave) still reaches, sideways and upward. A
// crop to the standing figure alone would clip the hands and the helmet.
const armsReach = 30

// TerminalFrame draws one still of animation as half-block text, rows terminal
// rows tall. at picks the frame as a fraction of the loop: 0 is the first
// frame, 1 the last.
//
// It reads the desktop set rather than the pixel sheet. The pixel sheet is a
// 16320x5796 PNG that decodes to hundreds of megabytes and most of a second,
// which is fine once at `ox pet enable` and wrong for a CLI success line; the
// desktop strip for one animation decodes in tens of milliseconds.
//
// Colors go through theme.Adapt, so a 256-color terminal gets palette codes.
// Without color (NO_COLOR, a pipe) a half block is a solid glyph that draws no
// picture at all: callers must check theme.Profile themselves and print text
// alone then.
func TerminalFrame(animation string, at float64, rows int) (*TerminalArt, error) {
	if rows <= 0 {
		return nil, fmt.Errorf("rows must be positive, got %d", rows)
	}
	if at < 0 || at > 1 || math.IsNaN(at) {
		return nil, fmt.Errorf("frame position must be within [0, 1], got %v", at)
	}

	raw, err := desktopAssets.ReadFile(desktopDir + "/manifest.json")
	if err != nil {
		return nil, fmt.Errorf("oxy desktop set: %w", err)
	}
	var manifest HiresManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("parse desktop manifest: %w", err)
	}
	anim, ok := manifest.Animations[animation]
	if !ok {
		return nil, fmt.Errorf("oxy has no %q animation", animation)
	}
	frames := len(anim.MS)
	if frames == 0 {
		frames = anim.Frames
	}
	if frames <= 0 || anim.Width <= 0 || anim.Height <= 0 {
		return nil, fmt.Errorf("desktop animation %q has no frames to draw", animation)
	}

	// the figure's box, in the strip's own pixels: the strip is cropped to the
	// animation's rect, which sits at (anim.X, anim.Y) in the shared cell
	figure := image.Rect(
		manifest.Figure.X-armsReach, manifest.Figure.Y-armsReach,
		manifest.Figure.X+manifest.Figure.Width+armsReach, manifest.Figure.Y+manifest.Figure.Height,
	).Intersect(image.Rect(anim.X, anim.Y, anim.X+anim.Width, anim.Y+anim.Height))
	if figure.Empty() {
		return nil, fmt.Errorf("desktop animation %q does not show the figure", animation)
	}
	figure = figure.Sub(image.Pt(anim.X, anim.Y))

	data, err := desktopAssets.ReadFile(desktopDir + "/" + animation + ".png")
	if err != nil {
		return nil, fmt.Errorf("desktop animation %q: %w", animation, err)
	}
	strip, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode desktop strip %q: %w", animation, err)
	}
	if b := strip.Bounds(); b.Dx() != anim.Width*frames || b.Dy() != anim.Height {
		return nil, fmt.Errorf("desktop strip %q is %dx%d, want %d frames of %dx%d", animation, b.Dx(), b.Dy(), frames, anim.Width, anim.Height)
	}

	index := min(frames-1, int(math.Round(at*float64(frames-1))))
	frame := figure.Add(image.Pt(index*anim.Width, 0)).Add(strip.Bounds().Min)
	cell := crop(strip, frame)

	// each cell is two pixels tall, so the grid is as wide as the figure's
	// aspect needs at 2*rows pixels of height
	columns := max(1, int(math.Round(float64(rows)*2*float64(cell.Bounds().Dx())/float64(cell.Bounds().Dy()))))
	grid := downscale(cell, columns, rows*2)
	return &TerminalArt{Lines: halfBlockLines(grid, columns, rows), Columns: columns}, nil
}

// halfBlockLines turns a columns x 2*rows pixel grid into styled text. Each
// cell shows two stacked pixels: the upper one as the foreground of an upper
// half block, the lower one as its background. A transparent pixel keeps the
// terminal's own color.
func halfBlockLines(img *image.NRGBA, columns, rows int) []string {
	lines := make([]string, rows)
	for row := 0; row < rows; row++ {
		var line strings.Builder
		for col := 0; col < columns; col++ {
			top, topOK := pixel(img, col, row*2)
			bottom, bottomOK := pixel(img, col, row*2+1)
			switch {
			case topOK && bottomOK:
				line.WriteString(lipgloss.NewStyle().Foreground(adapted(top)).Background(adapted(bottom)).Render("▀"))
			case topOK:
				line.WriteString(lipgloss.NewStyle().Foreground(adapted(top)).Render("▀"))
			case bottomOK:
				line.WriteString(lipgloss.NewStyle().Foreground(adapted(bottom)).Render("▄"))
			default:
				line.WriteByte(' ')
			}
		}
		lines[row] = line.String()
	}
	return lines
}

// adapted is a pixel's 0x00RRGGBB color, degraded to what the terminal can show.
func adapted(rgb uint32) color.Color {
	return theme.Adapt(color.RGBA{R: uint8(rgb >> 16), G: uint8(rgb >> 8), B: uint8(rgb), A: 255})
}
