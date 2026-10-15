package main

// plan_html_routing_test.go covers how plan commands decide that a --file is
// an authored HTML page rather than markdown (ox#1070).

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
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

// identityMetaRe matches the identity <meta> tags save stamps into a <head>.
var identityMetaRe = regexp.MustCompile(`<meta name="sageox:[^"]*" content="[^"]*">\n`)

// assertSavedAsAuthoredPage checks the ledger holds exactly one plan, saved
// HTML-primary, with the page as plan.html and markdown derived from it as
// plan.md. Save stamps the plan's identity <meta> tags into a page that has a
// <head>; with those taken out the page must match byte for byte.
func assertSavedAsAuthoredPage(t *testing.T, page string) {
	t.Helper()
	info, meta := savedOnlyPlan(t)
	if meta.Primary != plan.PrimaryHTML {
		t.Errorf("meta.primary = %q, want %q", meta.Primary, plan.PrimaryHTML)
	}
	if !info.HasHTML {
		t.Fatal("no plan.html stored: the authored page was dropped")
	}
	stored, err := os.ReadFile(filepath.Join(info.Dir, "plan.html"))
	if err != nil {
		t.Fatal(err)
	}
	if got := identityMetaRe.ReplaceAllString(string(stored), ""); got != page {
		t.Errorf("plan.html is not the authored page verbatim (identity metas aside):\n%s", stored)
	}
	md, err := os.ReadFile(filepath.Join(info.Dir, "plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(md), "<title>") || !strings.Contains(string(md), "# Real title") {
		t.Errorf("plan.md is not markdown derived from the page:\n%s", md)
	}
}

// TestPlanEnrichSave_KeepsAuthoredHTMLPage is the ox#1115 regression: enrich
// swapped an authored page for its derived markdown, then saved with a nil
// page, so the ledger got plan.md only, no plan.html and an empty primary.
// Both enrich save paths, --persist (the ExitPlanMode hook) and the --text
// auto-save, must store the page the way `ox plan save --file` does.
func TestPlanEnrichSave_KeepsAuthoredHTMLPage(t *testing.T) {
	pages := map[string]string{
		"no doctype":   doctypelessPage,
		"with doctype": "<!doctype html><html><head><title>My Plan</title></head><body><h1>Real title</h1><p>Body.</p></body></html>",
	}
	modes := map[string][]string{
		"persist": {"persist", "true"},
		"text":    {"text", "true"},
	}
	for pageName, page := range pages {
		for modeName, flag := range modes {
			t.Run(modeName+" "+pageName, func(t *testing.T) {
				root := newPlanCaptureTestRepo(t)
				t.Setenv("SAGEOX_AGENT_ID", "")

				src := filepath.Join(root, ".context", "plan.html")
				writePlanSource(t, src, page)
				runPlanEnrich(t, "file", src, flag[0], flag[1])

				assertSavedAsAuthoredPage(t, page)
			})
		}
	}
}

// TestPlanEnrichSave_MarkdownStaysMarkdownPrimary: the ox#1115 fix must not
// turn a markdown plan into an HTML-primary one.
func TestPlanEnrichSave_MarkdownStaysMarkdownPrimary(t *testing.T) {
	for _, mode := range []string{"persist", "text"} {
		t.Run(mode, func(t *testing.T) { checkEnrichSavesMarkdownPrimary(t, mode) })
	}
}

func checkEnrichSavesMarkdownPrimary(t *testing.T, mode string) {
	root := newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "")

	const md = "# Cache warmup\n\n## Approach\n\nWarm the cache on boot.\n"
	src := filepath.Join(root, ".context", "plan.md")
	writePlanSource(t, src, md)
	runPlanEnrich(t, "file", src, mode, "true")

	info, meta := savedOnlyPlan(t)
	if meta.Primary != "" || info.HasHTML {
		t.Errorf("markdown plan saved with primary=%q html=%v, want markdown-primary with no plan.html", meta.Primary, info.HasHTML)
	}
	stored, err := os.ReadFile(filepath.Join(info.Dir, "plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != md {
		t.Errorf("plan.md is not the authored markdown verbatim:\n%s", stored)
	}
}

// TestPlanReviewSaveDraft_KeepsAuthoredHTMLPage: `ox plan review --file
// plan.html` saved the same way as enrich (ox#1115) and also enriched the raw
// HTML, so it stored the page's markup as plan.md and dropped the page.
func TestPlanReviewSaveDraft_KeepsAuthoredHTMLPage(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "")

	src := filepath.Join(root, ".context", "plan.html")
	writePlanSource(t, src, doctypelessPage)
	cmd := &cobra.Command{}
	cmd.Flags().StringSlice("companion", nil, "")
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if _, err := reviewSaveDraft(cmd, src); err != nil {
		t.Fatalf("reviewSaveDraft: %v", err)
	}

	assertSavedAsAuthoredPage(t, doctypelessPage)
}

// TestPlanEnrichPersist_StdinPageKeepsAuthoredHTML: the ExitPlanMode hook pipes
// the plan on stdin with no path, so the page is recognized by its content.
func TestPlanEnrichPersist_StdinPageKeepsAuthoredHTML(t *testing.T) {
	newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "")

	const page = "<!doctype html><html><head><title>My Plan</title></head><body><h1>Real title</h1><p>Body.</p></body></html>"
	cmd := planEnrichCmd
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(page))
	t.Cleanup(func() {
		cmd.SetIn(strings.NewReader(""))
		_ = cmd.Flags().Set("persist", "false")
	})
	if err := cmd.Flags().Set("persist", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("enrich RunE: %v", err)
	}
	var res plan.Result
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("--persist stdout is not one JSON document: %v\n%s", err, out.String())
	}

	assertSavedAsAuthoredPage(t, page)
}

// TestPlanReviewSaveDraft_ReturnsTheSavedSlug: an authored page's
// ox-plan-slug decides where it is saved, so the slug handed to the review
// loop must be that one, not the one its title would give.
func TestPlanReviewSaveDraft_ReturnsTheSavedSlug(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "")

	const page = `<meta name="ox-plan-slug" content="custom-slug"><title>My Plan</title><h1>Real title</h1><p>Body.</p>`
	src := filepath.Join(root, ".context", "plan.html")
	writePlanSource(t, src, page)
	cmd := &cobra.Command{}
	cmd.Flags().StringSlice("companion", nil, "")
	slug, err := reviewSaveDraft(cmd, src)
	if err != nil {
		t.Fatalf("reviewSaveDraft: %v", err)
	}
	info, _ := savedOnlyPlan(t)
	if slug != info.Slug || slug != "custom-slug" {
		t.Errorf("reviewSaveDraft returned %q, saved plan slug is %q, want both %q", slug, info.Slug, "custom-slug")
	}
}

// TestPlanReviewSaveDraft_ReportsWhySaveFailed: when the page's ox-plan-slug
// names two live plans, save refuses to guess. review --file must say so and
// name the cause, not just "could not save draft".
func TestPlanReviewSaveDraft_ReportsWhySaveFailed(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "")

	const page = `<meta name="ox-plan-slug" content="dup-slug"><title>My Plan</title><h1>Real title</h1><p>Body.</p>`
	src := filepath.Join(root, ".context", "plan.html")
	writePlanSource(t, src, page)
	cmd := &cobra.Command{}
	cmd.Flags().StringSlice("companion", nil, "")
	if _, err := reviewSaveDraft(cmd, src); err != nil {
		t.Fatalf("first save: %v", err)
	}
	// a second live plan claiming the same slug, as a copied page would leave
	info, _ := savedOnlyPlan(t)
	twin := filepath.Join(filepath.Dir(info.Dir), "1999-01-01-dup-slug")
	if err := os.MkdirAll(twin, 0o755); err != nil {
		t.Fatal(err)
	}
	meta, err := os.ReadFile(filepath.Join(info.Dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(twin, "meta.json"), meta, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = reviewSaveDraft(cmd, src)
	var amb *plan.AmbiguousSlugError
	if !errors.As(err, &amb) {
		t.Fatalf("err = %v, want it to wrap the AmbiguousSlugError that blocked the save", err)
	}
	if !strings.Contains(err.Error(), "could not save draft to the ledger") {
		t.Errorf("err = %q, want the review-facing context kept", err)
	}
}

// TestWritePlanHuman_NextStepFollowsWhatWasSaved: once enrich has saved the
// authored page as the plan of record, the advice must not ask the coworker
// to author and save a plan.html again; for a markdown plan it still must.
func TestWritePlanHuman_NextStepFollowsWhatWasSaved(t *testing.T) {
	result := plan.Result{Signals: plan.SignalSummary{Material: true}}
	for _, tc := range []struct {
		name      string
		pageSaved bool
		want      string
		notWant   string
	}{
		{"page saved", true, "The page is saved as the plan of record", "Author a visual `plan.html`"},
		{"markdown saved", false, "Author a visual `plan.html`", "The page is saved as the plan of record"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			var out bytes.Buffer
			cmd.SetOut(&out)
			if err := writePlanHuman(cmd, result, "/ledger/data/plans/x", tc.pageSaved); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tc.want) || strings.Contains(out.String(), tc.notWant) {
				t.Errorf("advice for %s:\n%s", tc.name, out.String())
			}
		})
	}
}
