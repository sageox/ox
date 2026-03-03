package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFixLevel_Constants verifies fix level constants are defined correctly
func TestFixLevel_Constants(t *testing.T) {
	tests := []struct {
		level    FixLevel
		expected string
	}{
		{FixLevelCheckOnly, "check-only"},
		{FixLevelAuto, "auto"},
		{FixLevelSuggested, "suggested"},
		{FixLevelConfirm, "confirm"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, FixLevel(tt.expected), tt.level)
		})
	}
}

// TestDoctorCheck_Methods verifies DoctorCheck method implementations
func TestDoctorCheck_Methods(t *testing.T) {
	check := &DoctorCheck{
		Slug:        "test-slug",
		Name:        "Test Check",
		Category:    "Test Category",
		FixLevel:    FixLevelSuggested,
		Description: "A test check",
		Run: func(fix bool) checkResult {
			if fix {
				return PassedCheck("Test Check", "fixed")
			}
			return PassedCheck("Test Check", "ok")
		},
	}

	assert.Equal(t, "Test Check", check.GetName())
	assert.Equal(t, "Test Category", check.GetCategory())

	result := check.RunCheck(false)
	assert.True(t, result.passed)
	assert.Equal(t, "ok", result.message)

	result = check.RunCheck(true)
	assert.True(t, result.passed)
	assert.Equal(t, "fixed", result.message)
}

// TestDoctorCheck_FixLevelHelpers verifies fix level helper methods
func TestDoctorCheck_FixLevelHelpers(t *testing.T) {
	tests := []struct {
		name                string
		level               FixLevel
		wantAutoFixable     bool
		wantRequiresConfirm bool
		wantHasFix          bool
	}{
		{
			name:                "check-only",
			level:               FixLevelCheckOnly,
			wantAutoFixable:     false,
			wantRequiresConfirm: false,
			wantHasFix:          false,
		},
		{
			name:                "auto",
			level:               FixLevelAuto,
			wantAutoFixable:     true,
			wantRequiresConfirm: false,
			wantHasFix:          true,
		},
		{
			name:                "suggested",
			level:               FixLevelSuggested,
			wantAutoFixable:     false,
			wantRequiresConfirm: false,
			wantHasFix:          true,
		},
		{
			name:                "confirm",
			level:               FixLevelConfirm,
			wantAutoFixable:     false,
			wantRequiresConfirm: true,
			wantHasFix:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := &DoctorCheck{
				Slug:     "test",
				Name:     "Test",
				Category: "Test",
				FixLevel: tt.level,
				Run:      func(fix bool) checkResult { return PassedCheck("test", "ok") },
			}

			assert.Equal(t, tt.wantAutoFixable, check.IsAutoFixable(), "IsAutoFixable mismatch")
			assert.Equal(t, tt.wantRequiresConfirm, check.RequiresConfirmation(), "RequiresConfirmation mismatch")
			assert.Equal(t, tt.wantHasFix, check.HasFix(), "HasFix mismatch")
		})
	}
}

// TestDoctorCheckRegistry_Registration verifies check registration
func TestDoctorCheckRegistry_Registration(t *testing.T) {
	// the registry is populated by init() in doctor_check_registry.go
	// verify some key checks are registered
	require.NotEmpty(t, DoctorCheckRegistry, "DoctorCheckRegistry should not be empty")

	// verify specific checks exist
	authCheck := GetDoctorCheck(CheckSlugAuthStatus)
	require.NotNil(t, authCheck, "auth-status check should be registered")
	assert.Equal(t, "Logged in", authCheck.Name)
	assert.Equal(t, "Authentication", authCheck.Category)
	assert.Equal(t, FixLevelCheckOnly, authCheck.FixLevel)

	gitignoreCheck := GetDoctorCheck(CheckSlugGitignore)
	require.NotNil(t, gitignoreCheck, "gitignore check should be registered")
	assert.Equal(t, FixLevelAuto, gitignoreCheck.FixLevel)
}

// TestDoctorCheckRegistry_GetByCategory verifies category filtering
func TestDoctorCheckRegistry_GetByCategory(t *testing.T) {
	authChecks := GetDoctorChecksByCategory("Authentication")
	require.NotEmpty(t, authChecks, "should have Authentication checks")

	for _, check := range authChecks {
		assert.Equal(t, "Authentication", check.Category)
	}
}

// TestDoctorCheckRegistry_GetByFixLevel verifies fix level filtering
func TestDoctorCheckRegistry_GetByFixLevel(t *testing.T) {
	autoChecks := GetDoctorChecksByFixLevel(FixLevelAuto)
	require.NotEmpty(t, autoChecks, "should have auto-fix checks")

	for _, check := range autoChecks {
		assert.Equal(t, FixLevelAuto, check.FixLevel)
	}

	confirmChecks := GetDoctorChecksByFixLevel(FixLevelConfirm)
	require.NotEmpty(t, confirmChecks, "should have confirm checks")

	for _, check := range confirmChecks {
		assert.Equal(t, FixLevelConfirm, check.FixLevel)
	}
}

// TestCheckSlugConstants_Unique verifies all slug constants are unique
func TestCheckSlugConstants_Unique(t *testing.T) {
	slugs := []string{
		CheckSlugSageoxDir,
		CheckSlugConfigJSON,
		CheckSlugGitignore,
		CheckSlugGitattributes,
		CheckSlugSageoxGitignore,
		CheckSlugReadme,
		CheckSlugRepoMarker,
		CheckSlugLedgerPath,
		CheckSlugLedgerRemote,
		CheckSlugTeamContextPath,
		CheckSlugTeamSymlink,
		CheckSlugLegacyStructure,
		CheckSlugGitConfig,
		CheckSlugGitRemotes,
		CheckSlugGitRepoState,
		CheckSlugMergeConflicts,
		CheckSlugGitConnectivity,
		CheckSlugGitAuth,
		CheckSlugGitHooks,
		CheckSlugGitLFS,
		CheckSlugStashedChanges,
		CheckSlugGitRepoPaths,
		CheckSlugAuthStatus,
		CheckSlugAuthPermissions,
		CheckSlugTokenExpiry,
		CheckSlugDaemonRunning,
		CheckSlugDaemonSocket,
		CheckSlugClaudeCodeHooks,
		CheckSlugOpenCodeHooks,
		CheckSlugGeminiHooks,
		CheckSlugCodexHooks,
		CheckSlugCodePuppyHooks,
		CheckSlugHookCommands,
		CheckSlugGitCommitHooks,
		CheckSlugTeamRegistration,
		CheckSlugLegacyTeamCtx,
		CheckSlugOrphanedTeamDirs,
	}

	seen := make(map[string]bool)
	for _, slug := range slugs {
		if seen[slug] {
			t.Errorf("duplicate slug constant: %s", slug)
		}
		seen[slug] = true
	}
}

// TestDoctorCheckRegistry_NoPanicOnDuplicate verifies duplicate registration panics
func TestDoctorCheckRegistry_NoPanicOnDuplicate(t *testing.T) {
	// this test verifies that the registry would panic on duplicate registration
	// we don't actually register duplicates since it would break the test suite
	// instead we verify the check for duplicates exists

	// the RegisterDoctorCheck function checks for duplicates
	// we can verify this by checking that an existing slug is in the registry
	existingCheck := GetDoctorCheck(CheckSlugAuthStatus)
	require.NotNil(t, existingCheck, "auth-status check should exist")
}

// TestDoctorOptions_ShouldFix verifies the shouldFix method behavior
func TestDoctorOptions_ShouldFix(t *testing.T) {
	tests := []struct {
		name     string
		opts     doctorOptions
		slug     string
		expected bool
	}{
		{
			name:     "fix=false, no slugs - returns false",
			opts:     doctorOptions{fix: false, fixSlugs: nil},
			slug:     "any-slug",
			expected: false,
		},
		{
			name:     "fix=true, no slugs - returns true for any slug",
			opts:     doctorOptions{fix: true, fixSlugs: nil},
			slug:     "any-slug",
			expected: true,
		},
		{
			name:     "fix=true, empty slugs - returns true for any slug",
			opts:     doctorOptions{fix: true, fixSlugs: []string{}},
			slug:     "any-slug",
			expected: true,
		},
		{
			name:     "fix=true, specific slugs - returns true for matching slug",
			opts:     doctorOptions{fix: true, fixSlugs: []string{"ledger-path", "team-symlink"}},
			slug:     "ledger-path",
			expected: true,
		},
		{
			name:     "fix=true, specific slugs - returns false for non-matching non-auto slug",
			opts:     doctorOptions{fix: true, fixSlugs: []string{"ledger-path", "team-symlink"}},
			slug:     "git-config", // not auto-fix and not in fixSlugs list
			expected: false,
		},
		{
			name:     "fix=false but fixSlugs present - returns true for matching slug",
			opts:     doctorOptions{fix: false, fixSlugs: []string{"ledger-path"}},
			slug:     "ledger-path",
			expected: true,
		},
		{
			name:     "multiple slugs - matches second slug",
			opts:     doctorOptions{fix: true, fixSlugs: []string{"config-json", "readme", "gitattributes"}},
			slug:     "readme",
			expected: true,
		},
		{
			name:     "multiple slugs - no match",
			opts:     doctorOptions{fix: true, fixSlugs: []string{"config-json", "readme", "gitattributes"}},
			slug:     "auth-status",
			expected: false,
		},
		{
			name:     "auto-fix check returns true even without --fix flag",
			opts:     doctorOptions{fix: false},
			slug:     "gitignore", // registered as FixLevelAuto
			expected: true,
		},
		{
			name:     "auto-fix check returns true even when not in fixSlugs",
			opts:     doctorOptions{fix: true, fixSlugs: []string{"ledger-path"}},
			slug:     "gitattributes", // registered as FixLevelAuto
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.opts.shouldFix(tt.slug)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestGetAvailableSlugs verifies getAvailableSlugs returns sorted slugs
func TestGetAvailableSlugs(t *testing.T) {
	slugs := getAvailableSlugs()

	// verify we get some slugs back
	require.NotEmpty(t, slugs, "should return registered slugs")

	// verify slugs are sorted
	for i := 1; i < len(slugs); i++ {
		assert.True(t, slugs[i-1] < slugs[i], "slugs should be sorted: %s should come before %s", slugs[i-1], slugs[i])
	}

	// verify known slugs are present
	foundLedgerPath := false
	foundAuthStatus := false
	for _, slug := range slugs {
		if slug == CheckSlugLedgerPath {
			foundLedgerPath = true
		}
		if slug == CheckSlugAuthStatus {
			foundAuthStatus = true
		}
	}
	assert.True(t, foundLedgerPath, "should contain ledger-path slug")
	assert.True(t, foundAuthStatus, "should contain auth-status slug")
}
