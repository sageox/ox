package main

import (
	"io"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/ui"
)

// rootHeaderGap separates the SageOx mark from the header text beside it.
const rootHeaderGap = "  "

// rootHeaderLines is the text of the root help header: name and tagline.
func rootHeaderLines() []string {
	return []string{
		cli.StyleBrand.Render("ox") + " - Shared team context for AI and human coworkers",
		cli.StyleDim.Render("Making agentic engineering multiplayer."),
	}
}

// rootHeaderArt is the SageOx mark for the root help header, or nil when it
// may not be drawn.
func rootHeaderArt(out io.Writer, width int) []string {
	return rootHeaderArtFor(currentLoginArtEnv(out), width)
}

// rootHeaderArtFor is rootHeaderArt for a given audience: only under the
// `new_logo` brand rollout, only for a person at a color terminal (the same
// rule as `ox login`: never a pipe, CI, an AI coworker, --json or NO_COLOR),
// and only when the header still fits beside it in width columns.
func rootHeaderArtFor(env loginArtEnv, width int) []string {
	if !env.NewLogo || !env.allowed() {
		return nil
	}
	art := ui.RenderMark()
	if rootHeaderWidthBeside(art) > width {
		return nil
	}
	return art
}

func rootHeaderWidthBeside(art []string) int {
	width := 0
	for _, line := range rootHeaderLines() {
		width = max(width, ansi.StringWidth(line))
	}
	if len(art) == 0 {
		return width
	}
	return ansi.StringWidth(art[0]) + ansi.StringWidth(rootHeaderGap) + width
}

// rootHelpHeader renders the root help header, with art drawn to its left and
// the text level with the middle of the figure when art is given.
func rootHelpHeader(art []string) string {
	text := rootHeaderLines()
	var b strings.Builder
	if len(art) == 0 {
		for _, line := range text {
			b.WriteString(line + "\n")
		}
		return b.String()
	}
	top := max(0, (len(art)-len(text))/2)
	for i, artLine := range art {
		line := artLine
		if j := i - top; j >= 0 && j < len(text) {
			line += rootHeaderGap + text[j]
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}
