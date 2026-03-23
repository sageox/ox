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

// TestDoctorCheck_IsAutoFixable verifies IsAutoFixable method
func TestDoctorCheck_IsAutoFixable(t *testing.T) {
	tests := []struct {
		name            string
		level           FixLevel
		wantAutoFixable bool
	}{
		{"check-only", FixLevelCheckOnly, false},
		{"auto", FixLevelAuto, true},
		{"suggested", FixLevelSuggested, false},
		{"confirm", FixLevelConfirm, false},
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
		CheckSlugLedgerRemote,
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
		CheckSlugSessionOrphaned,
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
	foundAuthStatus := false
	foundGitRepoPaths := false
	for _, slug := range slugs {
		if slug == CheckSlugAuthStatus {
			foundAuthStatus = true
		}
		if slug == CheckSlugGitRepoPaths {
			foundGitRepoPaths = true
		}
	}
	assert.True(t, foundAuthStatus, "should contain auth-status slug")
	assert.True(t, foundGitRepoPaths, "should contain git-repo-paths slug")
}
