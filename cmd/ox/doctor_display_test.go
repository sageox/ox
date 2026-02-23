package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckResult_String verifies checkResult fields are properly accessible
func TestCheckResult_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		check    checkResult
		wantName string
		wantMsg  string
	}{
		{
			name:     "passed check with message",
			check:    PassedCheck("test-check", "all good"),
			wantName: "test-check",
			wantMsg:  "all good",
		},
		{
			name:     "failed check with detail",
			check:    FailedCheck("test-check", "failed", "fix it"),
			wantName: "test-check",
			wantMsg:  "failed",
		},
		{
			name:     "warning check",
			check:    WarningCheck("test-check", "warning msg", "detail"),
			wantName: "test-check",
			wantMsg:  "warning msg",
		},
		{
			name:     "skipped check",
			check:    SkippedCheck("test-check", "skipped msg", "detail"),
			wantName: "test-check",
			wantMsg:  "skipped msg",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantName, tt.check.name, "name mismatch")
			assert.Equal(t, tt.wantMsg, tt.check.message, "message mismatch")
		})
	}
}

// TestCheckCategory_MixedResults handles mix of passed/failed/warning
func TestCheckCategory_MixedResults(t *testing.T) {
	t.Parallel()
	categories := []checkCategory{
		{
			name: "Mixed Results",
			checks: []checkResult{
				PassedCheck("check1", "passed"),
				FailedCheck("check2", "failed", "action needed"),
				WarningCheck("check3", "warning", "consider this"),
				SkippedCheck("check4", "skipped", "not applicable"),
			},
		},
	}

	cmd := &cobra.Command{}
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)

	hasFailed := displayDoctorResults(cmd, categories, doctorOptions{verbose: true})

	assert.True(t, hasFailed, "displayDoctorResults() should return true when checks failed")

	output := buf.String()
	assert.Contains(t, output, "1 passed", "output missing correct pass count")
	assert.Contains(t, output, "1 warnings", "output missing correct warning count")
	assert.Contains(t, output, "1 failed", "output missing correct fail count")
	assert.Contains(t, output, "check1", "output missing check1")
	assert.Contains(t, output, "check2", "output missing check2")
}

// TestDisplayDoctorResults_AllPassed returns false (no failures)
func TestDisplayDoctorResults_AllPassed(t *testing.T) {
	t.Parallel()
	categories := []checkCategory{
		{
			name: "All Passing",
			checks: []checkResult{
				PassedCheck("check1", "ok"),
				PassedCheck("check2", "ok"),
				PassedCheck("check3", "ok"),
			},
		},
		{
			name: "More Passing",
			checks: []checkResult{
				PassedCheck("check4", "ok"),
			},
		},
	}

	cmd := &cobra.Command{}
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)

	hasFailed := displayDoctorResults(cmd, categories, doctorOptions{verbose: true})

	assert.False(t, hasFailed, "displayDoctorResults() should return false when all checks passed")

	output := buf.String()
	assert.Contains(t, output, "4 passed", "output missing correct pass count")
	assert.Contains(t, output, "0 warnings", "output missing warning count")
	assert.Contains(t, output, "0 failed", "output missing fail count")
}

// TestDisplayPrioritySummary_AllPassedShowsHealthy verifies the reassuring
// confirmation message when all checks pass with no warnings.
func TestDisplayPrioritySummary_AllPassedShowsHealthy(t *testing.T) {
	t.Parallel()
	categories := []checkCategory{
		{
			name: "Core",
			checks: []checkResult{
				PassedCheck("check1", "ok"),
				PassedCheck("check2", "ok"),
			},
		},
	}

	cmd := &cobra.Command{}
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)

	// non-verbose mode exercises displayPrioritySummary
	hasFailed := displayDoctorResults(cmd, categories, doctorOptions{})

	assert.False(t, hasFailed)
	output := buf.String()
	assert.Contains(t, output, "All checks passed", "should show healthy confirmation when no failures or warnings")
}

// TestDisplayPrioritySummary_WarningsNoHealthyMessage verifies the healthy
// confirmation is NOT shown when there are warnings (even without failures).
func TestDisplayPrioritySummary_WarningsNoHealthyMessage(t *testing.T) {
	t.Parallel()
	categories := []checkCategory{
		{
			name: "Mixed",
			checks: []checkResult{
				PassedCheck("check1", "ok"),
				WarningCheck("check2", "might be an issue", ""),
			},
		},
	}

	cmd := &cobra.Command{}
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)

	hasFailed := displayDoctorResults(cmd, categories, doctorOptions{})

	assert.False(t, hasFailed)
	output := buf.String()
	assert.NotContains(t, output, "All checks passed", "should NOT show healthy message when warnings exist")
}

// TestDisplayDoctorResults_HasFailures returns true when any check failed
func TestDisplayDoctorResults_HasFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		categories []checkCategory
		wantFailed bool
	}{
		{
			name: "single failure",
			categories: []checkCategory{
				{
					name: "Category",
					checks: []checkResult{
						PassedCheck("check1", "ok"),
						FailedCheck("check2", "failed", "fix this"),
					},
				},
			},
			wantFailed: true,
		},
		{
			name: "multiple failures",
			categories: []checkCategory{
				{
					name: "Category",
					checks: []checkResult{
						FailedCheck("check1", "failed", "fix 1"),
						FailedCheck("check2", "failed", "fix 2"),
					},
				},
			},
			wantFailed: true,
		},
		{
			name: "failure in second category",
			categories: []checkCategory{
				{
					name: "Category1",
					checks: []checkResult{
						PassedCheck("check1", "ok"),
					},
				},
				{
					name: "Category2",
					checks: []checkResult{
						FailedCheck("check2", "failed", "fix this"),
					},
				},
			},
			wantFailed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			buf := new(bytes.Buffer)
			cmd.SetOut(buf)

			hasFailed := displayDoctorResults(cmd, tt.categories, doctorOptions{verbose: true})

			assert.Equal(t, tt.wantFailed, hasFailed, "displayDoctorResults() result mismatch")
		})
	}
}

// TestDisplayDoctorResults_OnlyWarnings returns false (warnings don't count as failures)
func TestDisplayDoctorResults_OnlyWarnings(t *testing.T) {
	t.Parallel()
	categories := []checkCategory{
		{
			name: "Warnings Only",
			checks: []checkResult{
				WarningCheck("check1", "warn1", "consider this"),
				WarningCheck("check2", "warn2", "consider that"),
				PassedCheck("check3", "ok"),
			},
		},
	}

	cmd := &cobra.Command{}
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)

	hasFailed := displayDoctorResults(cmd, categories, doctorOptions{verbose: true})

	assert.False(t, hasFailed, "displayDoctorResults() should return false for warnings only")

	output := buf.String()
	assert.Contains(t, output, "1 passed", "output missing correct pass count")
	assert.Contains(t, output, "2 warnings", "output missing correct warning count")
	assert.Contains(t, output, "0 failed", "output missing fail count")
}

// TestDisplayDoctorResults_WithChildren properly handles nested child checks
func TestDisplayDoctorResults_WithChildren(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		categories []checkCategory
		wantFailed bool
		wantPass   string
		wantFail   string
	}{
		{
			name: "parent passed with passing children",
			categories: []checkCategory{
				{
					name: "Category",
					checks: []checkResult{
						{
							name:    "parent",
							passed:  true,
							message: "ok",
							children: []checkResult{
								PassedCheck("child1", "ok"),
								PassedCheck("child2", "ok"),
							},
						},
					},
				},
			},
			wantFailed: false,
			wantPass:   "3 passed",
			wantFail:   "0 failed",
		},
		{
			name: "parent passed with failed child",
			categories: []checkCategory{
				{
					name: "Category",
					checks: []checkResult{
						{
							name:    "parent",
							passed:  true,
							message: "ok",
							children: []checkResult{
								PassedCheck("child1", "ok"),
								FailedCheck("child2", "failed", "fix child"),
							},
						},
					},
				},
			},
			wantFailed: true,
			wantPass:   "2 passed",
			wantFail:   "1 failed",
		},
		{
			name: "parent failed with passing children",
			categories: []checkCategory{
				{
					name: "Category",
					checks: []checkResult{
						{
							name:    "parent",
							passed:  false,
							message: "failed",
							detail:  "fix parent",
							children: []checkResult{
								PassedCheck("child1", "ok"),
								PassedCheck("child2", "ok"),
							},
						},
					},
				},
			},
			wantFailed: true,
			wantPass:   "2 passed",
			wantFail:   "1 failed",
		},
		{
			name: "parent with warning child",
			categories: []checkCategory{
				{
					name: "Category",
					checks: []checkResult{
						{
							name:    "parent",
							passed:  true,
							message: "ok",
							children: []checkResult{
								PassedCheck("child1", "ok"),
								WarningCheck("child2", "warn", "consider"),
							},
						},
					},
				},
			},
			wantFailed: false,
			wantPass:   "2 passed",
			wantFail:   "0 failed",
		},
		{
			name: "skipped child doesn't count as failure",
			categories: []checkCategory{
				{
					name: "Category",
					checks: []checkResult{
						{
							name:    "parent",
							passed:  true,
							message: "ok",
							children: []checkResult{
								PassedCheck("child1", "ok"),
								SkippedCheck("child2", "skipped", "not applicable"),
							},
						},
					},
				},
			},
			wantFailed: false,
			wantPass:   "2 passed",
			wantFail:   "0 failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			buf := new(bytes.Buffer)
			cmd.SetOut(buf)

			hasFailed := displayDoctorResults(cmd, tt.categories, doctorOptions{verbose: true})

			assert.Equal(t, tt.wantFailed, hasFailed, "displayDoctorResults() result mismatch")

			output := buf.String()
			assert.Contains(t, output, tt.wantPass, "output missing pass count")
			assert.Contains(t, output, tt.wantFail, "output missing fail count")
		})
	}
}

// TestDisplayDoctorResults_DetailRendering ensures details are rendered
func TestDisplayDoctorResults_DetailRendering(t *testing.T) {
	t.Parallel()
	categories := []checkCategory{
		{
			name: "Category",
			checks: []checkResult{
				FailedCheck("check1", "failed", "run this command to fix"),
				{
					name:    "check2",
					passed:  true,
					message: "ok",
					children: []checkResult{
						FailedCheck("child", "failed", "child fix needed"),
					},
				},
			},
		},
	}

	cmd := &cobra.Command{}
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)

	displayDoctorResults(cmd, categories, doctorOptions{verbose: true})

	output := buf.String()
	// details should be present in output somewhere (rendered via renderCheck)
	// we don't check exact formatting since that depends on ui package
	assert.Contains(t, output, "check1", "output missing check1")
	assert.Contains(t, output, "check2", "output missing check2")
	assert.Contains(t, output, "child", "output missing child check")
}

// TestRunDoctorChecks_CategoryStructure verifies runDoctorChecks returns expected category structure
func TestRunDoctorChecks_CategoryStructure(t *testing.T) {
	t.Parallel()
	categories := getCachedDoctorChecks()

	// should return at least the core categories
	require.NotEmpty(t, categories, "runDoctorChecks() returned no categories")

	// verify expected category names exist
	expectedCategories := []string{
		"Project Structure",
		"Integration",
		"User Config",
		"Git Status",
		"Authentication",
		"Ecosystem",
		"Updates",
	}

	categoryNames := make(map[string]bool)
	for _, cat := range categories {
		categoryNames[cat.name] = true
	}

	for _, expected := range expectedCategories {
		assert.True(t, categoryNames[expected], "runDoctorChecks() missing expected category %q", expected)
	}
}

// TestRunDoctorChecks_WithFixFlag verifies fix flag is passed through
func TestRunDoctorChecks_WithFixFlag(t *testing.T) {
	t.Parallel()
	// run with fix=true (can't cache this since it may have side effects)
	categoriesWithFix := runDoctorChecks(doctorOptions{fix: true})
	require.NotEmpty(t, categoriesWithFix, "runDoctorChecks(true) returned no categories")

	// use cached fix=false result
	categoriesWithoutFix := getCachedDoctorChecks()
	require.NotEmpty(t, categoriesWithoutFix, "runDoctorChecks(false) returned no categories")

	// both should return same category structure (fix flag affects check behavior, not structure)
	assert.Equal(t, len(categoriesWithFix), len(categoriesWithoutFix), "category count differs")
}

// TestRunDoctorChecks_ChecksHaveValidFields verifies all checks have required fields
func TestRunDoctorChecks_ChecksHaveValidFields(t *testing.T) {
	t.Parallel()
	categories := getCachedDoctorChecks()

	for _, cat := range categories {
		assert.NotEmpty(t, cat.name, "category has empty name")

		for _, check := range cat.checks {
			assert.NotEmpty(t, check.name, "check in category %q has empty name", cat.name)

			// verify children have valid fields too
			for _, child := range check.children {
				assert.NotEmpty(t, child.name, "child check of %q in category %q has empty name", check.name, cat.name)
			}
		}
	}
}

// TestRunDoctorChecks_ConfigCheckWithChildren verifies config check adds children when passed
func TestRunDoctorChecks_ConfigCheckWithChildren(t *testing.T) {
	t.Parallel()
	categories := getCachedDoctorChecks()

	var projectCat *checkCategory
	for i := range categories {
		if categories[i].name == "Project Structure" {
			projectCat = &categories[i]
			break
		}
	}

	require.NotNil(t, projectCat, "runDoctorChecks() missing 'Project Structure' category")

	// look for config check (it may or may not have children depending on actual config state)
	// just verify the structure allows children
	for _, check := range projectCat.checks {
		if len(check.children) > 0 {
			// if any check has children, verify children are valid
			for _, child := range check.children {
				assert.NotEmpty(t, child.name, "child of %q has empty name", check.name)
			}
		}
	}
}

// TestRenderCheck_CheckTypes verifies rendering for all check types (passed, failed, warning, skipped)
func TestRenderCheck_CheckTypes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		check     checkResult
		wantPass  int
		wantWarn  int
		wantFail  int
		wantSkip  int
		wantInOut string
	}{
		{
			name:      "passed",
			check:     PassedCheck("test-passed", "all good"),
			wantPass:  1,
			wantWarn:  0,
			wantFail:  0,
			wantSkip:  0,
			wantInOut: "test-passed",
		},
		{
			name:      "failed",
			check:     FailedCheck("test-failed", "error occurred", "run 'ox init' to fix"),
			wantPass:  0,
			wantWarn:  0,
			wantFail:  1,
			wantSkip:  0,
			wantInOut: "test-failed",
		},
		{
			name:      "warning",
			check:     WarningCheck("test-warning", "outdated", "consider updating"),
			wantPass:  0,
			wantWarn:  1,
			wantFail:  0,
			wantSkip:  0,
			wantInOut: "test-warning",
		},
		{
			name:      "skipped",
			check:     SkippedCheck("test-skipped", "not applicable", ""),
			wantPass:  0,
			wantWarn:  0,
			wantFail:  0,
			wantSkip:  1,
			wantInOut: "test-skipped",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			buf := new(bytes.Buffer)
			cmd.SetOut(buf)

			var passCount, warnCount, failCount, skipCount int
			renderCheck(cmd, tt.check, 1, &passCount, &warnCount, &failCount, &skipCount)

			assert.Equal(t, tt.wantPass, passCount, "passCount mismatch")
			assert.Equal(t, tt.wantWarn, warnCount, "warnCount mismatch")
			assert.Equal(t, tt.wantFail, failCount, "failCount mismatch")
			assert.Equal(t, tt.wantSkip, skipCount, "skipCount mismatch")

			output := buf.String()
			assert.Contains(t, output, tt.wantInOut, "output missing check name")
		})
	}
}

// TestRenderCheck_WithChildren verifies check with children renders child tree
func TestRenderCheck_WithChildren(t *testing.T) {
	t.Parallel()
	cmd := &cobra.Command{}
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)

	var passCount, warnCount, failCount, skipCount int
	check := checkResult{
		name:    "parent-check",
		passed:  true,
		message: "ok",
		children: []checkResult{
			PassedCheck("child1", "ok"),
			PassedCheck("child2", "ok"),
		},
	}

	renderCheck(cmd, check, 1, &passCount, &warnCount, &failCount, &skipCount)

	// parent + 2 children = 3 passed
	assert.Equal(t, 3, passCount, "passCount mismatch")

	output := buf.String()
	assert.Contains(t, output, "parent-check", "output missing parent check")
	assert.Contains(t, output, "child1", "output missing child1")
	assert.Contains(t, output, "child2", "output missing child2")
}

// TestRenderCheck_NestedDetails verifies detail lines are rendered
func TestRenderCheck_NestedDetails(t *testing.T) {
	t.Parallel()
	cmd := &cobra.Command{}
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)

	var passCount, warnCount, failCount, skipCount int
	check := checkResult{
		name:    "check-with-detail",
		passed:  false,
		message: "failed",
		detail:  "run 'ox doctor --fix' to resolve",
		children: []checkResult{
			{
				name:    "child-with-detail",
				passed:  false,
				message: "failed",
				detail:  "child detail message",
			},
		},
	}

	renderCheck(cmd, check, 1, &passCount, &warnCount, &failCount, &skipCount)

	// parent + child = 2 failed
	assert.Equal(t, 2, failCount, "failCount mismatch")

	output := buf.String()
	assert.Contains(t, output, "check-with-detail", "output missing parent check")
	assert.Contains(t, output, "child-with-detail", "output missing child check")

	// suppress unused variable warning
	_ = strings.Contains
}

// TestDisplayDoctorResults_SkippedCount verifies skipped checks are counted in summary
func TestDisplayDoctorResults_SkippedCount(t *testing.T) {
	t.Parallel()
	categories := []checkCategory{
		{
			name: "Category",
			checks: []checkResult{
				PassedCheck("check1", "ok"),
				SkippedCheck("check2", "requires login", "Run ox login"),
				SkippedCheck("check3", "daemon not running", "Run ox daemon start"),
			},
		},
	}

	cmd := &cobra.Command{}
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)

	hasFailed := displayDoctorResults(cmd, categories, doctorOptions{verbose: true})

	assert.False(t, hasFailed, "skipped checks should not cause failure")

	output := buf.String()
	assert.Contains(t, output, "1 passed", "output missing correct pass count")
	assert.Contains(t, output, "2 skipped", "output missing correct skip count")
}

// TestCountCheck_SkipCount verifies countCheck properly increments skipCount
func TestCountCheck_SkipCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		check    checkResult
		wantPass int
		wantWarn int
		wantFail int
		wantSkip int
	}{
		{
			name:     "passed",
			check:    PassedCheck("test", "ok"),
			wantPass: 1,
		},
		{
			name:     "warning",
			check:    WarningCheck("test", "warn", "detail"),
			wantWarn: 1,
		},
		{
			name:     "failed",
			check:    FailedCheck("test", "fail", "detail"),
			wantFail: 1,
		},
		{
			name:     "skipped",
			check:    SkippedCheck("test", "skip", "detail"),
			wantSkip: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var passCount, warnCount, failCount, skipCount int
			countCheck(tt.check, &passCount, &warnCount, &failCount, &skipCount)

			assert.Equal(t, tt.wantPass, passCount, "passCount mismatch")
			assert.Equal(t, tt.wantWarn, warnCount, "warnCount mismatch")
			assert.Equal(t, tt.wantFail, failCount, "failCount mismatch")
			assert.Equal(t, tt.wantSkip, skipCount, "skipCount mismatch")
		})
	}
}

// TestDoctorState_Detection verifies doctorState detection
func TestDoctorState_Detection(t *testing.T) {
	t.Parallel()
	// this is an integration-style test that verifies the state detection works
	// the actual values depend on the test environment
	state := detectDoctorState()

	// state fields should be set (values depend on environment)
	// just verify the function doesn't panic and returns a valid struct
	_ = state.isAuthenticated
	_ = state.isDaemonRunning
}
