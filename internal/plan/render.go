package plan

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"path"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	ghtml "github.com/yuin/goldmark/renderer/html"
)

// Package render produces a self-contained, agent-agnostic HTML plan directly
// from the binary — NO Claude-only skill required. This is what lets Codex,
// Gemini, Amp, Pi, and any other agent generate a SageOx-enriched HTML plan
// from `ox plan render --open`. The SageOx enrichment (deterministic badges) is
// injected by construction, so the render always satisfies the LintBranding
// attribution contract (footer credit + an anchored OX marker).
//
// Beyond faithful markdown, the renderer does deterministic CONTENT-PRESENTATION
// work — the lever that makes a plan read well now that chrome is free:
//   - per-section badge anchoring: a deterministic signal is shown next to the
//     section whose files it concerns (data join), not as one global prose blob;
//   - a TL;DR hero callout lifted to the top so the decision leads;
//   - risk sections flagged with severity styling;
//   - verdict cells (yes/no/✓/✗) colored so matrices read at a glance.
// All degrade gracefully: a plan with none of these conventions just renders.

//go:embed assets/scaffold.css assets/scaffold.js assets/review.js assets/plan.html.tmpl assets/wordmark-dark.svg assets/wordmark-light.svg
var renderAssets embed.FS

// RenderOptions carries optional render-time context that isn't part of the
// enrichment Result.
type RenderOptions struct {
	Slug string
	// Review is the merged review state (rounds + resolutions) for this plan, so
	// the render can show each item's open/addressed state inline and in a
	// summary. Empty for a plan with no review yet.
	Review []MergedItem
	// ReviewEndpoint + ReviewToken are set ONLY when the page is served by the
	// ephemeral `ox plan review` server: the page POSTs marks to the endpoint
	// with the token. Empty for a static file:// render (clipboard fallback).
	ReviewEndpoint string
	ReviewToken    string
}

// reviewStateItem is the slim per-item shape injected into the page for the
// review layer to paint inline. state is open|addressed|verified|wontfix.
type reviewStateItem struct {
	Anchor  string `json:"anchor"`
	Section string `json:"section,omitempty"`
	Label   string `json:"label"`
	Status  string `json:"status"`
	State   string `json:"state"`
	Note    string `json:"note,omitempty"`
}

// mermaid fences come out of goldmark as <pre><code class="language-mermaid">…;
// mermaid.run() wants <pre class="mermaid">…. The escaped entities (&gt; etc.)
// decode back to raw text in the DOM via textContent, which is what mermaid
// reads — so no un-escaping is needed here.
var mermaidFence = regexp.MustCompile(`(?s)<pre><code class="language-mermaid">(.*?)</code></pre>`)

var h1Line = regexp.MustCompile(`(?m)^#\s+(.+?)\s*$`)

// riskHeading flags a section whose heading is about risks, so it can carry
// severity styling.
var riskHeading = regexp.MustCompile(`(?i)\brisks?\b`)

// blockSplit separates markdown into blank-line-delimited blocks (for TL;DR
// extraction). tldrLead matches a block that opens with a TL;DR marker.
var (
	blockSplit = regexp.MustCompile(`\n\s*\n`)
	tldrLead   = regexp.MustCompile(`(?i)^\s*>?\s*\*{0,2}tl;?dr\b`)
)

type tocEntry struct {
	ID      string
	Num     string
	Heading string
}

type renderSection struct {
	ID      string
	Heading string
	HTML    template.HTML
	IsRisk  bool
	Signals []renderSignal // deterministic signals anchored to this section
	files   []string       // cited files, for anchoring (not rendered)
}

type renderSignal struct {
	Type   string // collision | prior-art | expert-routing
	Label  string
	Why    string
	Source string
}

type reviewSummary struct {
	Open      int
	Addressed int
	Verified  int
	Wontfix   int
	HasReview bool
}

type renderData struct {
	Title          string
	Slug           string
	CSS            template.CSS
	JS             template.JS
	ReviewJS       template.JS
	ReviewJSON     template.JS // JSON island: merged review state for the page
	ReviewEndpoint string      // set only when served by `ox plan review`
	ReviewToken    string
	Review         reviewSummary
	TOC            []tocEntry
	TLDR           template.HTML
	Preamble       template.HTML
	Sections       []renderSection
	HasSignals     bool
	SignalCount    int
	Plural         string
	Signals        []renderSignal // unanchored signals (no matching section)
	FooterCredit   bool
	// WordmarkDark/Light are the inline SageOx wordmark SVGs for the subtle
	// side-nav corner badge; CSS shows the variant matching the active theme.
	WordmarkDark  template.HTML
	WordmarkLight template.HTML
}

// RenderHTML renders a resolved plan + its enrichment Result into a single
// self-contained HTML document. Deterministic and network-free at render time
// (Mermaid loads from CDN only when the page is viewed).
func RenderHTML(in Input, res Result) ([]byte, error) {
	return RenderHTMLOpts(in, res, RenderOptions{})
}

// RenderHTMLOpts is RenderHTML with optional render-time context (e.g. the slug
// for the review layer). RenderHTML delegates here with zero options.
func RenderHTMLOpts(in Input, res Result, opts RenderOptions) ([]byte, error) {
	css, err := renderAssets.ReadFile("assets/scaffold.css")
	if err != nil {
		return nil, fmt.Errorf("read scaffold.css: %w", err)
	}
	js, err := renderAssets.ReadFile("assets/scaffold.js")
	if err != nil {
		return nil, fmt.Errorf("read scaffold.js: %w", err)
	}
	reviewJS, err := renderAssets.ReadFile("assets/review.js")
	if err != nil {
		return nil, fmt.Errorf("read review.js: %w", err)
	}
	tmplBytes, err := renderAssets.ReadFile("assets/plan.html.tmpl")
	if err != nil {
		return nil, fmt.Errorf("read plan.html.tmpl: %w", err)
	}
	wordmarkDark, err := renderAssets.ReadFile("assets/wordmark-dark.svg")
	if err != nil {
		return nil, fmt.Errorf("read wordmark-dark.svg: %w", err)
	}
	wordmarkLight, err := renderAssets.ReadFile("assets/wordmark-light.svg")
	if err != nil {
		return nil, fmt.Errorf("read wordmark-light.svg: %w", err)
	}
	tmpl, err := template.New("plan").Parse(string(tmplBytes))
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}

	md := newMarkdown()

	data := renderData{
		Title:          planTitle(in),
		Slug:           opts.Slug,
		CSS:            template.CSS(css),
		JS:             template.JS(js),
		ReviewJS:       template.JS(reviewJS),
		ReviewEndpoint: opts.ReviewEndpoint,
		ReviewToken:    opts.ReviewToken,
		FooterCredit:   len(res.Annotations) > 0 || len(res.Context) > 0,
		WordmarkDark:   template.HTML(wordmarkDark),  //nolint:gosec // first-party embedded asset
		WordmarkLight:  template.HTML(wordmarkLight), //nolint:gosec // first-party embedded asset
	}
	data.ReviewJSON, data.Review = buildReviewState(opts.Review)

	// Inline reference markers ox can stand behind: where a section's prose names
	// a reference ox surfaced team context for (an ADR), wrap the first mention
	// with a neutral OX marker + the surfaced snippet as a tooltip. This marks
	// "SageOx has context on this," NOT a verdict — aligns/conflicts stay the
	// agent's judgment to assert.
	markers := contextMarkers(res.Context)

	num := 0
	for _, s := range in.Sections {
		if strings.TrimSpace(s.Heading) == "" {
			// preamble: content before the first H2. Strip a leading H1 (the
			// template renders the title) and lift any TL;DR into its own callout.
			tldr, rest := splitTLDR(s.Body)
			if strings.TrimSpace(tldr) != "" {
				tldrHTML, err := mdToHTML(md, tldr)
				if err != nil {
					return nil, err
				}
				data.TLDR = template.HTML(stripLeadingTLDRLabel(string(tldrHTML)))
			}
			body, err := mdToHTML(md, rest)
			if err != nil {
				return nil, err
			}
			pre := stripLeadingH1(string(body))
			pre, markers = injectMarkers(pre, markers)
			data.Preamble = template.HTML(pre)
			continue
		}
		body, err := mdToHTML(md, s.Body)
		if err != nil {
			return nil, err
		}
		var secHTML string
		secHTML, markers = injectMarkers(string(body), markers)
		num++
		id := fmt.Sprintf("sec-%d", num)
		data.TOC = append(data.TOC, tocEntry{ID: id, Num: fmt.Sprintf("%02d", num), Heading: s.Heading})
		data.Sections = append(data.Sections, renderSection{
			ID:      id,
			Heading: s.Heading,
			HTML:    template.HTML(secHTML),
			IsRisk:  riskHeading.MatchString(s.Heading),
			files:   s.Files,
		})
	}

	// Anchor each deterministic signal to the section(s) whose files it concerns;
	// signals that match no section stay in the global enrichment panel. This is
	// the data join that replaces a single prose blob with section-anchored
	// markers — the spec's per-section badge rail.
	all := deterministicSignalsWithFiles(res)
	data.HasSignals = len(all) > 0
	data.SignalCount = len(all)
	if data.SignalCount != 1 {
		data.Plural = "s"
	}
	for _, sig := range all {
		matched := false
		for i := range data.Sections {
			if filesIntersect(sig.files, data.Sections[i].files) {
				data.Sections[i].Signals = append(data.Sections[i].Signals, sig.renderSignal)
				matched = true
			}
		}
		if !matched {
			data.Signals = append(data.Signals, sig.renderSignal)
		}
	}

	// Render-time diagram check: the page swallows Mermaid errors, so surface any
	// broken/non-portable diagram to the log trail (the command layer also prints
	// these as hints). Advisory only.
	for _, f := range LintMermaidMarkdown(in.Raw) {
		slog.Warn("plan render: mermaid lint", "rule", f.Rule, "detail", f.Message)
	}

	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}
	return out.Bytes(), nil
}

func newMarkdown() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(extension.GFM), // tables, strikethrough, autolinks, task lists
		goldmark.WithRendererOptions(
			// Pass through inline HTML so plan-authored device mockups, <details>, and
			// the parameterized viz fragments render. TRUST BOUNDARY: the plan markdown
			// is authored by the developer's own agent and rendered locally for that
			// same developer (the review server binds 127.0.0.1, token-gated) — not a
			// third-party-content surface. Mermaid is additionally pinned to
			// securityLevel:'antiscript' (assets/scaffold.js), which strips <script>
			// from diagram labels. If plan markdown ever ingests untrusted third-party
			// content, add an HTML sanitizer (e.g. bluemonday) here.
			ghtml.WithUnsafe(),
		),
	)
}

func mdToHTML(md goldmark.Markdown, src string) (template.HTML, error) {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return "", fmt.Errorf("markdown convert: %w", err)
	}
	html := mermaidFence.ReplaceAllString(buf.String(), `<pre class="mermaid">$1</pre>`)
	html = colorVerdictCells(html)
	return template.HTML(html), nil
}

// signalWithFiles couples a projected renderSignal with the annotation's file
// list so RenderHTML can anchor it to a section.
type signalWithFiles struct {
	renderSignal
	files []string
}

// deterministicSignalsWithFiles projects the ox-computed badges (collision /
// prior-art / expert-routing) into anchorable signals. Judgment badges are
// agent-authored and not surfaced here. The presence of any signal is what makes
// the render emit the anchored OX marker the lint contract requires.
func deterministicSignalsWithFiles(res Result) []signalWithFiles {
	labels := map[string]string{
		"collision":      "Collision",
		"prior-art":      "Prior art",
		"expert-routing": "Expert",
	}
	var out []signalWithFiles
	for _, a := range res.Annotations {
		if string(a.Kind) != "deterministic" {
			continue
		}
		label, ok := labels[string(a.Type)]
		if !ok {
			continue
		}
		out = append(out, signalWithFiles{
			renderSignal: renderSignal{
				Type:   string(a.Type),
				Label:  label,
				Why:    a.Why,
				Source: a.SourceURL,
			},
			files: a.Files,
		})
	}
	return out
}

// filesIntersect reports whether two file-reference lists name a common file,
// comparing both the full ref and the basename (and ignoring a :line suffix), so
// `internal/plan/render.go` matches `render.go:42`.
func filesIntersect(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	set := make(map[string]struct{}, len(a)*2)
	for _, f := range a {
		n := normalizeRef(f)
		set[n] = struct{}{}
		set[path.Base(n)] = struct{}{}
	}
	for _, f := range b {
		n := normalizeRef(f)
		if _, ok := set[n]; ok {
			return true
		}
		if _, ok := set[path.Base(n)]; ok {
			return true
		}
	}
	return false
}

var lineSuffix = regexp.MustCompile(`:\d+$`)

func normalizeRef(s string) string {
	return lineSuffix.ReplaceAllString(strings.TrimSpace(s), "")
}

func planTitle(in Input) string {
	if m := h1Line.FindStringSubmatch(in.Raw); m != nil {
		return strings.TrimSpace(m[1])
	}
	for _, s := range in.Sections {
		if strings.TrimSpace(s.Heading) != "" {
			return s.Heading
		}
	}
	return "Implementation Plan"
}

var leadingH1 = regexp.MustCompile(`(?s)^\s*<h1[^>]*>.*?</h1>`)

func stripLeadingH1(html string) string {
	return strings.TrimSpace(leadingH1.ReplaceAllString(html, ""))
}
