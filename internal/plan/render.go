package plan

import (
	"bytes"
	"embed"
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
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

//go:embed assets/scaffold.css assets/scaffold.js assets/review.js assets/sw.js assets/plan.html.tmpl assets/wordmark-dark.svg assets/wordmark-light.svg assets/mermaid.min.js
var renderAssets embed.FS

// ReviewServiceWorkerJS returns the embedded service worker the live review
// server exposes at /sw.js — the offline shell that keeps a rendered plan
// readable in the browser after the server exits (review.js registers it).
func ReviewServiceWorkerJS() ([]byte, error) {
	return renderAssets.ReadFile("assets/sw.js")
}

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
	// PriorArtURL resolves a prior-art source (its kind + ref/slug) to a SageOx
	// web URL, opened in a new tab from the enrichment panel. Nil-safe: when nil
	// or when it returns "", the prior-art entry renders as crisp text with no
	// link. This is the seam that keeps internal/plan config-agnostic — the
	// command layer builds the closure from the local project config, and
	// `ox plan enrich --json` (no config) never embeds an environment URL.
	PriorArtURL func(refKind, ref string) string
	// SessionURL is the universal conversation link (/c/<ses_id>) of the
	// recording that produced this plan, rendered as a deterministic footer
	// link so committed plan artifacts point back to their session. Empty
	// (no recording, no start-minted ID, or session attribution disabled)
	// omits the link.
	SessionURL string
	// Artifact renders a strictly self-contained page with NO external resource
	// requests, suitable for publishing as a Claude Code Artifact (served under a
	// strict CSP that blocks all cross-origin script/style/font/img and all
	// fetch/XHR/WebSocket). In this mode the Google-Fonts <link> is dropped (the
	// font stacks fall back to system fonts), the SSE review layer is omitted, and
	// the Mermaid CDN <script> is replaced by the vendored library inlined in
	// place (only when the plan carries a diagram), so diagrams render at full
	// parity with zero network. The SageOx enrichment reference links are
	// preserved — top-level <a href> navigation is not CSP-blocked, so a published
	// artifact stays a hub back into the Ledger. Companion cards and
	// ```html-interactive blocks are omitted/stripped in this mode: an artifact
	// is one self-contained page with no sibling files and no author scripting.
	Artifact bool
	// Companions are rich self-contained HTML artifacts bundled with the plan
	// (see companion.go), linked prominently near the top of the rendered page.
	// The Href is context-resolved by the caller (relative companions/<name>
	// for file renders; the same path against the review server's route).
	Companions []Companion
}

// reviewStateItem is the slim per-item shape injected into the page for the
// review layer to paint inline. state is open|addressed|verified|wontfix.
type reviewStateItem struct {
	Anchor   string `json:"anchor"`
	Section  string `json:"section,omitempty"`
	Label    string `json:"label"`
	Status   string `json:"status"`
	State    string `json:"state"`
	Note     string `json:"note,omitempty"`
	Reviewer string `json:"reviewer,omitempty"`
}

// mermaid fences come out of goldmark as <pre><code class="language-mermaid">…;
// mermaid.run() wants <pre class="mermaid">…. The escaped entities (&gt; etc.)
// decode back to raw text in the DOM via textContent, which is what mermaid
// reads — so no un-escaping is needed here.
var mermaidFence = regexp.MustCompile(`(?s)<pre><code class="language-mermaid">(.*?)</code></pre>`)

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
	URL    string // SageOx web URL for prior-art; empty = render as plain text
}

type reviewSummary struct {
	Open      int
	Addressed int
	Verified  int
	Wontfix   int
	HasReview bool
}

// statChip is one hero stat (value + label), emitted from plan structure so
// the first 30 seconds carry numbers, not prose. Class picks the accent.
type statChip struct {
	Value string
	Label string
	Class string // neutral | copper | amber | red | teal
	Href  string // scroll-jump target ("" = inert)
}

// renderContext is one surfaced context-bundle item shown in the enrichment
// panel's alignment strip. Retrieved context used to reach the page ONLY when
// the prose happened to name it (inline markers); the strip closes that
// plumbing gap — retrieved ADRs/sessions/decisions are visible enrichment
// even when no marker anchors.
type renderContext struct {
	Kind  string
	Title string
}

type renderData struct {
	Title string
	Slug  string
	CSS   template.CSS
	JS    template.JS
	// MermaidJS is the vendored mermaid library, inlined ONLY in artifact mode
	// when the plan actually contains a diagram — so a CSP-locked artifact still
	// renders diagrams with zero network, and a diagram-free artifact pays none of
	// the weight. Empty in the normal render, which loads Mermaid from the CDN.
	MermaidJS      template.JS
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
	Signals        []renderSignal  // unanchored signals (no matching section)
	ContextItems   []renderContext // surfaced context bundle (alignment strip)
	Stats          []statChip      // hero stat chips (structure-derived numbers)
	FooterCredit   bool
	SessionURL     string // /c/ conversation link of the producing recording ("" = omit)
	// Artifact toggles the CSP-safe variant: the template drops the Google-Fonts
	// link, the Mermaid CDN script, and the SSE review layer when set.
	Artifact bool
	// Companions render as a prominent card row under the title (never in
	// artifact mode — a self-contained page must not carry sibling-file links).
	Companions []Companion
	// Tabbed switches the section layout from one long scroll to tabbed views
	// with a sticky top tab bar (the comparison-page register). Enabled when
	// the plan has more than 3 H2 sections; smaller plans keep the single
	// scroll, which reads better at that size.
	Tabbed bool
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
		Title:          Title(in),
		Slug:           opts.Slug,
		CSS:            template.CSS(string(css) + highlightCSS()),
		JS:             template.JS(js),
		ReviewJS:       template.JS(reviewJS),
		ReviewEndpoint: opts.ReviewEndpoint,
		ReviewToken:    opts.ReviewToken,
		FooterCredit:   len(res.Annotations) > 0 || len(res.Context) > 0,
		SessionURL:     opts.SessionURL,
		Artifact:       opts.Artifact,
		WordmarkDark:   template.HTML(wordmarkDark),  //nolint:gosec // first-party embedded asset
		WordmarkLight:  template.HTML(wordmarkLight), //nolint:gosec // first-party embedded asset
	}
	if !opts.Artifact {
		data.Companions = opts.Companions
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
				tldrHTML, err := mdToHTML(md, tldr, opts.Artifact)
				if err != nil {
					return nil, err
				}
				data.TLDR = template.HTML(stripLeadingTLDRLabel(string(tldrHTML)))
			}
			body, err := mdToHTML(md, rest, opts.Artifact)
			if err != nil {
				return nil, err
			}
			pre := stripLeadingH1(string(body))
			pre, markers = injectMarkers(pre, markers)
			data.Preamble = template.HTML(pre)
			continue
		}
		body, err := mdToHTML(md, s.Body, opts.Artifact)
		if err != nil {
			return nil, err
		}
		var secHTML string
		secHTML, markers = injectMarkers(string(body), markers)
		// structure-driven auto-visualization: gated-track tables gain a
		// swimlane, comparison tables become click-to-inspect (autoviz.go).
		secHTML = autoVisualize(secHTML, s.Heading)
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

	// Tabbed layout: with more than 3 H2 sections the single scroll buries the
	// later sections; tabs (sticky top bar, one view at a time) keep every
	// section one click away. Smaller plans keep the scroll — a tab bar with 2
	// entries is chrome without information.
	data.Tabbed = len(data.Sections) > 3

	// TL;DR hero, ALWAYS: a page must open on a plain-language lede, never on
	// status blocks or insider jargon. When the plan carries no explicit TL;DR
	// marker, the first plain paragraph (preamble first, else the first
	// section) is LIFTED into the hero — moved, not copied, so it never reads
	// twice.
	if strings.TrimSpace(string(data.TLDR)) == "" {
		if para, rest, ok := liftLede(string(data.Preamble)); ok {
			data.TLDR = template.HTML("<p>" + para + "</p>") //nolint:gosec // first-party rendered fragment
			data.Preamble = template.HTML(rest)              //nolint:gosec // first-party rendered fragment
		} else if len(data.Sections) > 0 {
			if para, rest, ok := liftLede(string(data.Sections[0].HTML)); ok {
				data.TLDR = template.HTML("<p>" + para + "</p>") //nolint:gosec // first-party rendered fragment
				data.Sections[0].HTML = template.HTML(rest)      //nolint:gosec // first-party rendered fragment
			}
		}
	}

	// Alignment strip: surface the retrieved context bundle (top items by
	// score) so enrichment is visible even when no inline marker anchors and
	// no badge fired — the retrieved ADR/session IS team context on the plan.
	data.ContextItems = topContextItems(res.Context, 5)

	// Hero stat chips: the first-30-seconds numbers (sections, files, team
	// signals, open review items), each only when non-zero. Two chips minimum
	// or none — a single lonely chip is noise, not a dashboard.
	data.Stats = heroStats(res, len(data.Sections), data.Review)

	// Anchor each signal to the section(s) whose files it concerns; signals
	// that match no section stay in the global enrichment panel. This is the
	// data join that replaces a single prose blob with section-anchored
	// markers — the spec's per-section badge rail.
	all := signalsWithFiles(res, opts.PriorArtURL)
	data.HasSignals = len(all) > 0
	data.SignalCount = len(all)
	if data.SignalCount != 1 {
		data.Plural = "s"
	}
	// A signal anchors to the section(s) whose files it concerns. But a signal cited
	// across most of the plan (a broadly-shared file) would stamp the same chip on
	// every section — the repetition curation guards against — so it rolls into the
	// single global coordination panel instead. The cap is PROPORTIONAL, not a fixed
	// count: a file cited in 3 of 12 sections is still section-specific and anchors,
	// while one cited in 3 of 3 globalizes (see tooBroadToAnchor).
	for _, sig := range all {
		var hits []int
		for i := range data.Sections {
			if filesIntersect(sig.files, data.Sections[i].files) {
				hits = append(hits, i)
			}
		}
		if len(hits) == 0 || tooBroadToAnchor(len(hits), len(data.Sections)) {
			data.Signals = append(data.Signals, sig.renderSignal)
			continue
		}
		for _, i := range hits {
			data.Sections[i].Signals = append(data.Sections[i].Signals, sig.renderSignal)
		}
	}

	// Render-time diagram check: the page swallows Mermaid errors, so surface any
	// broken/non-portable diagram to the log trail (the command layer also prints
	// these as hints). Advisory only.
	for _, f := range LintMermaidMarkdown(in.Raw) {
		slog.Warn("plan render: mermaid lint", "rule", f.Rule, "detail", f.Message)
	}

	// Artifact mode is served under a CSP that blocks the Mermaid CDN. When the
	// plan carries a diagram, inline the vendored library so the artifact renders
	// it with zero network — at full parity with the normal render (same dark/
	// light theme vars in scaffold.js). A diagram-free artifact skips the weight.
	if opts.Artifact && renderHasMermaid(data) {
		mermaidJS, err := renderAssets.ReadFile("assets/mermaid.min.js")
		if err != nil {
			return nil, fmt.Errorf("read mermaid.min.js: %w", err)
		}
		data.MermaidJS = template.JS(mermaidJS) //nolint:gosec // first-party vendored asset, SRI-verified against the CDN copy
	}

	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}
	return out.Bytes(), nil
}

// renderHasMermaid reports whether the rendered body contains a Mermaid block
// (mermaidFence rewrites fenced ```mermaid into <pre class="mermaid">), so the
// artifact path only pays the vendored library's weight when a diagram exists.
func renderHasMermaid(data renderData) bool {
	const marker = `class="mermaid"`
	// TLDR is split out of the preamble into its own callout, so a diagram inside
	// a "TL;DR" block lands here, not in Preamble — check it too.
	if strings.Contains(string(data.TLDR), marker) || strings.Contains(string(data.Preamble), marker) {
		return true
	}
	for _, s := range data.Sections {
		if strings.Contains(string(s.HTML), marker) {
			return true
		}
	}
	return false
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

func mdToHTML(md goldmark.Markdown, src string, artifact bool) (template.HTML, error) {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return "", fmt.Errorf("markdown convert: %w", err)
	}
	// Ordering is load-bearing: mermaid fences are rewritten to <pre class="mermaid">
	// FIRST, then ```html-interactive fences pass through raw — both BEFORE
	// highlightFences (which scans for <pre><code class="language-X">) so neither
	// is ever chroma-tokenized. colorVerdictCells touches table cells only, so its
	// position is independent; compareContainers rewrites paragraph markers only.
	rendered := mermaidFence.ReplaceAllString(buf.String(), `<pre class="mermaid">$1</pre>`)
	rendered = interactiveFences(rendered, artifact)
	rendered = highlightFences(rendered)
	rendered = colorVerdictCells(rendered)
	rendered = compareContainers(rendered)
	return template.HTML(rendered), nil //nolint:gosec // first-party plan markdown; see newMarkdown trust note
}

// interactiveFence matches a goldmark-rendered ```html-interactive fenced block.
// The fence is the SANCTIONED channel for plan-authored interactivity (tab
// logic, field inspectors, animated figures): unlike a raw markdown HTML block
// — which goldmark splits at the first blank line for non-script tags — a
// fence's content survives verbatim to the closing fence, so multi-element
// interactive fragments arrive intact.
var interactiveFence = regexp.MustCompile(`(?s)<pre><code class="language-html-interactive">(.*?)</code></pre>`)

// interactiveFences rewrites ```html-interactive fences into raw HTML
// (entities un-escaped back to the authored markup) wrapped in a neutral
// container. TRUST BOUNDARY: same as newMarkdown's WithUnsafe — the plan is
// the developer's own local content rendered locally for that developer, so
// author scripting is a feature, not an injection surface. In artifact mode
// the block is REPLACED by a static placeholder: the artifact CSP posture is
// "no author scripting", and stripping here keeps that contract strict.
func interactiveFences(s string, artifact bool) string {
	return interactiveFence.ReplaceAllStringFunc(s, func(block string) string {
		if artifact {
			return `<div class="interactive-omitted">Interactive block omitted from the self-contained artifact export — open the full render for the live version.</div>`
		}
		m := interactiveFence.FindStringSubmatch(block)
		if m == nil {
			return block
		}
		return `<div class="interactive-block">` + html.UnescapeString(m[1]) + `</div>`
	})
}

// comparePair matches a `:::compare` opener paragraph, its content, and the
// closing `:::` paragraph. Rewritten pairwise; an unbalanced marker is left
// visible in the output (author feedback beats silent structural damage).
var comparePair = regexp.MustCompile(`(?s)<p>:::compare</p>\s*(.*?)\s*<p>:::</p>`)

// compareContainers turns `:::compare … :::` blocks into side-by-side panes —
// the comparison-pane register the hand-built pages use, expressible from
// plain markdown: two tables, or two blocks each under its own H3.
func compareContainers(s string) string {
	return comparePair.ReplaceAllStringFunc(s, func(block string) string {
		m := comparePair.FindStringSubmatch(block)
		if m == nil {
			return block
		}
		var b strings.Builder
		b.WriteString(`<div class="compare">`)
		for _, pane := range splitComparePanes(m[1]) {
			b.WriteString(`<div class="compare-pane">`)
			b.WriteString(pane)
			b.WriteString(`</div>`)
		}
		b.WriteString(`</div>`)
		return b.String()
	})
}

// splitComparePanes divides a compare block's inner HTML into panes: at each
// <h3> when the block carries two or more (heading-titled panes), else at
// each <table> (the bare two-tables case), else the whole block as a single
// pane (degrades to one full-width panel rather than breaking layout).
func splitComparePanes(inner string) []string {
	for _, marker := range []string{"<h3", "<table"} {
		if strings.Count(inner, marker) >= 2 {
			return splitAtMarker(inner, marker)
		}
	}
	return []string{inner}
}

// splitAtMarker splits s at each occurrence of marker; content before the
// first occurrence (a lede paragraph) becomes its own leading pane.
func splitAtMarker(s, marker string) []string {
	var out []string
	rest := s
	for {
		i := strings.Index(rest, marker)
		if i < 0 {
			if trimmed := strings.TrimSpace(rest); trimmed != "" {
				out = append(out, trimmed)
			}
			return out
		}
		if lead := strings.TrimSpace(rest[:i]); lead != "" {
			out = append(out, lead)
		}
		j := strings.Index(rest[i+len(marker):], marker)
		if j < 0 {
			out = append(out, strings.TrimSpace(rest[i:]))
			return out
		}
		out = append(out, strings.TrimSpace(rest[i:i+len(marker)+j]))
		rest = rest[i+len(marker)+j:]
	}
}

// codeFence matches a goldmark-rendered fenced code block carrying a language
// class. Non-greedy body capture mirrors mermaidFence. By the time highlightFences
// runs, mermaid blocks are already <pre class="mermaid">, so they never match
// here. goldmark HTML-escapes code content, so a literal "</code></pre>" inside a
// block becomes "&lt;/code&gt;&lt;/pre&gt;" and cannot prematurely close the match.
var codeFence = regexp.MustCompile(`(?s)<pre><code class="language-([\w+#.-]+)">(.*?)</code></pre>`)

// Code highlighting renders class-based markup once (the markup is style-
// independent), while highlightCSS() emits two theme-scoped stylesheets so the
// colors flip with the page's [data-theme] toggle — no client JS, no CDN. The
// palette mirrors sageox-mono's web highlighter (Shiki github-dark / github-light)
// for cross-surface parity.
var (
	highlightFormatter = chromahtml.New(chromahtml.WithClasses(true), chromahtml.ClassPrefix("ox-hl-"))
	highlightCSSOnce   sync.Once
	highlightCSSCache  string
	chromaBackground   = regexp.MustCompile(`background(-color)?:[^;}]*;?`)
)

// highlightFences colorizes fenced code blocks server-side with chroma. Blocks
// with no language class don't match the regex (left monochrome); blocks with an
// unknown language, the mermaid language, or any chroma error are returned
// verbatim — an unknown lexer must NOT fall back to prose-tokenizing.
func highlightFences(s string) string {
	return codeFence.ReplaceAllStringFunc(s, func(block string) string {
		m := codeFence.FindStringSubmatch(block)
		if m == nil {
			return block
		}
		lang := m[1]
		if lang == "mermaid" {
			return block // belt-and-suspenders; mermaid is handled upstream
		}
		lexer := lexers.Get(lang)
		if lexer == nil {
			return block // unknown language → leave monochrome, never panic
		}
		iterator, err := lexer.Tokenise(nil, html.UnescapeString(m[2])) // chroma re-escapes on output
		if err != nil {
			return block
		}
		var out bytes.Buffer
		if err := highlightFormatter.Format(&out, styles.Get("github-dark"), iterator); err != nil {
			return block
		}
		return out.String()
	})
}

// highlightCSS builds the combined, theme-scoped chroma stylesheet (generated
// once). github-dark applies under html[data-theme="dark"], github (light) under
// html[data-theme="light"], so toggling the theme recolors code with zero JS.
// chroma's baked panel background is stripped so the scaffold's var(--panel)
// shows through.
func highlightCSS() string {
	highlightCSSOnce.Do(func() {
		highlightCSSCache = "\n/* code syntax highlighting (chroma, theme-scoped) */\n" +
			themeScopedChromaCSS(`html[data-theme="dark"]`, "github-dark") +
			themeScopedChromaCSS(`html[data-theme="light"]`, "github")
	})
	return highlightCSSCache
}

// themeScopedChromaCSS emits chroma's class CSS for one style, with every rule
// prefixed by scope (a [data-theme] selector) and all background declarations
// stripped. WriteCSS uses the formatter's class prefix, so classes line up with
// the markup highlightFences produces.
func themeScopedChromaCSS(scope, styleName string) string {
	var raw bytes.Buffer
	if err := highlightFormatter.WriteCSS(&raw, styles.Get(styleName)); err != nil {
		return ""
	}
	css := chromaBackground.ReplaceAllString(raw.String(), "")
	var b strings.Builder
	for _, line := range strings.Split(css, "\n") {
		line = strings.TrimSpace(line)
		// chroma prefixes each rule with a "/* TokenName */" comment; drop it so
		// the scope sits flush against the selector (cleaner, and unambiguous).
		if strings.HasPrefix(line, "/*") {
			if end := strings.Index(line, "*/"); end >= 0 {
				line = strings.TrimSpace(line[end+2:])
			}
		}
		if line == "" || !strings.Contains(line, "{") {
			continue
		}
		b.WriteString(scope)
		b.WriteByte(' ')
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// signalWithFiles couples a projected renderSignal with the annotation's file
// list so RenderHTML can anchor it to a section.
type signalWithFiles struct {
	renderSignal
	files []string
}

// signalsWithFiles projects badges into anchorable signals: the ox-computed
// deterministic ones (collision / prior-art / expert-routing) AND the
// agent-authored, cited judgment ones (aligns / conflicts / expert
// perspective) — a saved plan's merged annotations.json carries both, and
// dropping judgment badges left the alignment layer invisible in real output.
// rigor stays excluded: it is a collaboration meta-signal, not plan content.
// The presence of any signal is what makes the render emit the anchored OX
// marker the lint contract requires.
func signalsWithFiles(res Result, priorArtURL func(refKind, ref string) string) []signalWithFiles {
	labels := map[string]string{
		"collision":          "Collision",
		"prior-art":          "Prior art",
		"expert-routing":     "Expert",
		"aligns":             "Aligns",
		"conflicts":          "Conflicts",
		"expert-perspective": "Expert view",
	}
	var out []signalWithFiles
	for _, a := range res.Annotations {
		if a.Kind != BadgeDeterministic && a.Kind != BadgeJudgment {
			continue
		}
		if a.Type == BadgeRigor {
			continue
		}
		label, ok := labels[string(a.Type)]
		if !ok {
			continue
		}
		// humanize is the DEFAULT human projection — it strips agent-only provenance
		// from any badge's Why, including future ones that never set HumanWhy (the
		// durable floor TestRenderHTML_NoProvenanceLeaks enforces). HumanWhy overrides
		// only for a genuine rewrite, not a strip. The raw Why stays intact on
		// res.Annotations for the --json/agent path.
		why := humanize(a.Why)
		if a.HumanWhy != "" {
			why = a.HumanWhy
		}
		// Suppress a raw "commit:<sha>" source from the human view — it is agent-
		// only provenance. A real PR / web URL is kept. --json keeps SourceURL raw.
		src := a.SourceURL
		if strings.HasPrefix(src, "commit:") {
			src = ""
		}
		sig := renderSignal{
			Type:   string(a.Type),
			Label:  label,
			Why:    why,
			Source: src,
		}
		// Prior-art entries link to the SageOx web view when the command layer
		// supplied a resolver and it can build a URL for this source kind
		// (sessions today; plans/murmurs fall back to plain text).
		if a.Type == BadgePriorArt && priorArtURL != nil {
			sig.URL = priorArtURL(a.RefKind, a.SourceURL)
		}
		out = append(out, signalWithFiles{renderSignal: sig, files: a.Files})
	}
	return out
}

// firstParaRe matches the first <p> block of a rendered fragment; leadingJunk
// guards liftLede against reaching INTO a wrapper (a compare pane, a figure,
// a list) to steal a nested paragraph — lifting is only safe when the
// paragraph is the fragment's actual opening block.
var (
	firstParaRe = regexp.MustCompile(`(?s)<p>(.*?)</p>`)
	// only a fragment that OPENS with a bare paragraph lifts — an opening
	// blockquote (a status banner) or table is not a lede, and lifting the
	// <p> nested inside it would gut its wrapper.
	leadingTags = regexp.MustCompile(`^\s*<p>`)
)

// liftLede lifts the opening paragraph out of a rendered HTML fragment,
// returning (paragraph inner HTML, remaining fragment, ok). ok is false when
// the fragment doesn't OPEN with plain prose (a table, figure, or heading
// first) — synthesizing a hero from mid-document content would misrepresent
// the plan, so the hero is skipped instead.
func liftLede(html string) (string, string, bool) {
	if !leadingTags.MatchString(html) {
		return "", html, false
	}
	m := firstParaRe.FindStringSubmatchIndex(html)
	if m == nil {
		return "", html, false
	}
	para := strings.TrimSpace(html[m[2]:m[3]])
	if para == "" {
		return "", html, false
	}
	rest := strings.TrimSpace(html[:m[0]] + html[m[1]:])
	return para, rest, true
}

// heroStats derives the hero chip row from plan structure + enrichment: each
// chip only when its number is non-zero, and fewer than two chips yields none
// (one lonely number is noise). Signals chip is teal (source-colored), open
// review amber (needs attention), the rest neutral.
func heroStats(res Result, sections int, review reviewSummary) []statChip {
	var out []statChip
	if sections > 0 {
		out = append(out, statChip{Value: fmt.Sprintf("%d", sections), Label: "sections", Class: "neutral"})
	}
	if res.Signals.Files > 0 {
		out = append(out, statChip{Value: fmt.Sprintf("%d", res.Signals.Files), Label: "files touched", Class: "neutral"})
	}
	if n := res.Signals.Collisions + res.Signals.PriorArt + res.Signals.ExpertRoutes; n > 0 {
		out = append(out, statChip{Value: fmt.Sprintf("%d", n), Label: "team signals", Class: "teal"})
	}
	if review.Open > 0 {
		out = append(out, statChip{Value: fmt.Sprintf("%d", review.Open), Label: "open review", Class: "amber"})
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// topContextItems projects the retrieved context bundle into the alignment
// strip: highest-scored first (stable on ties), capped so the strip stays a
// glance, not a dump.
func topContextItems(items []ContextItem, limit int) []renderContext {
	idx := make([]int, len(items))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return items[idx[a]].Score > items[idx[b]].Score })
	var out []renderContext
	for _, i := range idx {
		if len(out) == limit {
			break
		}
		title := strings.TrimSpace(items[i].Title)
		if title == "" {
			title = items[i].Ref
		}
		if title == "" {
			continue
		}
		out = append(out, renderContext{Kind: items[i].Kind, Title: title})
	}
	return out
}

// tooBroadToAnchor reports a signal cited so widely it belongs in the global panel
// rather than repeating its chip per section: it must hit at least 3 sections AND
// more than half of them. The proportional test keeps a file cited in 3 of 12
// sections section-specific (anchors) while globalizing one cited in 3 of 3; the
// floor of 3 means a signal in one or two sections is never treated as repetition.
func tooBroadToAnchor(hits, sections int) bool {
	return hits >= 3 && hits*2 > sections
}

// provenanceShapes are the agent-only provenance fragments a detector Why string
// carries for the --json path but a human chip must never show: a
// "(N commits, last touched …)" clause, a "— touched by N workspaces …" clause,
// and a trailing "<N>h/<N>d ago" recency stamp. humanize strips them at render
// time; the raw Why stays intact on the annotation for the agent path.
var provenanceShapes = []*regexp.Regexp{
	regexp.MustCompile(`\s*\([^)]*\b\d+\s+commits?\b[^)]*\)`),
	regexp.MustCompile(`\s*[—-]\s*touched by \d+ workspaces?[^.;]*`),
	regexp.MustCompile(`\s+\d+[hdwmy] ago\b`),
}

// humanize projects a detector Why into decision-first chip text by stripping the
// known provenance shapes. It is the DEFAULT human projection: every deterministic
// badge is cleaned, including future ones that never set HumanWhy — the durable
// floor a forgotten HumanWhy can't breach. HumanWhy stays an override for a badge
// whose human phrasing is a genuine rewrite (collision coordinate guidance, murmur
// present-tense), not a mechanical strip.
func humanize(why string) string {
	out := why
	for _, re := range provenanceShapes {
		out = re.ReplaceAllString(out, "")
	}
	return strings.TrimRight(strings.TrimSpace(out), " —-,")
}

// filesIntersect reports whether two file-reference lists name a common file. A
// bare basename in one list matches a fuller path in the other at a path boundary
// ("render.go:42" matches "internal/plan/render.go"), but two DIFFERENT files that
// merely share a basename do NOT match ("internal/auth/client.go" vs
// "internal/lfs/client.go") — the basename collision that used to inflate the
// per-section anchor count and wrongly globalize a narrowly-cited signal.
func filesIntersect(a, b []string) bool {
	for _, fa := range a {
		na := normalizeRef(fa)
		for _, fb := range b {
			if refMatch(na, normalizeRef(fb)) {
				return true
			}
		}
	}
	return false
}

// refMatch matches two normalized refs as equal or as a path-boundary suffix of
// one another, so "render.go" matches ".../plan/render.go" but "auth/client.go"
// does not match "lfs/client.go".
func refMatch(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return a == b || strings.HasSuffix(a, "/"+b) || strings.HasSuffix(b, "/"+a)
}

var lineSuffix = regexp.MustCompile(`:\d+$`)

func normalizeRef(s string) string {
	return lineSuffix.ReplaceAllString(strings.TrimSpace(s), "")
}

var leadingH1 = regexp.MustCompile(`(?s)^\s*<h1[^>]*>.*?</h1>`)

func stripLeadingH1(html string) string {
	return strings.TrimSpace(leadingH1.ReplaceAllString(html, ""))
}
