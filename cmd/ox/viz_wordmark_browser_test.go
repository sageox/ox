//go:build browser

package main

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/sageox/ox/internal/viz"
)

// Drive the shipped recipe in a real CSS engine: checking its width declaration
// alone misses a page-wide minimum that overrides that declaration.
func TestBrowser_WordmarkIgnoresPageSVGMinimums(t *testing.T) {
	pattern, ok := viz.PatternByID("wordmark")
	if !ok {
		t.Fatal("wordmark recipe missing")
	}
	block := func(language string) string {
		t.Helper()
		_, rest, found := strings.Cut(pattern.Body, "```"+language+"\n")
		if !found {
			t.Fatalf("wordmark recipe has no %s fence", language)
		}
		body, _, found := strings.Cut(rest, "```")
		if !found {
			t.Fatalf("wordmark recipe has an unterminated %s fence", language)
		}
		return body
	}
	html, css := block("html"), block("css")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Put author CSS on either side of the recipe: attribution should not
		// depend on where an AI coworker pastes the snippet into the page.
		authorCSS := `<style>body{margin:0}svg{display:block;min-width:790px;width:100%;height:auto}</style>`
		before, after := authorCSS, ""
		if r.URL.Query().Get("order") == "after" {
			before, after = "", authorCSS
		}
		_, _ = fmt.Fprintf(w, `<!doctype html><html><head>%s<style>%s</style>%s</head><body><h1>A readable plan</h1><svg id="diagram" viewBox="0 0 1000 100"><title>Wide diagram</title><rect width="1000" height="100" fill="gray"/></svg>%s</body></html>`, before, css, after, html)
	}))
	defer server.Close()
	ctx := newChrome(t)
	for _, order := range []string{"before", "after"} {
		for _, width := range []int{1500, 390} {
			for _, theme := range []string{"dark", "light"} {
				t.Run(fmt.Sprintf("%s/%d/%s", order, width, theme), func(t *testing.T) {
					var sizes struct {
						LogoWidth    float64 `json:"logoWidth"`
						LogoHeight   float64 `json:"logoHeight"`
						DiagramWidth float64 `json:"diagramWidth"`
						VisibleMarks int     `json:"visibleMarks"`
					}
					if err := chromedp.Run(ctx,
						chromedp.EmulateViewport(int64(width), 815),
						chromedp.Navigate(server.URL+"?order="+order),
						chromedp.Evaluate(fmt.Sprintf(`document.documentElement.dataset.theme=%q`, theme), nil),
						chromedp.Evaluate(`(() => {
							const marks = [...document.querySelectorAll('[data-ox-wordmark] svg')].filter(el => el.getBoundingClientRect().width > 0);
							const logo = marks[0]?.getBoundingClientRect();
							return {logoWidth: logo?.width || 0, logoHeight: logo?.height || 0,
								diagramWidth: document.querySelector('#diagram').getBoundingClientRect().width,
								visibleMarks: marks.length};
						})()`, &sizes),
					); err != nil {
						t.Fatalf("measure wordmark: %v", err)
					}
					if sizes.VisibleMarks != 1 {
						t.Errorf("visible theme variants = %d, want 1", sizes.VisibleMarks)
					}
					// Allow subpixel SVG aspect-ratio rounding, but no visible inflation.
					if math.Abs(sizes.LogoWidth-96) > 0.1 || math.Abs(sizes.LogoHeight-30) > 0.1 {
						t.Errorf("wordmark = %.2f x %.2f, want 96 x 30 CSS pixels", sizes.LogoWidth, sizes.LogoHeight)
					}
					if want := float64(max(width, 790)); math.Abs(sizes.DiagramWidth-want) > 0.1 {
						t.Errorf("diagram width = %.2f, want %.2f (author minimum preserved)", sizes.DiagramWidth, want)
					}
				})
			}
		}
	}
}
