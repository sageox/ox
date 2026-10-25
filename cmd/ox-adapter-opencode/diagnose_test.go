package main

import (
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// TestHandleDiagnose_HooksMissing_OffersSafeFixViaOx verifies the
// hooks-missing issue is repairable via the "ox" dispatch path, not the
// external adapter binary — ox-adapter-opencode as argv[0] is rejected by
// adapterFixArgvAllowlist in cmd/ox/doctor_adapters.go, so a FixArgv naming it
// would silently downgrade to display-only under `ox doctor --fix`.
// Failure prevented: FixSafe=true with an argv[0] the auto-fix path refuses,
// making the "safe, automatic" repair never actually run.
func TestHandleDiagnose_HooksMissing_OffersSafeFixViaOx(t *testing.T) {
	repoRoot := t.TempDir() // no .opencode/plugin/ox-prime.ts present

	res, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: repoRoot})
	if err != nil {
		t.Fatalf("handleDiagnose: %v", err)
	}

	var issue *adapterprotocol.DiagnoseIssue
	for i := range res.Issues {
		if res.Issues[i].Slug == "hooks-missing" {
			issue = &res.Issues[i]
			break
		}
	}
	if issue == nil {
		t.Fatalf("expected hooks-missing, got %+v", res.Issues)
	}

	if !issue.FixSafe {
		t.Error("FixSafe = false, want true")
	}
	want := []string{"ox", "integrate", "install", "--opencode"}
	if len(issue.FixArgv) != len(want) {
		t.Fatalf("FixArgv = %v, want %v", issue.FixArgv, want)
	}
	for i := range want {
		if issue.FixArgv[i] != want[i] {
			t.Fatalf("FixArgv = %v, want %v (argv[0] must be \"ox\" — "+
				"ox-adapter-opencode is not in the auto-fix allowlist)", issue.FixArgv, want)
		}
	}
}
