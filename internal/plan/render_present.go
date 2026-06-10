package plan

import (
	"regexp"
	"strings"
)

// render_present.go holds the deterministic content-presentation transforms the
// renderer applies to goldmark output: verdict-cell coloring and TL;DR
// extraction. These are pure string transforms over rendered HTML/markdown,
// separated from the template-assembly in render.go for cohesion. (Risk-section
// flagging and per-section badge anchoring live in render.go where they touch the
// section loop directly.)

// verdictClass maps a cell's trimmed text (lowercased) to a color class. Only
// short, unambiguous verdict tokens get colored so a normal data cell is left
// alone.
var verdictClass = map[string]string{
	"yes": "v-good", "✓": "v-good", "✅": "v-good", "pass": "v-good", "done": "v-good", "good": "v-good", "ok": "v-good", "ship": "v-good",
	"no": "v-bad", "✗": "v-bad", "❌": "v-bad", "fail": "v-bad", "blocker": "v-bad", "bad": "v-bad", "🔴": "v-bad",
	"maybe": "v-warn", "partial": "v-warn", "watch": "v-warn", "wip": "v-warn", "later": "v-warn", "⚠": "v-warn", "🟡": "v-warn",
}

// verdictGlyph pairs each verdict class with a shape so meaning isn't color-only.
var verdictGlyph = map[string]string{"v-good": "✓ ", "v-bad": "✗ ", "v-warn": "! "}

var (
	tdCell   = regexp.MustCompile(`(?s)<td>(.*?)</td>`)
	tagStrip = regexp.MustCompile(`<[^>]+>`)
)

// colorVerdictCells adds a color class to table cells whose entire content is a
// verdict token (yes/no/✓/✗/…), so impact and verification matrices read at a
// glance. Cells with any other content are untouched.
func colorVerdictCells(html string) string {
	return tdCell.ReplaceAllStringFunc(html, func(cell string) string {
		inner := tdCell.FindStringSubmatch(cell)[1]
		text := strings.TrimSpace(strings.ToLower(tagStrip.ReplaceAllString(inner, "")))
		if cls, ok := verdictClass[text]; ok {
			// redundant encoding: a leading glyph so the verdict survives
			// grayscale + color-vision deficiency, not color alone.
			return `<td class="` + cls + `">` + verdictGlyph[cls] + inner + `</td>`
		}
		return cell
	})
}

// splitTLDR pulls a LEADING TL;DR block out of the preamble markdown so it can
// be rendered as the decision-first hero callout. Only the first non-empty block
// is eligible — a mid-document "TL;DR" is left in place, so its meaning isn't
// reordered into the hero. Returns ("", md) when the lede isn't a TL;DR.
func splitTLDR(md string) (tldr, rest string) {
	blocks := blockSplit.Split(md, -1)
	// the "lede" is the first non-empty block, skipping a leading H1 title block
	// (the title renders separately, so a TL;DR right after it is still leading).
	lede := -1
	for i, b := range blocks {
		if strings.TrimSpace(b) == "" {
			continue
		}
		if lede == -1 && h1Line.MatchString(b) {
			continue // leading title block — keep looking for the real lede
		}
		lede = i
		break
	}
	if lede < 0 || !tldrLead.MatchString(blocks[lede]) {
		return "", md // the lede isn't a TL;DR — leave a mid-document one in place
	}
	tldr = blocks[lede]
	kept := append(append([]string{}, blocks[:lede]...), blocks[lede+1:]...)
	return tldr, strings.Join(kept, "\n\n")
}

// tldrLabelHTML strips a leading "TL;DR" / "TL;DR:" label from the rendered
// callout body — the callout chrome already announces it — keeping the substance.
var tldrLabelHTML = regexp.MustCompile(`(?is)^(<(?:p|blockquote)[^>]*>\s*)(?:<strong>)?\s*tl;?dr\s*:?\s*(?:</strong>)?\s*`)

func stripLeadingTLDRLabel(html string) string {
	return tldrLabelHTML.ReplaceAllString(strings.TrimSpace(html), "$1")
}
