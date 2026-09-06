package main

// plan_save_resave_cli_test.go covers `ox plan save` and `ox plan list` at the
// COMMAND level — the layer where three PR #879 findings actually lived.
//
// The headline one is the re-save duplicate. internal/plan already had
// TestSave_ResaveFromSameSourceRevisesInPlace, and it was green the whole time,
// because it calls plan.Save directly with an EMPTY meta.Slug. Every real CLI
// save went through savePlanArtifacts, which filled the slug in from the topic
// first — so Save's source-path-reuse branch was unreachable in the field and
// retitling a page still minted a second ledger dir. A unit test one layer down
// cannot see that: the composition root is the only thing that computes the
// argument the branch keys off.
//
// So these tests drive the real cobra RunE and assert on durable ledger state.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/plan"
	"github.com/spf13/cobra"
)

// newPlanSaveCmdForTest builds an isolated cobra command wired to runPlanSave
// with the full real flag set INCLUDING --kind (the flag the legacy route never
// read). Isolated, not the global planSaveCmd, so flag state never leaks
// between tests.
func newPlanSaveCmdForTest() *cobra.Command {
	cmd := &cobra.Command{
		RunE: func(c *cobra.Command, _ []string) error { return runPlanSave(c) },
	}
	cmd.SetContext(context.Background())
	cmd.Flags().String("plan", "", "")
	cmd.Flags().String("annotations", "", "")
	cmd.Flags().String("html", "", "")
	cmd.Flags().String("file", "", "")
	cmd.Flags().String("kind", "", "")
	return cmd
}

// runPlanSaveCLI executes `ox plan save` with the given args and returns
// everything it wrote: the command's own writer AND os.Stdout, which is where
// cli.PrintHint lands.
func runPlanSaveCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newPlanSaveCmdForTest()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	var err error
	hints := captureStdoutForPlanCLI(t, func() { err = cmd.Execute() })
	return buf.String() + hints, err
}

// captureStdoutForPlanCLI redirects os.Stdout for the duration of fn. Needed
// because cli.PrintHint writes to os.Stdout directly, not to the cobra writer,
// so the artifact hint is invisible to a test that only reads the buffer.
func captureStdoutForPlanCLI(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	// Drain concurrently: a full pipe buffer would deadlock the command
	// mid-print.
	drained := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		drained <- string(b)
	}()
	defer func() {
		os.Stdout = orig
		_ = r.Close()
	}()
	fn()
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("close stdout pipe: %v", cerr)
	}
	return <-drained
}

// writeAuthoredPage writes a self-contained page whose <h1> is title. Padded
// past the artifact-nudge size floor so the SAME fixture serves both the
// re-save tests and the unsaved-artifact discovery test.
func writeAuthoredPage(t *testing.T, path, title string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	var body strings.Builder
	body.WriteString("<!doctype html>\n<html><head><meta charset=\"utf-8\">\n")
	body.WriteString("<style>body{font-family:system-ui;background:#0b0d10;color:#e6e8eb}</style>\n")
	fmt.Fprintf(&body, "</head><body>\n<h1>%s</h1>\n", title)
	body.WriteString("<h2>Decision</h2><p>Ship the narrower change.</p>\n")
	body.WriteString("<h2>Risk</h2><p>The ledger grows a duplicate directory.</p>\n")
	// Pad past artifactMinBytes (20 KiB) so findUnsavedArtifacts considers it
	// an authored page rather than a fragment.
	body.WriteString("<!-- ")
	body.WriteString(strings.Repeat("padding to clear the authored-page size floor. ", 600))
	body.WriteString(" -->\n</body></html>\n")
	if err := os.WriteFile(path, []byte(body.String()), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestPlanSaveFile_RetitledResaveRevisesTheSamePlan is the CLI-level regression
// gate for the duplicate-plan defect. It drives the real `ox plan save --file`
// twice against one source path, changing the page's <h1> in between — the
// exact field sequence that produced two ledger entries five minutes apart with
// identical source_plan_path, which a human then had to reconcile by hand with
// `ox plan supersede`.
//
// Red-first: restore the unconditional `slug := plan.Slugify(topic)` in
// savePlanArtifacts and this fails with 2 plans.
func TestPlanSaveFile_RetitledResaveRevisesTheSamePlan(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "")

	page := filepath.Join(root, ".context", "plan.html")
	writeAuthoredPage(t, page, "Ledger Clone Hardening")
	if _, err := runPlanSaveCLI(t, "--file", page); err != nil {
		t.Fatalf("first save: %v", err)
	}

	// The human retitles the page and saves again.
	writeAuthoredPage(t, page, "Ledger Clone Hardening And Repair")
	if _, err := runPlanSaveCLI(t, "--file", page); err != nil {
		t.Fatalf("second save: %v", err)
	}

	gitRoot := findGitRoot()
	plans, err := plan.List(gitRoot)
	if err != nil {
		t.Fatalf("plan.List: %v", err)
	}
	if len(plans) != 1 {
		var got []string
		for _, p := range plans {
			got = append(got, p.Slug)
		}
		t.Fatalf("re-saving one source file produced %d plans (%v), want 1 revised in place", len(plans), got)
	}

	// Revised, not merely deduplicated: the surviving plan must carry the NEW
	// title, or the second save was silently discarded instead of applied.
	if !strings.Contains(plans[0].Topic, "Repair") {
		t.Errorf("surviving plan topic = %q, want the retitled page's topic — the re-save did not land", plans[0].Topic)
	}

	// The reverse link and the provenance log both name the plan by slug, and
	// the derived case now leaves meta.Slug empty on the way in. An empty slug
	// on the way out is exactly how produced_plans goes silently unset.
	meta, err := plan.LoadMeta(plans[0].Dir)
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if meta.Slug == "" {
		t.Error("saved meta.json carries an empty slug — plan.Save's derived slug was never written back")
	}
}

// TestPlanSaveFile_ExplicitSlugStillWins pins the other half of the same
// change: an author who declares <meta name="ox-plan-slug"> owns the identity,
// and the source-path reuse must not override it.
func TestPlanSaveFile_ExplicitSlugStillWins(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "")

	page := filepath.Join(root, ".context", "plan.html")
	writeAuthoredPage(t, page, "Some Ignored Title")
	raw, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	withSlug := strings.Replace(string(raw), "<meta charset=\"utf-8\">",
		"<meta charset=\"utf-8\">\n<meta name=\"ox-plan-slug\" content=\"declared-identity\">", 1)
	if err := os.WriteFile(page, []byte(withSlug), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runPlanSaveCLI(t, "--file", page); err != nil {
		t.Fatalf("save: %v", err)
	}

	plans, err := plan.List(findGitRoot())
	if err != nil || len(plans) != 1 {
		t.Fatalf("plan.List: err=%v n=%d", err, len(plans))
	}
	if plans[0].Slug != "declared-identity" {
		t.Errorf("slug = %q, want the page-declared %q", plans[0].Slug, "declared-identity")
	}
}

// TestPlanSave_KindValidatedOnBothRoutes covers the flag the legacy route never
// read: `--kind review` used to persist meta.json.kind as "plan", and an
// unknown value passed straight through. --kind is now validated once, up
// front, for both routes, and rejected outright on the deprecated --plan route
// rather than being silently dropped.
//
// Red-first: delete the up-front ValidKind check and the legacy-route rejection
// in runPlanSave; all three subtests fail with "want error, got nil".
func TestPlanSave_KindValidatedOnBothRoutes(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "")

	// Real inputs on both routes, so a missing guard fails the assertion by
	// SAVING successfully rather than by erroring for some unrelated reason.
	planMD := filepath.Join(root, ".context", "plan.md")
	if err := os.MkdirAll(filepath.Dir(planMD), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planMD, []byte("# Legacy Route Plan\n\n## Decision\nShip it.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	annPath := filepath.Join(root, ".context", "annotations.json")
	if err := os.WriteFile(annPath, []byte(`{"annotations":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(root, ".context", "plan.html")
	writeAuthoredPage(t, page, "File Route Plan")

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "legacy --plan route rejects a VALID kind rather than dropping it",
			args: []string{"--plan", planMD, "--annotations", annPath, "--kind", "review"},
			want: "--kind is not supported with the legacy --plan route",
		},
		{
			name: "legacy --plan route rejects an unknown kind",
			args: []string{"--plan", planMD, "--annotations", annPath, "--kind", "bogus"},
			want: `unknown --kind "bogus"`,
		},
		{
			name: "--file route rejects an unknown kind",
			args: []string{"--file", page, "--kind", "bogus"},
			want: `unknown --kind "bogus"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := runPlanSaveCLI(t, tt.args...)
			if err == nil {
				t.Fatalf("want an error, got nil — the flag was accepted and silently dropped")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

// TestPlanSaveFile_ValidKindReachesMeta is the companion positive case: a kind
// the CLI accepts must actually land in meta.json, or the validation above
// would be guarding nothing.
func TestPlanSaveFile_ValidKindReachesMeta(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "")

	page := filepath.Join(root, ".context", "review.html")
	writeAuthoredPage(t, page, "Device Capture Review Sheet")
	if _, err := runPlanSaveCLI(t, "--file", page, "--kind", "review"); err != nil {
		t.Fatalf("save: %v", err)
	}

	plans, err := plan.List(findGitRoot())
	if err != nil || len(plans) != 1 {
		t.Fatalf("plan.List: err=%v n=%d", err, len(plans))
	}
	meta, err := plan.LoadMeta(plans[0].Dir)
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if meta.Kind != plan.KindReview {
		t.Errorf("meta.kind = %q, want %q", meta.Kind, plan.KindReview)
	}
}

// TestPrimeGuidance_SaveCommandNamesTheKindFlag is the other end of the --kind
// story, and the reason it belongs beside the validation tests above: the CLI
// can validate --kind all it likes, but if the command every AI coworker is
// primed with omits the flag, every mockup, review sheet and evidence page
// still lands in the ledger labeled "plan". The guidance used to mention the
// kinds in a parenthetical detached from the command, which reads as optional
// background rather than part of the invocation.
//
// Red-first: put the kinds back in a parenthetical and drop them from the
// command string — this fails.
func TestPrimeGuidance_SaveCommandNamesTheKindFlag(t *testing.T) {
	var sb strings.Builder
	writePlanEnrichmentGuidance(&sb, "claude-code")
	got := sb.String()

	const want = "`ox plan save --file plan.html --kind plan|mockup|review|evidence`"
	if !strings.Contains(got, want) {
		t.Errorf("primed save command does not carry --kind; want %s\ngot: %s", want, got)
	}
}

// runPlanListCLI drives runPlanList and returns (cobra writer output, stdout
// output). They are kept SEPARATE on purpose: the --json contract is that the
// writer carries pure JSON and no hint text reaches stdout either.
func runPlanListCLI(t *testing.T, jsonOut bool) (string, string) {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	var err error
	hints := captureStdoutForPlanCLI(t, func() { err = runPlanList(cmd, jsonOut) })
	if err != nil {
		t.Fatalf("runPlanList(json=%v): %v", jsonOut, err)
	}
	return buf.String(), hints
}

// TestPlanList_EmptyLedgerStillSurfacesUnsavedArtifact covers the project shape
// the artifact discovery exists for and was the one shape it skipped: nothing
// saved yet, and an authored page sitting unsaved in the working tree. The
// old `len(plans) == 0` early return fired before discovery ran, so the human
// most in need of the nudge was the only one who never saw it.
//
// The --json subtest is the other half of the contract: hint text must never
// reach a scripted parse.
//
// Red-first: restore the bare `return nil` after the empty-list message and the
// human subtest fails with "hint missing".
func TestPlanList_EmptyLedgerStillSurfacesUnsavedArtifact(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "")

	page := filepath.Join(root, ".context", "capture-review.html")
	writeAuthoredPage(t, page, "Device Capture Review Sheet")

	// Guard the premise: this test proves nothing if the ledger is not empty.
	if plans, err := plan.List(findGitRoot()); err != nil || len(plans) != 0 {
		t.Fatalf("premise broken — want an empty ledger, got err=%v n=%d", err, len(plans))
	}

	t.Run("human output carries both the empty state and the hint", func(t *testing.T) {
		out, hints := runPlanListCLI(t, false)
		if !strings.Contains(out, "No saved plans yet") {
			t.Errorf("empty-state message missing from %q", out)
		}
		if !strings.Contains(hints, "not in the ledger") {
			t.Errorf("unsaved-artifact hint missing; stdout was %q", hints)
		}
		if !strings.Contains(hints, "capture-review.html") {
			t.Errorf("hint does not name the artifact; stdout was %q", hints)
		}
	})

	t.Run("json output stays pure", func(t *testing.T) {
		out, hints := runPlanListCLI(t, true)
		var decoded []plan.PlanInfo
		if err := json.Unmarshal([]byte(out), &decoded); err != nil {
			t.Fatalf("--json output is not valid JSON: %v\noutput: %q", err, out)
		}
		if len(decoded) != 0 {
			t.Errorf("decoded %d plans, want 0", len(decoded))
		}
		if strings.Contains(out, "not in the ledger") || strings.Contains(hints, "not in the ledger") {
			t.Errorf("hint text leaked into the --json path: writer=%q stdout=%q", out, hints)
		}
	})
}
