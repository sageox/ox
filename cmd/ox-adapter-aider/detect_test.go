package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

// hooksMissingIssue returns the aider:hooks-missing issue from a diagnosis, or
// nil. Other issues (aider not on PATH) depend on the machine and are ignored.
func hooksMissingIssue(t *testing.T, repoRoot string) *adapterprotocol.DiagnoseIssue {
	t.Helper()
	result, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: repoRoot})
	if err != nil {
		t.Fatalf("handleDiagnose: %v", err)
	}
	for i := range result.Issues {
		if result.Issues[i].Slug == "aider:hooks-missing" {
			return &result.Issues[i]
		}
	}
	return nil
}

// TestDiagnose_HooksMissing covers `ox doctor` checking a repo whose
// CONVENTIONS.md exists but never got the ox prime block.
// Failure prevented: Aider sessions start without loading team context and
// doctor stays silent, or doctor nags about repos that are set up fine (current
// or pre-#527 legacy marker) or that have no CONVENTIONS.md to repair.
func TestDiagnose_HooksMissing(t *testing.T) {
	tests := []struct {
		name        string
		conventions *string // nil means no CONVENTIONS.md
		wantIssue   bool
	}{
		{name: "no CONVENTIONS.md is not an ox problem", conventions: nil, wantIssue: false},
		{name: "CONVENTIONS.md without the ox marker", conventions: strPtr("# House rules\n\nUse tabs.\n"), wantIssue: true},
		{name: "empty CONVENTIONS.md", conventions: strPtr(""), wantIssue: true},
		{name: "current marker is installed", conventions: strPtr("# Rules\n\n" + aiderPrimeBlock + "\n"), wantIssue: false},
		{name: "legacy marker still counts as installed", conventions: strPtr(aiderLegacyPrimeMarkerStart + "\nox agent prime\n" + aiderLegacyPrimeMarkerEnd + "\n"), wantIssue: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoRoot := t.TempDir()
			if tt.conventions != nil {
				if err := os.WriteFile(filepath.Join(repoRoot, "CONVENTIONS.md"), []byte(*tt.conventions), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			issue := hooksMissingIssue(t, repoRoot)
			if (issue != nil) != tt.wantIssue {
				t.Fatalf("hooks-missing issue present = %v, want %v (issue: %+v)", issue != nil, tt.wantIssue, issue)
			}
			if issue != nil && (issue.Severity != "warning" || !issue.FixSafe) {
				t.Errorf("hooks-missing must be a warning with a safe fix, got severity=%q fixSafe=%v", issue.Severity, issue.FixSafe)
			}
		})
	}
}

// TestDiagnose_HooksMissingFixInstallsHooks runs the exact command the issue
// recommends and checks it repairs the repo.
// Failure prevented: doctor tells a coworker to run an adapter command with
// flags the adapter rejects, so the "safe fix" never fixes anything and the
// warning returns on every diagnosis.
func TestDiagnose_HooksMissingFixInstallsHooks(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "CONVENTIONS.md"), []byte("# House rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	issue := hooksMissingIssue(t, repoRoot)
	if issue == nil {
		t.Fatal("expected a hooks-missing issue for a CONVENTIONS.md without the ox marker")
	}

	fields := strings.Fields(issue.Fix)
	if len(fields) < 2 || fields[0] != "ox-adapter-aider" {
		t.Fatalf("fix %q must invoke ox-adapter-aider", issue.Fix)
	}
	var out bytes.Buffer
	if err := adapterruntime.RunWithArgs(newConfig(), fields[1:], nil, &out); err != nil {
		t.Fatalf("running the recommended fix %q failed: %v\noutput: %s", issue.Fix, err, out.String())
	}

	if after := hooksMissingIssue(t, repoRoot); after != nil {
		t.Errorf("hooks-missing persists after running its own fix %q", issue.Fix)
	}
}

func strPtr(s string) *string { return &s }
