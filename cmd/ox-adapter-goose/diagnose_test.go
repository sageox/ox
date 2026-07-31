package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

func issueBySlug(issues []adapterprotocol.DiagnoseIssue, slug string) *adapterprotocol.DiagnoseIssue {
	for i := range issues {
		if issues[i].Slug == slug {
			return &issues[i]
		}
	}
	return nil
}

// TestDiagnose_ReportsMissingHooks — the ordinary "not set up yet" state, which
// must come with a runnable fix rather than just a complaint.
func TestDiagnose_ReportsMissingHooks(t *testing.T) {
	root := t.TempDir()

	res, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: root})
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}

	issue := issueBySlug(res.Issues, "hooks-missing")
	if issue == nil {
		t.Fatalf("expected a hooks-missing issue, got %+v", res.Issues)
	}
	if issue.Fix == "" {
		t.Error("hooks-missing must carry a Fix command — doctor is the last line of defense")
	}
	if !issue.FixSafe {
		t.Error("installing hooks into a fresh repo is a safe auto-fix")
	}
}

// TestDiagnose_DetectsOrphanedHooksJSON is the failure mode unique to the
// plugin-directory model: hooks.json present, plugin.json absent. Goose ignores
// a plugin directory with no manifest, so the hooks NEVER FIRE — but from the
// filesystem alone this looks identical to a working install. Without this
// check the user sees a hooks file and assumes recording works.
func TestDiagnose_DetectsOrphanedHooksJSON(t *testing.T) {
	root := t.TempDir()
	if _, err := handleInstallHooks(projectParams(root)); err != nil {
		t.Fatalf("install: %v", err)
	}

	manifestPath, err := manifestFilePath(root, "project")
	if err != nil {
		t.Fatalf("manifestFilePath: %v", err)
	}
	if err := os.Remove(manifestPath); err != nil {
		t.Fatalf("remove manifest: %v", err)
	}

	res, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: root})
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}

	issue := issueBySlug(res.Issues, "manifest-missing")
	if issue == nil {
		t.Fatalf("orphaned hooks.json went undetected — hooks would silently never fire. Issues: %+v", res.Issues)
	}
	if issue.Severity != "warning" {
		t.Errorf("severity = %q, want warning — this is a fully broken install", issue.Severity)
	}
	if issue.Fix == "" {
		t.Error("manifest-missing must carry a Fix command")
	}

	// And the missing-hooks issue must NOT also fire — hooks.json does exist.
	if issueBySlug(res.Issues, "hooks-missing") != nil {
		t.Error("hooks-missing should not fire when hooks.json is present")
	}
}

// TestDiagnose_CleanInstallHasNoPluginIssues — after a correct install, neither
// plugin-directory issue should fire. A check that always complains is noise.
func TestDiagnose_CleanInstallHasNoPluginIssues(t *testing.T) {
	root := t.TempDir()
	if _, err := handleInstallHooks(projectParams(root)); err != nil {
		t.Fatalf("install: %v", err)
	}

	res, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: root})
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}

	for _, slug := range []string{"hooks-missing", "manifest-missing"} {
		if issueBySlug(res.Issues, slug) != nil {
			t.Errorf("%s fired on a clean install", slug)
		}
	}
}

// TestDiagnose_NoRepoRootSkipsPluginChecks — diagnose is called outside a repo
// (e.g. `ox doctor` in a bare directory) and must not invent findings.
func TestDiagnose_NoRepoRootSkipsPluginChecks(t *testing.T) {
	res, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: ""})
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}

	for _, slug := range []string{"hooks-missing", "manifest-missing"} {
		if issueBySlug(res.Issues, slug) != nil {
			t.Errorf("%s fired with no repo root", slug)
		}
	}
}

// TestDiagnose_FixCommandIsRunnable pins the shape of the remediation string so
// a rename of the subcommand or its flags cannot silently produce a Fix that
// does nothing when an operator pastes it.
func TestDiagnose_FixCommandIsRunnable(t *testing.T) {
	root := t.TempDir()

	res, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: root})
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}

	issue := issueBySlug(res.Issues, "hooks-missing")
	if issue == nil {
		t.Fatal("expected hooks-missing")
	}

	want := "ox-adapter-goose install-hooks --repo-root " + root + " --scope project"
	if issue.Fix != want {
		t.Errorf("Fix = %q, want %q", issue.Fix, want)
	}
}

// TestDiagnose_DetailNamesTheActualPath — an operator needs to know which file
// to look at, and the plugin path is not one they would guess.
func TestDiagnose_DetailNamesTheActualPath(t *testing.T) {
	root := t.TempDir()

	res, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: root})
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}

	issue := issueBySlug(res.Issues, "hooks-missing")
	if issue == nil {
		t.Fatal("expected hooks-missing")
	}

	want := filepath.Join(root, ".agents", "plugins", "sageox", "hooks", "hooks.json")
	if !strings.Contains(issue.Detail, want) {
		t.Errorf("Detail = %q, want it to name %q", issue.Detail, want)
	}
}
