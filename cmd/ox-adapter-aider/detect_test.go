package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// TestDiagnose_HooksMissingFixInstallsHooks checks that the recommended shell
// command preserves the repository path and that its arguments repair the repo.
// Failure prevented: a path is split or interpreted as shell syntax, so the
// recommended fix targets the wrong directory or never repairs the warning.
func TestDiagnose_HooksMissingFixInstallsHooks(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "plain path", path: "repository"},
		{name: "spaces", path: "repository with spaces"},
		{name: "single quote", path: "repository's files"},
		{name: "shell metacharacters", path: "repository $HOME $(printf expanded) ; #"},
		{name: "combined", path: "repository's $HOME $(printf expanded) ; #"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoRoot := filepath.Join(t.TempDir(), tt.path)
			if err := os.Mkdir(repoRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repoRoot, "CONVENTIONS.md"), []byte("# House rules\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			issue := hooksMissingIssue(t, repoRoot)
			if issue == nil {
				t.Fatal("expected a hooks-missing issue for a CONVENTIONS.md without the ox marker")
			}
			args := []string{"install-hooks", "--repo-root", repoRoot, "--scope", "project"}
			t.Run("shell round trip", func(t *testing.T) {
				if runtime.GOOS == "windows" {
					t.Skip("the recommended command uses POSIX shell quoting; Windows has no required POSIX shell")
				}
				command := "set -- " + issue.Fix + `; printf '%s\000' "$@"`
				output, err := exec.Command("sh", "-c", command).CombinedOutput()
				if err != nil {
					t.Fatalf("parse recommended fix %q: %v\noutput: %s", issue.Fix, err, output)
				}
				want := strings.Join(append([]string{"ox-adapter-aider"}, args...), "\x00") + "\x00"
				if string(output) != want {
					t.Fatalf("recommended fix changed shell arguments: got %q, want %q", output, want)
				}
			})

			var out bytes.Buffer
			if err := adapterruntime.RunWithArgs(newConfig(), args, nil, &out); err != nil {
				t.Fatalf("running fix arguments failed: %v\noutput: %s", err, out.String())
			}
			if after := hooksMissingIssue(t, repoRoot); after != nil {
				t.Errorf("hooks-missing persists after running its fix arguments")
			}
		})
	}
}

func strPtr(s string) *string { return &s }
