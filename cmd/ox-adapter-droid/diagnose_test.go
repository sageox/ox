package main

import (
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHandleDiagnose_HooksMissing_OffersSafeFixViaOx verifies the
// droid:hooks-missing issue is repairable via the "ox" dispatch path, not the
// external adapter binary — ox-adapter-droid as argv[0] is rejected by
// adapterFixArgvAllowlist in cmd/ox/doctor_adapters.go, so a FixArgv naming it
// would silently downgrade to display-only under `ox doctor --fix`.
// Failure prevented: FixSafe=true with an argv[0] the auto-fix path refuses,
// making the "safe, automatic" repair never actually run.
func TestHandleDiagnose_HooksMissing_OffersSafeFixViaOx(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // never read the real ~/.factory in a test
	repoRoot := t.TempDir()

	res, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: repoRoot})
	require.NoError(t, err)

	issue := issueBySlugDroid(res.Issues, "droid:hooks-missing")
	if issue == nil {
		t.Fatalf("expected droid:hooks-missing, got %+v", res.Issues)
	}
	assert.True(t, issue.FixSafe)
	assert.Equal(t, []string{"ox", "integrate", "install", "--droid"}, issue.FixArgv,
		"argv[0] must be \"ox\" — ox-adapter-droid is not in the auto-fix allowlist")
}

func issueBySlugDroid(issues []adapterprotocol.DiagnoseIssue, slug string) *adapterprotocol.DiagnoseIssue {
	for i := range issues {
		if issues[i].Slug == slug {
			return &issues[i]
		}
	}
	return nil
}
