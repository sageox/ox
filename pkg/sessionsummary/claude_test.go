package sessionsummary

import (
	"os"
	"testing"
)

// TestDefaultSummaryModel_NoEnv verifies the floating Haiku 4.5 alias is
// returned when OX_SUMMARY_MODEL is unset. Failure prevented: silent
// regression to user's local default (Sonnet/Opus) on a fresh install,
// erasing the cost win this code exists to deliver.
func TestDefaultSummaryModel_NoEnv(t *testing.T) {
	// Setenv registers cleanup; Unsetenv puts us in the "unset" state for
	// the body of the test. Cleanup restores prior value either way.
	t.Setenv("OX_SUMMARY_MODEL", "sentinel")
	if err := os.Unsetenv("OX_SUMMARY_MODEL"); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	if got := DefaultSummaryModel(); got != "claude-haiku-4-5" {
		t.Errorf("DefaultSummaryModel() = %q, want claude-haiku-4-5", got)
	}
}

// TestDefaultSummaryModel_Override verifies an explicit model override
// is honored. Failure prevented: env-var rollback path silently broken,
// trapping users on Haiku if quality issues emerge in production.
func TestDefaultSummaryModel_Override(t *testing.T) {
	t.Setenv("OX_SUMMARY_MODEL", "claude-sonnet-4-6")
	if got := DefaultSummaryModel(); got != "claude-sonnet-4-6" {
		t.Errorf("DefaultSummaryModel() = %q, want claude-sonnet-4-6", got)
	}
}

// TestDefaultSummaryModel_EmptyOverride verifies that OX_SUMMARY_MODEL=
// (set to empty) is distinct from unset — it returns "" so the summary run
// omits --model and defers to the user's local default. Failure
// prevented: rollback to pre-Haiku behavior unavailable, blocking ops
// from fast-rolling back if Haiku quality regresses.
func TestDefaultSummaryModel_EmptyOverride(t *testing.T) {
	t.Setenv("OX_SUMMARY_MODEL", "")
	if got := DefaultSummaryModel(); got != "" {
		t.Errorf("DefaultSummaryModel() = %q with empty override, want empty string", got)
	}
}
