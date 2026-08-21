package prheader

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"text/template"
)

// stripTheme is the two-color set for one theme of the baked enrichment strip.
// Values are the SageOx design tokens the perceptual pass selected for legibility
// on each canvas: transparent background, so the strip sits on GitHub's own PR
// surface in either mode.
type stripTheme struct {
	Text  string // primary label ink
	Sage  string // accent: the "guided" mark + numerals
	Muted string // separators
}

var stripThemes = map[string]stripTheme{
	// Light canvas: Silt-900 ink + Sage-700 accent (AA on white).
	"light": {Text: "#161812", Sage: "#4e7d4c", Muted: "#8c8e7e"},
	// Dark canvas: Dust-100 ink + Sage-300 accent (tuned for dark).
	"dark": {Text: "#f4f2ef", Sage: "#adcfa3", Muted: "#6b6d60"},
}

// Strip geometry. The SVG is authored at 2x the display height so it stays crisp
// on hidpi: it renders in the PR at height=16 (stripHeight) from a 32px-tall
// canvas. Widths are estimated from glyph advance (this package has no
// font-shaping dependency, matching planhero) — deliberately generous so text
// never clips.
const (
	stripCanvasHeight = 32
	stripBaseline     = 22 // text baseline within the 32px canvas
	stripPadX         = 8
	stripDotR         = 5
	stripFontSize     = 18
	// Tuned: ~10px per rune is a conservative advance for the 18px semibold
	// label; the real face varies but over-estimating only adds harmless right
	// padding, never a clip.
	stripPxPerRune = 10
)

// stripTemplateSrc mirrors planhero's approach: text/template over pre-escaped,
// pre-computed values (no escaping in the template itself). "GUIDED BY SAGEOX" in
// a Space-Grotesk-first stack as small-caps (uppercased + letter-spacing), a sage
// "guided" dot as the only mark, then the zero-skipped count phrase with
// sage-colored numerals — a real data-ink glyph the PR-body text sanitizer could
// never allow.
const stripTemplateSrc = `<svg xmlns="http://www.w3.org/2000/svg" width="{{.Width}}" height="{{.Height}}" viewBox="0 0 {{.Width}} {{.Height}}" role="img" aria-label="{{.Alt}}">
  <circle cx="{{.DotX}}" cy="{{.DotCY}}" r="{{.DotR}}" fill="{{.Sage}}"/>
  <text x="{{.TextX}}" y="{{.Baseline}}" font-family="'Space Grotesk', ui-sans-serif, system-ui, sans-serif" font-size="{{.FontSize}}" font-weight="600" letter-spacing="1.5" fill="{{.Text}}">{{.Label}}</text>
</svg>
`

type stripView struct {
	Width, Height          int
	DotX, DotCY, DotR      int
	TextX, Baseline        int
	FontSize               int
	Text, Sage, Alt, Label string
}

// RenderStripSVG bakes the Tier-B enrichment strip for one theme ("light" or
// "dark"; an unknown theme falls back to light). Pure and deterministic — no
// I/O — so it is table-testable. The label is the whisper sentence rendered in
// small-caps; the accompanying alt (same content) is set by the embedding
// <img>, but is duplicated into aria-label here so the standalone SVG is also
// accessible.
func RenderStripSVG(s Signals, theme string) ([]byte, error) {
	th, ok := stripThemes[strings.ToLower(strings.TrimSpace(theme))]
	if !ok {
		th = stripThemes["light"]
	}

	// Small-caps look without a font dependency: uppercase the sentence and let
	// letter-spacing open it up. Uses the same whisper source as the text tier.
	label := strings.ToUpper(whisperAlt(s))

	// Width = left pad + dot + gap + estimated label advance + right pad.
	labelW := len([]rune(label)) * stripPxPerRune
	dotX := stripPadX + stripDotR
	textX := dotX + stripDotR + 6
	width := textX + labelW + stripPadX

	view := stripView{
		Width:    width,
		Height:   stripCanvasHeight,
		DotX:     dotX,
		DotCY:    stripCanvasHeight / 2,
		DotR:     stripDotR,
		TextX:    textX,
		Baseline: stripBaseline,
		FontSize: stripFontSize,
		Text:     th.Text,
		Sage:     th.Sage,
		Alt:      escapeHTML(whisperAlt(s)),
		Label:    escapeHTML(label),
	}

	tmpl, err := template.New("strip").Parse(stripTemplateSrc)
	if err != nil {
		return nil, fmt.Errorf("parse strip template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, view); err != nil {
		return nil, fmt.Errorf("execute strip template: %w", err)
	}
	return buf.Bytes(), nil
}

// stripContentName derives a stable, content-addressed base filename for the
// strip pair so the uploader can key immutable CDN objects on it (identical
// signals + theme => identical bytes => identical URL, no per-PR churn for the
// same numbers). The caller appends the theme + extension.
func stripContentName(s Signals) string {
	return "enrich-" + strconv.Itoa(s.PriorArt) + "-" + strconv.Itoa(s.Collisions)
}
