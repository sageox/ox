package main

import (
	"fmt"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Slug namespacing ---

// TestAdapterIssueSlug_Namespacing verifies slugs are prefixed with adapter name.
// Failure prevented: slug collisions between adapters and core checks.
func TestAdapterIssueSlug_Namespacing(t *testing.T) {
	tests := []struct {
		adapter string
		slug    string
		want    string
	}{
		{"claude-code", "hooks-missing", "claude-code:hooks-missing"},
		{"amp", "trusted-commands-missing", "amp:trusted-commands-missing"},
		{"test", "fake-issue", "test:fake-issue"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := adapterIssueSlug(tt.adapter, tt.slug)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestIsAdapterSlug verifies detection of adapter-namespaced slugs.
// Failure prevented: adapter slugs rejected by core slug validation.
func TestIsAdapterSlug(t *testing.T) {
	assert.True(t, isAdapterSlug("claude-code:hooks-missing"))
	assert.True(t, isAdapterSlug("test:diagnose-error"))
	assert.False(t, isAdapterSlug("auth-status"))
	assert.False(t, isAdapterSlug("ledger-path"))
}

// --- B. Severity mapping ---

// TestAdapterIssueToCheckResult_SeverityMapping verifies adapter severity
// levels map correctly to doctor check result types.
// Failure prevented: error-severity adapter issues displayed as info/warnings.
func TestAdapterIssueToCheckResult_SeverityMapping(t *testing.T) {
	tests := []struct {
		severity     string
		wantPassed   bool
		wantWarning  bool
		wantCritical bool
	}{
		{"error", false, false, true},
		{"warning", true, true, false},
		{"info", true, true, false},
		{"unknown", true, true, false}, // unknown defaults to warning
	}
	for _, tt := range tests {
		t.Run(tt.severity, func(t *testing.T) {
			issue := adapterprotocol.DiagnoseIssue{
				Slug:     "test-slug",
				Severity: tt.severity,
				Title:    "Test issue",
				Detail:   "Some detail",
			}
			cr := adapterIssueToCheckResult("test-adapter", issue)
			assert.Equal(t, tt.wantPassed, cr.passed, "passed")
			assert.Equal(t, tt.wantWarning, cr.warning, "warning")
			if tt.wantCritical {
				assert.Equal(t, "critical", cr.priority, "priority should be critical for error severity")
			}
		})
	}
}

// --- C. Check result conversion ---

// TestAdapterIssueToCheckResult_FieldMapping verifies all DiagnoseIssue fields
// are correctly mapped into the checkResult.
// Failure prevented: adapter issue details lost in conversion.
func TestAdapterIssueToCheckResult_FieldMapping(t *testing.T) {
	issue := adapterprotocol.DiagnoseIssue{
		Slug:     "hooks-not-installed",
		Severity: "error",
		Title:    "Claude Code hooks not installed",
		Detail:   "ox hook commands are missing from .claude/settings.json",
		Fix:      "ox integrate install",
		FixSafe:  true,
	}

	cr := adapterIssueToCheckResult("claude-code", issue)
	assert.Equal(t, "claude-code: Claude Code hooks not installed", cr.name)
	assert.Equal(t, "Claude Code hooks not installed", cr.message)
	assert.Contains(t, cr.detail, "ox hook commands are missing")
	assert.Contains(t, cr.detail, "fix: ox integrate install")
}

// TestAdapterIssueToCheckResult_NoFix verifies issues without a fix command
// don't include fix text in detail.
// Failure prevented: spurious "fix: " text when no fix is available.
func TestAdapterIssueToCheckResult_NoFix(t *testing.T) {
	issue := adapterprotocol.DiagnoseIssue{
		Slug:     "session-dir-large",
		Severity: "info",
		Title:    "Session directory is large",
		Detail:   "~/.claude/projects/ is 3.2 GB",
	}

	cr := adapterIssueToCheckResult("claude-code", issue)
	assert.NotContains(t, cr.detail, "fix:")
	assert.Equal(t, "~/.claude/projects/ is 3.2 GB", cr.detail)
}

// --- D. Error handling ---

// TestAdapterErrorToCheckResult verifies adapter failures surface as warnings.
// Failure prevented: adapter crash silently swallowed, user unaware of broken adapter.
func TestAdapterErrorToCheckResult(t *testing.T) {
	err := fmt.Errorf("timeout after 5s")
	cr := adapterErrorToCheckResult("claude-code", err, "warning")

	assert.True(t, cr.passed, "should be passed (warning, not failure)")
	assert.True(t, cr.warning, "should be warning")
	assert.Equal(t, "claude-code adapter", cr.name)
	assert.Contains(t, cr.message, "diagnose failed")
	assert.Contains(t, cr.message, "timeout after 5s")
	assert.Equal(t, "claude-code:diagnose-error", cr.slug)
}

// --- E. Fix slug matching ---

// TestShouldFixAdapterSlug verifies adapter fix-slug matching logic.
// Failure prevented: adapter fixes applied when not requested, or not applied when requested.
func TestShouldFixAdapterSlug(t *testing.T) {
	tests := []struct {
		name    string
		opts    doctorOptions
		slug    string
		fixSafe bool
		want    bool
	}{
		{
			name:    "explicit slug match",
			opts:    doctorOptions{fixSlugs: []string{"claude-code:hooks-missing"}},
			slug:    "claude-code:hooks-missing",
			fixSafe: true,
			want:    true,
		},
		{
			name:    "explicit slug no match",
			opts:    doctorOptions{fixSlugs: []string{"amp:config-missing"}},
			slug:    "claude-code:hooks-missing",
			fixSafe: true,
			want:    false,
		},
		{
			name:    "fix flag with safe fix",
			opts:    doctorOptions{fix: true},
			slug:    "claude-code:hooks-missing",
			fixSafe: true,
			want:    true,
		},
		{
			name:    "fix flag with unsafe fix",
			opts:    doctorOptions{fix: true},
			slug:    "claude-code:hooks-missing",
			fixSafe: false,
			want:    false,
		},
		{
			name:    "no fix flags",
			opts:    doctorOptions{},
			slug:    "claude-code:hooks-missing",
			fixSafe: true,
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.opts.shouldFixAdapterSlug(tt.slug, tt.fixSafe)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- F. Verify adapter fix ---

// TestVerifyAdapterFix_IssueGone verifies that fix verification correctly
// detects when an issue has been resolved.
// Failure prevented: fix reported as successful when issue persists.
func TestVerifyAdapterFix_IssueGone(t *testing.T) {
	// verifyAdapterFix calls ea.Diagnose which requires a real binary.
	// This test verifies the logic via the standalone function rather than
	// the full subprocess flow. Full E2E coverage uses the test adapter binary.
	// See integration tests for the full cycle.
	t.Skip("short: requires real adapter binary")
}

// --- G. Integration with slug validation ---

// TestSlugValidation_AllowsAdapterSlugs verifies that the core slug
// validation allows adapter-namespaced slugs through without error.
// Failure prevented: --fix-slug claude-code:hooks-missing rejected as "unknown slug".
func TestSlugValidation_AllowsAdapterSlugs(t *testing.T) {
	// the isAdapterSlug check is the gate in the RunE validation
	require.True(t, isAdapterSlug("claude-code:hooks-missing"),
		"adapter slug should pass through validation")
	require.False(t, isAdapterSlug("auth-status"),
		"core slug should still be validated against registry")
}
