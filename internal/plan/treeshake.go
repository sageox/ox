package plan

import (
	"regexp"
	"strings"
)

// treeshake.go — post-render CSS tree-shake. The scaffold ships styling for
// every primitive the renderer CAN emit (swimlanes, risk matrices, partition
// maps, six chart forms, device mockups, …); any given page uses a handful.
// Shipping the rest is non-data-ink (Tufte), so after the template executes we
// drop the style rules of primitive families whose markup is absent from the
// page.
//
// Safety model — opt-in families, drop only when certain:
//   - Only selectors OWNED by a registered family are ever candidates. A line
//     with any unowned selector (base typography, the review layer, body.rev-on
//     interaction rules, @media/@keyframes prelude) is always kept, so classes
//     that JS adds at runtime (rev-*, active, has-tabbar on a tabbed page) are
//     untouched by construction.
//   - Family presence is judged on class="…" tokens in the page markup, never
//     on prose (a plan that merely SAYS "swimlane" doesn't retain the CSS).
//   - The scaffold CSS is one-rule-per-line; shaking is line-scoped. A line
//     mixing present and absent families is kept (an inert rule is cheaper
//     than a wrong drop).

// cssFamily maps one primitive family: which selectors it owns (selRe over a
// single selector string) and which markup class tokens prove it is used.
// Markers ending in "-" are prefix matches (hstat- matches hstat-neutral).
type cssFamily struct {
	name    string
	selRe   *regexp.Regexp
	markers []string
}

var cssFamilies = []cssFamily{
	{"swim", regexp.MustCompile(`\.swim\b|\.auto-swim\b|\.track-swim\b|\.sw-key\b`), []string{"swim"}},
	{"spark", regexp.MustCompile(`\.spark\b|\.multiples\b`), []string{"spark", "multiples"}},
	{"ba", regexp.MustCompile(`\.ba\b`), []string{"ba", "ba-col"}},
	{"heat", regexp.MustCompile(`\.heat\b`), []string{"heat"}},
	{"device", regexp.MustCompile(`\.device\b|\.device-`), []string{"device"}},
	{"barc", regexp.MustCompile(`\.barc\b|\.bar-row\b`), []string{"barc", "bar-row"}},
	{"stat", regexp.MustCompile(`\.statrow\b|\.stat\b`), []string{"stat", "statrow"}},
	{"ftree", regexp.MustCompile(`\.ftree\b`), []string{"ftree"}},
	{"riskm", regexp.MustCompile(`\.riskm\b|\.riskm-fig\b`), []string{"riskm"}},
	{"flagm", regexp.MustCompile(`\.flagm\b`), []string{"flagm"}},
	{"partition", regexp.MustCompile(`\.pm-|\.pbar\b|\.pbar-|\.pseg\b|\.pseg-|\.pmapv\b|\.pmapv-`), []string{"pbar", "pmapv"}},
	{"charts", regexp.MustCompile(`\.donut\b|\.donut-|\.radar\b|\.radar-|\.quad\b|\.quad-|\.tmap\b|\.tmap-|\.sankey\b|\.sankey-|\.chord\b|\.chord-|\.linec\b|\.linec-|\.vsw\b`), []string{"donut", "radar", "quad", "tmap", "sankey", "chord", "linec"}},
	{"compare", regexp.MustCompile(`\.compare\b|\.compare-pane\b`), []string{"compare"}},
	{"inspect", regexp.MustCompile(`\.inspect\b|\.inspect-|\.lit\b`), []string{"inspect", "inspect-dock"}},
	{"interactive", regexp.MustCompile(`\.interactive-block\b|\.interactive-omitted\b`), []string{"interactive-block", "interactive-omitted"}},
	{"companions", regexp.MustCompile(`\.companion\b|\.companion-|\.companions\b`), []string{"companions"}},
	{"tldr", regexp.MustCompile(`\.tldr\b|\.tldr-`), []string{"tldr"}},
	{"hero", regexp.MustCompile(`\.hero-stats\b|\.hstat\b|\.hstat-`), []string{"hero-stats", "hstat"}},
	{"enrich", regexp.MustCompile(`\.ox-enrich\b|\.ox-ctx\b|\.ox-ctx-`), []string{"ox-enrich", "ox-ctx"}},
	{"chip", regexp.MustCompile(`\.ox-chip\b|\.ox-rail\b|\.ox-sig-|\.bead-map\b`), []string{"ox-chip", "ox-rail", "ox-sig-"}},
	{"annot", regexp.MustCompile(`\.ox-annot\b|\.ox-annot-`), []string{"ox-annot"}},
	{"mermaid", regexp.MustCompile(`\.mermaid\b`), []string{"mermaid"}},
	{"verdict", regexp.MustCompile(`\.v-good\b|\.v-warn\b|\.v-bad\b`), []string{"v-good", "v-warn", "v-bad"}},
	{"jumpbar", regexp.MustCompile(`\.tabbar\b|\.jump-tabs\b|\.jump-bar\b|\.jump-meta\b|\.kbd-map\b|\.legend\b|\.lg\b|\.lg-dot\b|\.has-tabbar\b`), []string{"tabbar"}},
	{"risksec", regexp.MustCompile(`\.risk\b`), []string{"risk"}},
	{"chroma", regexp.MustCompile(`\.ox-hl-`), []string{"ox-hl-"}},
}

var (
	styleBlockRe = regexp.MustCompile(`(?s)<style>(.*?)</style>`)
	classAttrRe  = regexp.MustCompile(`class="([^"]*)"`)
)

// shakeUnusedCSS drops unused primitive-family rules from the page's first
// <style> block, judged against the class tokens the rest of the page uses.
func shakeUnusedCSS(page []byte) []byte {
	loc := styleBlockRe.FindSubmatchIndex(page)
	if loc == nil {
		return page
	}
	markup := string(page[:loc[2]]) + string(page[loc[3]:])
	tokens := classTokenSet(markup)

	present := map[string]bool{}
	for _, f := range cssFamilies {
		present[f.name] = familyPresent(f, tokens)
	}

	css := string(page[loc[2]:loc[3]])
	var out strings.Builder
	out.Grow(len(css))
	for i, line := range strings.Split(css, "\n") {
		if i > 0 {
			out.WriteByte('\n')
		}
		if keepCSSLine(line, present) {
			out.WriteString(line)
		}
	}
	var b []byte
	b = append(b, page[:loc[2]]...)
	b = append(b, out.String()...)
	b = append(b, page[loc[3]:]...)
	return b
}

// classTokenSet collects every class token used in class="…" attributes.
func classTokenSet(markup string) map[string]bool {
	set := map[string]bool{}
	for _, m := range classAttrRe.FindAllStringSubmatch(markup, -1) {
		for _, tok := range strings.Fields(m[1]) {
			set[tok] = true
		}
	}
	return set
}

func familyPresent(f cssFamily, tokens map[string]bool) bool {
	for _, m := range f.markers {
		if strings.HasSuffix(m, "-") {
			for tok := range tokens {
				if strings.HasPrefix(tok, m) {
					return true
				}
			}
			continue
		}
		if tokens[m] {
			return true
		}
	}
	return false
}

// keepCSSLine decides one minified CSS line: dropped only when EVERY selector
// on the line is owned by a family and every owning family is absent.
func keepCSSLine(line string, present map[string]bool) bool {
	sels := selectorsOf(line)
	if len(sels) == 0 {
		return true // comment / blank / brace-only line
	}
	anyOwned := false
	for _, sel := range sels {
		owned := false
		for _, f := range cssFamilies {
			if f.selRe.MatchString(sel) {
				owned = true
				if present[f.name] {
					return true // a present family uses this line
				}
			}
		}
		if !owned {
			return true // base rule (typography, review layer, @media prelude)
		}
		anyOwned = true
	}
	return !anyOwned
}

// selectorsOf extracts the comma-separated selectors of every rule on one
// minified line ("selA,selB{…}selC{…}" → [selA selB selC]). At-rule preludes
// (@media(…), @keyframes name) come back as selectors too; they contain no
// family class so they always read as unowned → kept.
func selectorsOf(line string) []string {
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '{':
			if depth == 0 {
				for _, s := range strings.Split(line[start:i], ",") {
					if s = strings.TrimSpace(s); s != "" {
						out = append(out, s)
					}
				}
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
			start = i + 1
		}
	}
	return out
}
