package plan

import (
	"bytes"
	"embed"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// inject.go is the HTML-primary path: a developer hand-authors a rich,
// self-contained HTML plan (tabs, inspectors, animations — whatever the
// author's own agent built) and ox injects its enrichment chrome INTO that
// page rather than wrapping it. RenderHTML/RenderHTMLOpts stay the ox-owned
// generator for plans authored as markdown; InjectChrome is the seam for a
// plan authored directly as HTML, where ox contributes only the SageOx
// enrichment overlay, the footer credit, and the review loop — never the
// author's markup, layout, or scripting.

//go:embed assets/chrome.css assets/chrome.js
var chromeAssets embed.FS

// ChromeMarkerStart / ChromeMarkerEnd delimit the injected ox chrome bundle
// inside an authored HTML plan. Everything between them is ox-owned and
// replaced wholesale on re-injection; the author's markup is never touched.
const (
	ChromeMarkerStart = "<!-- ox-chrome:start -->"
	ChromeMarkerEnd   = "<!-- ox-chrome:end -->"
)

// ChromeData is everything the injected bundle needs, pre-projected so the
// JS stays dumb.
type ChromeData struct {
	Slug    string
	Signals []ChromeSignal  // enrichment chips (deterministic + judgment)
	Context []ChromeContext // surfaced context items (alignment strip)
	// ReviewJSON is the merged review-state JSON array ("[]" when none) — the
	// same shape render.go's buildReviewState produces, so review.js's
	// #ox-review-state island reads identically on an authored page.
	ReviewJSON     string
	ReviewEndpoint string // live review server base URL ("" = static)
	ReviewToken    string
	SessionURL     string // /c/ conversation link ("" = omit)
	FooterCredit   bool   // true when any enrichment exists (earned credit)
}

// ChromeSignal is one enrichment chip: a deterministic badge (collision /
// prior-art / expert-routing) or a judgment badge (aligns / conflicts /
// expert-perspective) the client agent authored from the context bundle.
type ChromeSignal struct {
	Type    string `json:"type"` // collision | prior-art | expert-routing | aligns | conflicts | expert-perspective
	Label   string `json:"label"`
	Why     string `json:"why"`
	URL     string `json:"url,omitempty"`
	Section string `json:"section,omitempty"`
}

// ChromeContext is one surfaced context item shown in the "Context surfaced"
// strip — a slimmed-down ContextItem (no Score/Snippet/Author/When; those are
// ranking/reasoning inputs for the agent, not something a human reader needs).
type ChromeContext struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Ref   string `json:"ref,omitempty"`
}

// chromePayload is the wire shape of the #ox-chrome-data island — the JSON
// snake_case keys are the chrome.js contract, independent of ChromeData's Go
// field names.
type chromePayload struct {
	Slug         string          `json:"slug"`
	Signals      []ChromeSignal  `json:"signals"`
	Context      []ChromeContext `json:"context"`
	Endpoint     string          `json:"endpoint"`
	Token        string          `json:"token"`
	SessionURL   string          `json:"session_url"`
	FooterCredit bool            `json:"footer_credit"`
}

// chromeSignalLabels maps a badge type to its chip label. Only types present
// here are ever surfaced as a ChromeSignal — this is what excludes BadgeRigor
// (a collaboration-rigor stance the panel never shows) without a separate
// type check.
var chromeSignalLabels = map[BadgeType]string{
	BadgeCollision:   "Collision",
	BadgePriorArt:    "Prior art",
	BadgeExpertRoute: "Expert",
	BadgeAligns:      "Aligns",
	BadgeConflicts:   "Conflicts",
	BadgeExpertPersp: "Expert view",
}

// chromeContextCap bounds the "Context surfaced" strip to the highest-scoring
// items — the alignment strip is a glance-able list, not the full bundle the
// agent reasoned over.
const chromeContextCap = 5

// BuildChromeData projects an enrichment Result into ChromeData. Deterministic
// badge Whys go through humanize() (same provenance-strip floor as the
// generated render — no SHAs / "12h ago" / workspace counts may leak);
// HumanWhy overrides when set; a "commit:<sha>" SourceURL is suppressed;
// prior-art URLs resolve through opts.PriorArtURL when non-nil. Judgment
// badges (aligns/conflicts/expert-perspective) ARE included here — they're
// agent-authored, cited claims the human should see. rigor is excluded.
// Context items are capped at 5 by Score descending (stable tie-break by
// document order).
func BuildChromeData(res Result, opts RenderOptions) ChromeData {
	reviewJSON, _ := buildReviewState(opts.Review)
	return ChromeData{
		Slug:           opts.Slug,
		Signals:        buildChromeSignals(res, opts.PriorArtURL),
		Context:        buildChromeContext(res.Context),
		ReviewJSON:     string(reviewJSON),
		ReviewEndpoint: opts.ReviewEndpoint,
		ReviewToken:    opts.ReviewToken,
		SessionURL:     opts.SessionURL,
		// Mirrors RenderHTMLOpts's FooterCredit: earned only when ox actually
		// found something to say, not on a greenfield plan with zero signals.
		FooterCredit: len(res.Annotations) > 0 || len(res.Context) > 0,
	}
}

// buildChromeSignals projects every non-rigor annotation into a ChromeSignal.
// Deterministic badges get the humanize()/HumanWhy/commit-URL treatment
// render.go's deterministicSignalsWithFiles applies; judgment badges keep
// their raw SourceURL as the citation link (they carry no agent-only
// provenance to strip).
func buildChromeSignals(res Result, priorArtURL func(refKind, ref string) string) []ChromeSignal {
	var out []ChromeSignal
	for _, a := range res.Annotations {
		label, ok := chromeSignalLabels[a.Type]
		if !ok {
			continue // rigor, or a future badge type the chrome doesn't know yet
		}
		why := humanize(a.Why)
		if a.HumanWhy != "" {
			why = a.HumanWhy
		}
		sig := ChromeSignal{
			Type:    string(a.Type),
			Label:   label,
			Why:     why,
			Section: a.Section,
		}
		switch {
		case a.Type == BadgePriorArt && priorArtURL != nil:
			sig.URL = priorArtURL(a.RefKind, a.SourceURL)
		case a.Kind == BadgeDeterministic && strings.HasPrefix(a.SourceURL, "commit:"):
			// agent-only provenance (a raw commit SHA) — never surface to a human.
		default:
			sig.URL = a.SourceURL
		}
		out = append(out, sig)
	}
	return out
}

// buildChromeContext slims + caps the context bundle for the alignment strip:
// the top chromeContextCap items by Score descending, ties broken by document
// order (sort.SliceStable over an index slice preserves it).
func buildChromeContext(items []ContextItem) []ChromeContext {
	if len(items) == 0 {
		return nil
	}
	order := make([]int, len(items))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		return items[order[i]].Score > items[order[j]].Score
	})
	n := len(order)
	if n > chromeContextCap {
		n = chromeContextCap
	}
	out := make([]ChromeContext, 0, n)
	for _, i := range order[:n] {
		it := items[i]
		out = append(out, ChromeContext{Kind: it.Kind, Title: it.Title, Ref: it.Ref})
	}
	return out
}

// chromeMarkerRegion matches a complete ox-chrome bundle, including one
// leading and one trailing newline if present, so repeated injection never
// accumulates blank lines where the previous bundle was removed.
var chromeMarkerRegion = regexp.MustCompile(`(?s)\n?` + regexp.QuoteMeta(ChromeMarkerStart) + `.*?` + regexp.QuoteMeta(ChromeMarkerEnd) + `\n?`)

// bodyCloseTag finds </body> case-insensitively — an authored page is not
// guaranteed to use lowercase HTML.
var bodyCloseTag = regexp.MustCompile(`(?i)</body>`)

// InjectChrome appends the ox chrome bundle to an authored HTML page,
// immediately before the last </body> (case-insensitive); a page with no
// </body> gets it appended at the end. NON-DESTRUCTIVE: the author's bytes
// are byte-for-byte preserved outside the marker region. IDEMPOTENT: any
// existing marker region is removed first, so re-injection never doubles.
func InjectChrome(authored []byte, data ChromeData) []byte {
	stripped := chromeMarkerRegion.ReplaceAll(authored, nil)
	bundle := buildChromeBundle(data)

	loc := bodyCloseTag.FindAllIndex(stripped, -1)
	insertAt := len(stripped)
	if len(loc) > 0 {
		insertAt = loc[len(loc)-1][0]
	}

	out := make([]byte, 0, len(stripped))
	out = append(out, stripped[:insertAt]...)
	out = append(out, bundle...)
	out = append(out, stripped[insertAt:]...)
	return out
}

// buildChromeBundle assembles the marker-delimited island+style+script bundle
// in the fixed order the API contract specifies: review-state island,
// chrome-data island, style, boot script, review script. Boot-before-review is
// load-bearing (chrome.js primes document.body's dataset before review.js's
// IIFE reads it); the rest is order-independent.
func buildChromeBundle(data ChromeData) []byte {
	reviewJSON := data.ReviewJSON
	if strings.TrimSpace(reviewJSON) == "" {
		reviewJSON = "[]"
	}
	payload := chromePayload{
		Slug:         data.Slug,
		Signals:      data.Signals,
		Context:      data.Context,
		Endpoint:     data.ReviewEndpoint,
		Token:        data.ReviewToken,
		SessionURL:   data.SessionURL,
		FooterCredit: data.FooterCredit,
	}
	if payload.Signals == nil {
		payload.Signals = []ChromeSignal{}
	}
	if payload.Context == nil {
		payload.Context = []ChromeContext{}
	}
	chromeJSON, err := json.Marshal(payload)
	if err != nil {
		// payload is plain strings/bools/slices of plain structs; Marshal only
		// fails on unsupported types (chan/func) or cyclic maps, neither of which
		// this shape contains — unreachable in practice, but fail closed rather
		// than panic on an untrusted-shaped Result feeding BuildChromeData.
		chromeJSON = []byte(`{}`)
	}

	var b bytes.Buffer
	b.WriteString("\n")
	b.WriteString(ChromeMarkerStart)
	b.WriteString("\n")

	b.WriteString(`<script type="application/json" id="ox-review-state">`)
	b.WriteString(escapeScriptIsland(reviewJSON))
	b.WriteString("</script>\n")

	b.WriteString(`<script type="application/json" id="ox-chrome-data">`)
	b.WriteString(escapeScriptIsland(string(chromeJSON)))
	b.WriteString("</script>\n")

	b.WriteString(`<style id="ox-chrome-style">`)
	b.Write(mustChromeAsset("assets/chrome.css"))
	b.WriteString("</style>\n")

	b.WriteString(`<script id="ox-chrome-boot">`)
	b.Write(mustChromeAsset("assets/chrome.js"))
	b.WriteString("</script>\n")

	b.WriteString(`<script id="ox-chrome-review">`)
	b.Write(mustReviewScript())
	b.WriteString("</script>\n")

	b.WriteString(ChromeMarkerEnd)
	b.WriteString("\n")
	return b.Bytes()
}

// jsonLessThanEscape is the six-character JSON unicode-escape sequence for
// "<" (backslash, u, 0, 0, 3, c) — written as a rune slice so gofmt/vet never
// see (and no future edit can accidentally collapse) a literal backslash-u
// escape sitting in Go source. encoding/json performs this exact substitution
// by default (HTMLEscape); escapeScriptIsland applies it to content that
// didn't go through json.Marshal (the caller-supplied ReviewJSON string).
var jsonLessThanEscape = string([]rune{'\\', 'u', '0', '0', '3', 'c'})

// escapeScriptIsland keeps JSON content safe to embed inside a <script>
// island by replacing "<" with jsonLessThanEscape. This is distinct from
// HTML-entity escaping ("&lt;"): script-tag content is a raw text element
// (entities are never decoded inside it), so "&lt;" would corrupt the JSON,
// while the unicode escape round-trips through JSON.parse back to the
// original "<" and makes a "</script" sequence unrecognizable to the HTML
// parser — the payload can never terminate the island early.
func escapeScriptIsland(jsonStr string) string {
	return strings.ReplaceAll(jsonStr, "<", jsonLessThanEscape)
}

// mustChromeAsset reads an embedded chrome.css/chrome.js asset. Both are
// embedded above; a read failure means the embed directive itself is
// broken (a compile-time condition), not a runtime one — panicking here
// mirrors template.Must's contract for a compiled-in asset that cannot
// legitimately be missing.
func mustChromeAsset(path string) []byte {
	b, err := chromeAssets.ReadFile(path)
	if err != nil {
		panic("plan: embedded chrome asset missing: " + path + ": " + err.Error())
	}
	return b
}

// mustReviewScript reads assets/review.js through the render package's
// existing renderAssets embed (declared in render.go) — inject.go embeds only
// its own new assets (chrome.css/chrome.js) and reuses that embed rather than
// duplicating the directive.
func mustReviewScript() []byte {
	b, err := renderAssets.ReadFile("assets/review.js")
	if err != nil {
		panic("plan: embedded assets/review.js missing: " + err.Error())
	}
	return b
}
