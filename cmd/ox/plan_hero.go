package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sageox/ox/internal/plan"
	"github.com/sageox/ox/internal/planhero"
)

// plan_hero.go generates hero.svg — a designed SVG poster of a saved plan,
// rendered from its structured data (internal/planhero; never a browser
// screenshot, so ox carries no headless-browser/Chromium dependency) — and is
// the PRIMARY producer for plan gallery thumbnails. Wired into
// savePlanArtifacts (plan.go), between plan.Save and the save's commit, so
// the existing `git add --sparse <dir>` there sweeps hero.svg into the same
// commit as plan.html with no separate commit path.

// writePlanHero renders and writes hero.svg into a just-saved plan dir, from
// data already resolved by the caller (in/res/meta) plus the plan's current
// lifecycle status (plan.CurrentStatus, which reads the events.jsonl this
// follows plan.Save writing). Every field of the poster degrades
// independently (see planhero.RenderPosterSVG) — this function's only error
// paths are template/write failures, which the caller (savePlanArtifacts)
// treats as best-effort and logs rather than fails the save on.
func writePlanHero(dir string, in plan.Input, res plan.Result, meta plan.Meta) error {
	author := ""
	if len(meta.Authors) > 0 {
		author = meta.Authors[0]
	}
	date := ""
	if !meta.CreatedAt.IsZero() {
		date = meta.CreatedAt.Format("2006-01-02")
	}

	svg, err := planhero.RenderPosterSVG(planhero.PosterInput{
		Title:  plan.PlanTopic(in),
		Status: string(plan.CurrentStatus(dir)),
		TLDR:   planHeroTLDR(in.Raw),

		Sections: planHeroSectionCount(in),
		// Diagrams counts ```mermaid fences in the plan's markdown source —
		// deliberately NOT a re-parse of rendered HTML (see the injection
		// site's comment in savePlanArtifacts): in.Raw is already in hand,
		// and every authored/rendered mermaid diagram originates as this
		// exact fence (render.go's mermaidFence rewrites it downstream).
		Diagrams:   strings.Count(in.Raw, "```mermaid"),
		Collisions: res.Signals.Collisions,

		Author: author,
		Date:   date,
	})
	if err != nil {
		return fmt.Errorf("render hero poster: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hero.svg"), svg, 0o644); err != nil {
		return fmt.Errorf("write hero.svg: %w", err)
	}
	return nil
}

// planHeroSectionCount counts the plan's H2 sections — mirrors exactly how
// internal/plan/render.go builds data.Sections (a Section with an empty
// Heading is the preamble, not a section, and isn't counted there either).
func planHeroSectionCount(in plan.Input) int {
	n := 0
	for _, s := range in.Sections {
		if strings.TrimSpace(s.Heading) != "" {
			n++
		}
	}
	return n
}

var (
	// planHeroTLDRMarker matches an explicit "TL;DR" lede marker opening a
	// markdown block. Conceptually mirrors internal/plan/render_present.go's
	// tldrLead, but reimplemented rather than reused: that regex is matched
	// against goldmark-RENDERED HTML, one layer downstream of where this
	// function runs (directly on markdown, before any HTML exists).
	planHeroTLDRMarker = regexp.MustCompile(`(?i)^\s*>?\s*\*{0,2}tl;?dr\b\**:?\s*`)
	planHeroBlockSplit = regexp.MustCompile(`\n\s*\n`)
	planHeroMDLink     = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	planHeroWhitespace = regexp.MustCompile(`\s+`)
	planHeroMDEmphasis = strings.NewReplacer("**", "", "__", "", "`", "")
)

// planHeroTLDR extracts a short, plain-text lede for the hero poster from the
// plan's raw markdown: an explicit TL;DR marker block when the plan opens
// with one, else the first prose paragraph. Returns "" when neither exists
// (the document opens with a heading-only preamble, or its lede is a table/
// list/blockquote rather than prose) — the poster then drops its TL;DR block
// entirely (planhero.RenderPosterSVG) rather than showing something
// misleading.
func planHeroTLDR(raw string) string {
	blocks := planHeroBlockSplit.Split(strings.TrimSpace(raw), -1)
	for _, b := range blocks {
		b = strings.TrimSpace(b)
		if b == "" || strings.HasPrefix(b, "#") {
			continue // blank or a heading block — keep looking for the lede
		}
		if loc := planHeroTLDRMarker.FindStringIndex(b); loc != nil {
			return planHeroPlainText(b[loc[1]:])
		}
		// List markers require a following space or tab; testing the bare
		// character would misread bold-opening prose ("**Important:** …") as a
		// list and drop the lede.
		if strings.HasPrefix(b, "|") || strings.HasPrefix(b, ">") ||
			strings.HasPrefix(b, "- ") || strings.HasPrefix(b, "* ") ||
			strings.HasPrefix(b, "-\t") || strings.HasPrefix(b, "*\t") {
			return "" // a table/blockquote/list opener isn't a prose lede
		}
		return planHeroPlainText(b)
	}
	return ""
}

// planHeroPlainText strips markdown link/emphasis/code syntax and collapses
// whitespace — the poster renders plain SVG text, not HTML, so no markdown
// formatting should survive into it.
func planHeroPlainText(s string) string {
	s = planHeroMDLink.ReplaceAllString(s, "$1")
	s = planHeroMDEmphasis.Replace(s)
	return strings.TrimSpace(planHeroWhitespace.ReplaceAllString(s, " "))
}
