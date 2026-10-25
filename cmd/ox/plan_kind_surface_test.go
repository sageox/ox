package main

// plan_kind_surface_test.go covers the cmd/ox wiring of --kind: the flag is
// validated at the command boundary, and the kind it carries reaches the craft
// linter and every confirmation line. The kind-suppression logic itself is
// proven at the package level (internal/plan/craft_lint_test.go); this file
// proves the CLI actually plumbs the kind through, which is where the real
// `--kind mockup` save went wrong on 2026-09-22.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/plan"
	"github.com/spf13/cobra"
)

// authoredKindPage is non-trivial (it names two distinct files, so
// Signals.NonTrivial fires) and has no closed Implementation notes appendix, so
// under KindPlan it earns craft.missing-progressive-disclosure — the finding
// that must disappear for a kind with no implementer to relocate depth for.
const authoredKindPage = `<!doctype html>
<html><head><title>Launch sequence</title></head><body>
<section><h2>Approach</h2>
<p>Update internal/plan/lint.go and cmd/ox/plan.go for the rollout.</p>
<figure class="barc"><div class="bar-row"></div></figure>
</section></body></html>
`

func writeKindPage(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(authoredKindPage), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// setKindFlags sets flags on a SHARED package-level command and restores them
// afterwards. Cobra command vars are global, so a leaked --kind turns an
// unrelated test in this package into a false pass or failure.
func setKindFlags(t *testing.T, cmd *cobra.Command, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		if err := cmd.Flags().Set(k, v); err != nil {
			t.Fatalf("set --%s: %v", k, err)
		}
		t.Cleanup(func() { _ = cmd.Flags().Set(k, "") })
	}
}

// TestPlanRenderCmd_RejectsUnknownKind and its lint twin pin the reason
// kindFlag exists: an unvalidated kind is not a harmless typo. `--kind reivew`
// used to fall through as the default kind, silently persisting the artifact as
// a plan — the exact mislabeling the flag was added to prevent.
func TestPlanRenderCmd_RejectsUnknownKind(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "test")
	t.Chdir(t.TempDir())

	cmd := planRenderCmd
	cmd.SetOut(&bytes.Buffer{})
	setKindFlags(t, cmd, map[string]string{"file": writeKindPage(t, "plan.html"), "kind": "reivew"})

	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("an unknown --kind must be refused, not silently saved as a plan")
	}
	for _, want := range []string{"reivew", "mockup", "evidence"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must echo the bad kind and name the valid ones, missing %q: %v", want, err)
		}
	}
}

func TestPlanLintCmd_RejectsUnknownKind(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "test")
	t.Chdir(t.TempDir())

	cmd := planLintCmd
	cmd.SetOut(&bytes.Buffer{})
	setKindFlags(t, cmd, map[string]string{"file": writeKindPage(t, "plan.html"), "kind": "mokup"})

	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("an unknown --kind must be refused before any lint work")
	}
	if !strings.Contains(err.Error(), "mokup") {
		t.Errorf("error must echo the bad kind: %v", err)
	}
}

// TestPlanLintCmd_AcceptsKindAndLints is the lint twin of the render happy
// path: a valid kind must reach runPlanLintFile rather than erroring, so the
// rejection above cannot be satisfied by refusing every kind.
func TestPlanLintCmd_AcceptsKindAndLints(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "test")
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	cmd := planLintCmd
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	setKindFlags(t, cmd, map[string]string{"file": writeKindPage(t, "plan.html"), "kind": string(plan.KindMockup)})

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("lint with a valid --kind: %v", err)
	}
	if out.Len() == 0 {
		t.Error("lint produced no report")
	}
}

// TestPlanRenderCmd_FreshRenderAcceptsKind proves the happy path through the
// same command boundary: a valid kind renders rather than erroring, so the
// validation above cannot be satisfied by rejecting everything.
func TestPlanRenderCmd_FreshRenderAcceptsKind(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "test")
	t.Chdir(t.TempDir())

	outPath := filepath.Join(t.TempDir(), "out.html")
	cmd := planRenderCmd
	cmd.SetOut(&bytes.Buffer{})
	setKindFlags(t, cmd, map[string]string{
		"file":   writeKindPage(t, "plan.html"),
		"kind":   string(plan.KindMockup),
		"output": outPath,
	})

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("render with a valid --kind: %v", err)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("render produced no page: %v", err)
	}
}

// TestPlanLintFile_KindReachesCraftLinter is the CLI-level proof that --kind is
// not merely validated but CARRIED: the same unsaved page linted as a plan and
// as a mockup must produce different craft findings.
func TestPlanLintFile_KindReachesCraftLinter(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "test")
	t.Chdir(t.TempDir())
	page := writeKindPage(t, "plan.html")

	lint := func(kind string) string {
		var out bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		if err := runPlanLintFile(cmd, page, false, kind); err != nil {
			t.Fatalf("lint --kind %q: %v", kind, err)
		}
		return out.String()
	}

	const planOnly = "craft.missing-progressive-disclosure"
	asPlan := lint(string(plan.KindPlan))
	if !strings.Contains(asPlan, planOnly) {
		t.Fatalf("fixture no longer earns %s as a plan, so the mockup assertion below would be vacuous:\n%s", planOnly, asPlan)
	}
	if asMockup := lint(string(plan.KindMockup)); strings.Contains(asMockup, planOnly) {
		t.Errorf("a mockup has no implementer to relocate depth for and must not be nagged with %s:\n%s", planOnly, asMockup)
	}
}

// TestPlanRenderFresh_PersistsTheKindItWasGiven closes the loop between the
// flag and the ledger: rendering an authored page inside a repo saves it, and
// what it saves must be recorded as the kind the author asked for. A kind that
// is validated at the boundary but dropped before the write is indistinguish-
// able, from the ledger's side, from never having been passed.
func TestPlanRenderFresh_PersistsTheKindItWasGiven(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "test")
	gitRoot := newPlanStatusTestRepo(t)

	cmd := planRenderCmd
	cmd.SetOut(&bytes.Buffer{})
	outPath := filepath.Join(t.TempDir(), "out.html")
	if err := runPlanRenderFresh(cmd, writeKindPage(t, "plan.html"), outPath, false, false, string(plan.KindMockup)); err != nil {
		t.Fatalf("runPlanRenderFresh: %v", err)
	}

	plans, err := plan.List(gitRoot)
	if err != nil || len(plans) == 0 {
		t.Fatalf("render did not save an artifact to the ledger: %v (%d plans)", err, len(plans))
	}
	meta, err := plan.LoadMeta(plans[0].Dir)
	if err != nil {
		t.Fatalf("load meta: %v", err)
	}
	if meta.Kind != plan.KindMockup {
		t.Errorf("ledger recorded kind %q, want %q — the --kind never reached the save", meta.Kind, plan.KindMockup)
	}
}

// TestPlanLint_SavedArtifactHonorsItsOwnKind proves the saved-artifact lint
// path reads WHAT the artifact is from its meta instead of assuming a plan.
// Without it, `ox plan lint <slug>` re-issues against a saved mockup exactly
// the nag that `--kind mockup` was added to suppress at save time.
func TestPlanLint_SavedArtifactHonorsItsOwnKind(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "test")
	gitRoot := newPlanStatusTestRepo(t)

	// A Result that genuinely earns both plan-shaped findings. Saving an empty
	// Result would make the mockup assertion below vacuous: a page with no
	// signals earns no craft findings under ANY kind.
	res := plan.Result{MockupSection: "Approach", Signals: plan.SignalSummary{Material: true}}
	barren := []byte(`<!doctype html><html><head><title>K</title></head><body><p>prose only</p></body></html>`)

	lintSaved := func(t *testing.T, kind plan.ArtifactKind) string {
		t.Helper()
		in := plan.Input{Raw: "# K " + string(kind) + "\n"}
		dir := savePlanArtifacts(gitRoot, in, res, barren, plan.PrimaryHTML, withKind(string(kind)))
		if dir == "" {
			t.Fatal("save failed")
		}
		var out bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		if err := runPlanLint(cmd, filepath.Base(dir), false); err != nil {
			t.Fatalf("runPlanLint: %v", err)
		}
		return out.String()
	}

	planOut := lintSaved(t, plan.KindPlan)
	for _, want := range []string{"craft.missing-mockup", "craft.missing-progressive-disclosure"} {
		if !strings.Contains(planOut, want) {
			t.Fatalf("saved PLAN must still earn %s, or the mockup assertion proves nothing:\n%s", want, planOut)
		}
	}
	mockupOut := lintSaved(t, plan.KindMockup)
	for _, banned := range []string{"craft.missing-mockup", "craft.missing-progressive-disclosure"} {
		if strings.Contains(mockupOut, banned) {
			t.Errorf("a saved mockup must not be linted as a plan; got %s:\n%s", banned, mockupOut)
		}
	}
}

// TestPlanSaveFile_MarkdownConfirmationNamesTheKind covers the markdown-primary
// save tail. Being told "Saved plan to ledger" after saving a design mockup is
// the single-noun collapse ArtifactKind exists to end, and the confirmation is
// the only place the author sees what ox concluded.
func TestPlanSaveFile_MarkdownConfirmationNamesTheKind(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "test")
	newPlanStatusTestRepo(t)

	mdPath := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(mdPath, []byte("# Launch sequence\n\n## Approach\n\nShip it.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := runPlanSaveFile(cmd, mdPath, "", string(plan.KindEvidence)); err != nil {
		t.Fatalf("runPlanSaveFile: %v", err)
	}
	if !strings.Contains(out.String(), "Saved evidence to ledger") {
		t.Errorf("confirmation must name what was saved, got: %s", out.String())
	}
}

// TestPlanRenderCmd_RejectsUnknownKindOnSavedRender pins validation at the
// COMMAND boundary rather than on one branch of it. `ox plan render <slug>`
// returns through the saved-artifact path, which legitimately ignores --kind
// (the stored kind wins). Validating only on the fresh-render branch therefore
// made `--kind reivew` silently accepted there — the same typo-swallowing the
// flag was added to prevent, just one path over.
func TestPlanRenderCmd_RejectsUnknownKindOnSavedRender(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "test")
	gitRoot := newPlanStatusTestRepo(t)

	dir := savePlanArtifacts(gitRoot, plan.Input{Raw: "# Saved kind guard\n"}, plan.Result{}, []byte(authoredKindPage), plan.PrimaryHTML)
	if dir == "" {
		t.Fatal("save failed")
	}

	cmd := planRenderCmd
	cmd.SetOut(&bytes.Buffer{})
	setKindFlags(t, cmd, map[string]string{
		"kind":   "reivew",
		"output": filepath.Join(t.TempDir(), "out.html"),
	})

	err := cmd.RunE(cmd, []string{filepath.Base(dir)})
	if err == nil {
		t.Fatal("an unknown --kind must be refused on the saved-render path too, not silently ignored")
	}
	if !strings.Contains(err.Error(), "reivew") {
		t.Errorf("error must echo the bad kind: %v", err)
	}
}
