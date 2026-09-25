package main

// plan_html_routing_test.go covers how plan commands decide that a --file is
// an authored HTML page rather than markdown (ox#1070).

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/plan"
	"github.com/spf13/cobra"
)

// doctypelessPage is the ox#1070 repro: a page with no doctype, <html>, <head> or <body>.
const doctypelessPage = `<title>My Plan</title><style>body{color:#111}</style><h1>Real title</h1><p>Body.</p>`

// writePlanSource writes content to path, creating parent dirs.
func writePlanSource(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// savedOnlyPlan returns the single plan in the ledger and its meta.
func savedOnlyPlan(t *testing.T) (plan.PlanInfo, plan.Meta) {
	t.Helper()
	plans, err := plan.List(findGitRoot())
	if err != nil || len(plans) != 1 {
		t.Fatalf("plan.List: err=%v n=%d, want exactly 1 saved plan", err, len(plans))
	}
	meta, err := plan.LoadMeta(plans[0].Dir)
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	return plans[0], meta
}

// TestPlanSaveFile_PageWithoutDoctypeSavesAsHTMLPrimary: without it a .html page
// with no doctype saves as markdown (raw HTML in plan.md, no plan.html, generic slug).
func TestPlanSaveFile_PageWithoutDoctypeSavesAsHTMLPrimary(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "")

	page := filepath.Join(root, ".context", "plan.html")
	writePlanSource(t, page, doctypelessPage)
	out, err := runPlanSaveCLI(t, "--file", page)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !strings.Contains(out, "Saved HTML-primary") {
		t.Errorf("save output does not report an HTML-primary save:\n%s", out)
	}

	info, meta := savedOnlyPlan(t)
	if meta.Primary != plan.PrimaryHTML {
		t.Errorf("meta.primary = %q, want %q", meta.Primary, plan.PrimaryHTML)
	}
	if info.Slug != "real-title" {
		t.Errorf("slug = %q, want %q from the page's <h1>", info.Slug, "real-title")
	}
	if !info.HasHTML {
		t.Fatal("no plan.html stored: the authored page was dropped")
	}
	stored, err := os.ReadFile(filepath.Join(info.Dir, "plan.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != doctypelessPage {
		t.Errorf("plan.html is not the authored page verbatim:\n%s", stored)
	}
	md, err := os.ReadFile(filepath.Join(info.Dir, "plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(md), "<title>") || !strings.Contains(string(md), "# Real title") {
		t.Errorf("plan.md is not markdown derived from the page:\n%s", md)
	}
}

// TestPlanSaveFile_MarkdownOpeningWithHTMLStaysMarkdown: without it a markdown
// plan that opens with a tag or embeds an html fence is misread as a page.
func TestPlanSaveFile_MarkdownOpeningWithHTMLStaysMarkdown(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "")

	const md = "<div align=\"center\"><img src=\"logo.png\" alt=\"logo\"></div>\n\n# Cache warmup\n\n" +
		"## Demo\n\n```html\n<!doctype html><html><body>demo</body></html>\n```\n"
	src := filepath.Join(root, ".context", "plan.md")
	writePlanSource(t, src, md)
	out, err := runPlanSaveCLI(t, "--file", src)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if strings.Contains(out, "HTML-primary") {
		t.Errorf("a markdown plan was saved as HTML-primary:\n%s", out)
	}

	info, meta := savedOnlyPlan(t)
	if meta.Primary != "" || info.HasHTML {
		t.Errorf("markdown plan saved with primary=%q html=%v, want markdown-primary with no plan.html", meta.Primary, info.HasHTML)
	}
	if info.Slug != "cache-warmup" {
		t.Errorf("slug = %q, want %q from the markdown H1", info.Slug, "cache-warmup")
	}
	stored, err := os.ReadFile(filepath.Join(info.Dir, "plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != md {
		t.Errorf("plan.md is not the authored markdown verbatim:\n%s", stored)
	}
}

// TestPlanRenderFile_PageWithoutDoctypeKeepsAuthoredMarkup: without it render
// replaces a .html page that has no doctype with the markdown template.
func TestPlanRenderFile_PageWithoutDoctypeKeepsAuthoredMarkup(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "test") // headless: never open a browser
	t.Chdir(t.TempDir())               // outside any git repo: no ledger save

	page := filepath.Join(t.TempDir(), "plan.html")
	writePlanSource(t, page, doctypelessPage)
	outPath := filepath.Join(t.TempDir(), "out.html")

	cmd := planRenderCmd
	cmd.SetOut(&bytes.Buffer{})
	if err := runPlanRenderFresh(cmd, page, outPath, false, false, ""); err != nil {
		t.Fatalf("runPlanRenderFresh: %v", err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.HasPrefix(s, doctypelessPage) || !strings.Contains(s, plan.ChromeMarkerStart) {
		t.Errorf("render is not the authored page with ox chrome appended:\n%.300s", s)
	}
}

// TestPlanEnrichCmd_PageWithoutDoctypeEnrichesDerivedMarkdown checks enrich reads a doctype-less .html page as HTML.
// Without it, enrich runs its markdown detectors over raw HTML tags and misreads the plan.
func TestPlanEnrichCmd_PageWithoutDoctypeEnrichesDerivedMarkdown(t *testing.T) {
	root := newPlanEnrichTestRepo(t)
	p := filepath.Join(root, "plan.html")
	writePlanSource(t, p, `<title>Auth plan</title><h2>Approach</h2><p>Edit <code>internal/auth/token.go</code> and <code>internal/auth/refresh.go</code>.</p><h2>Rollout</h2><p>Roll out in phases over two weeks.</p>`)

	var res plan.Result
	if err := json.Unmarshal([]byte(runPlanEnrich(t, "file", p)), &res); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	// the page's two <h2> sections only become plan steps through the derived markdown
	if res.Signals.Steps != 2 {
		t.Errorf("expected Signals.Steps=2 from the page's <h2> sections, got %d", res.Signals.Steps)
	}
}

// TestPlanLintFile_RoutesByFileName checks lint accepts a doctype-less .html page and still refuses a markdown file.
// Without it, lint rejects the same page that save now stores as HTML, or starts linting markdown as a page.
func TestPlanLintFile_RoutesByFileName(t *testing.T) {
	root := newPlanEnrichTestRepo(t)
	page := filepath.Join(root, "plan.html")
	writePlanSource(t, page, doctypelessPage)
	md := filepath.Join(root, "plan.md")
	writePlanSource(t, md, "## Approach\nShip it.\n")
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})

	if err := runPlanLintFile(cmd, page, false, ""); err != nil {
		t.Errorf("lint failed on a doctype-less .html page: %v", err)
	}
	err := runPlanLintFile(cmd, md, false, "")
	if err == nil || !strings.Contains(err.Error(), "does not look like an authored HTML page") {
		t.Errorf("lint must refuse a markdown file, got %v", err)
	}
}
