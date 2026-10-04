package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A failed doctor report must stop a shell pipeline in text and JSON modes,
// including setup failures that return before the regular checks run.
func TestDoctorExitCLI(t *testing.T) {
	skipIntegration(t)
	oxBin := testguard.BuildOxBinary(t, repoPath("..", ".."))
	for _, scenario := range []string{"outside_repo", "uninitialized", "unauthenticated", "failed_checks"} {
		modes := []string{"text", "json", "env_json"}
		if scenario == "outside_repo" || scenario == "uninitialized" {
			modes = append(modes, "unwritable_json")
		}
		for _, mode := range modes {
			t.Run(scenario+"/"+mode, func(t *testing.T) {
				env := append(noInputCLIEnv(t), "OX_NO_DAEMON=1", "FEATURE_CLOUD=false", "FEATURE_AUTH=false", "OX_JSON=")
				dir := t.TempDir()
				if scenario != "outside_repo" {
					dir = testGitRepo(t)
				}
				if scenario == "unauthenticated" || scenario == "failed_checks" {
					project := config.GetDefaultProjectConfig()
					project.RepoID = "repo_doctor_exit"
					require.NoError(t, config.SaveProjectConfig(dir, project))
				}
				if scenario == "unauthenticated" {
					env = append(env, "FEATURE_AUTH=true")
				}
				args := []string{"doctor", "--no-input", "--no-interactive"}
				switch mode {
				case "json", "unwritable_json":
					args = append(args, "--json")
				case "env_json":
					env = append(env, "OX_JSON=1")
				}
				if mode == "unwritable_json" {
					// Use direct output to exercise doctor's own writer error handling.
					env = append(env, "CLICOLOR_FORCE=1")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				cmd := testguard.OxCmdContext(t, ctx, oxBin, dir, env, args...)
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				if mode == "unwritable_json" {
					// A read-only stdout descriptor must surface the write error,
					// not replace it with a silent "checks failed" exit.
					path := filepath.Join(t.TempDir(), "report.json")
					require.NoError(t, os.WriteFile(path, nil, 0o600))
					file, err := os.Open(path)
					require.NoError(t, err)
					t.Cleanup(func() { _ = file.Close() })
					cmd.Stdout = file
				}
				err := cmd.Run()
				require.NoError(t, ctx.Err(), "doctor timed out: %s", stderr.String())
				var exitErr *exec.ExitError
				if assert.ErrorAs(t, err, &exitErr, "failed doctor report exited successfully: %s", stdout.String()) {
					assert.Equal(t, 1, exitErr.ExitCode())
				}
				if mode == "unwritable_json" {
					assert.Contains(t, stderr.String(), "Error:")
					assert.Contains(t, stderr.String(), "write")
					return
				}
				if mode == "text" {
					assert.NotEmpty(t, stdout.String(), "retain the human-readable report")
					return
				}
				var report JSONDoctorOutput
				require.NoError(t, json.Unmarshal(stdout.Bytes(), &report), "stdout must contain exactly one JSON report: %s", stdout.String())
				assert.True(t, report.Summary.HasFailed)
				assert.Positive(t, report.Summary.Failed)
				assert.NotContains(t, stderr.String(), "Error:", "the JSON report already explains the failed checks")
				if scenario == "failed_checks" {
					assert.Greater(t, len(report.Categories), 1, "must reach the regular check pipeline, not the setup gate")
				} else {
					require.Len(t, report.Categories, 1)
					assert.Equal(t, "Setup", report.Categories[0].Name)
				}
			})
		}
	}
}

// cachedDoctorChecks caches runDoctorChecks result for tests that only need to
// verify structure/behavior, not test multiple scenarios. This saves ~60s in test time.
//
// Skips in -short mode: a real runDoctorChecks shells out to git/auth/etc. and
// against a developer machine with a daemon-discovered team context it can hang
// the entire test package on a real network operation. Fast-tier callers should
// never depend on full doctor state.
var (
	cachedCategories     []checkCategory
	cachedDoctorState    doctorState
	cachedCategoriesOnce sync.Once
)

func getCachedDoctorChecks(t *testing.T) []checkCategory {
	t.Helper()
	if testing.Short() {
		t.Skip("short: full doctor pipeline shells out to git/auth — see .claude/rules/testing.md")
	}
	cachedCategoriesOnce.Do(func() {
		cachedDoctorState = detectDoctorState()
		var err error
		cachedCategories, err = runDoctorChecksWithState(context.Background(), doctorOptions{fix: false}, cachedDoctorState)
		require.NoError(t, err)
	})
	return cachedCategories
}

// TestCheckResultConstructors consolidates tests for PassedCheck, FailedCheck, WarningCheck, SkippedCheck
func TestCheckResultConstructors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		fn         func() checkResult
		wantPassed bool
		wantWarn   bool
		wantSkip   bool
		wantMsg    string
		wantDetail string
	}{
		{
			name:       "PassedCheck",
			fn:         func() checkResult { return PassedCheck("test", "ok") },
			wantPassed: true,
			wantWarn:   false,
			wantSkip:   false,
			wantMsg:    "ok",
			wantDetail: "",
		},
		{
			name:       "FailedCheck",
			fn:         func() checkResult { return FailedCheck("test", "not found", "run ox init") },
			wantPassed: false,
			wantWarn:   false,
			wantSkip:   false,
			wantMsg:    "not found",
			wantDetail: "run ox init",
		},
		{
			name:       "WarningCheck",
			fn:         func() checkResult { return WarningCheck("test", "outdated", "run ox update") },
			wantPassed: true,
			wantWarn:   true,
			wantSkip:   false,
			wantMsg:    "outdated",
			wantDetail: "run ox update",
		},
		{
			name:       "SkippedCheck",
			fn:         func() checkResult { return SkippedCheck("test", "not applicable", "") },
			wantPassed: false,
			wantWarn:   false,
			wantSkip:   true,
			wantMsg:    "not applicable",
			wantDetail: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.fn()

			assert.Equal(t, "test", result.name, "name mismatch")
			assert.Equal(t, tc.wantPassed, result.passed, "passed mismatch")
			assert.Equal(t, tc.wantWarn, result.warning, "warning mismatch")
			assert.Equal(t, tc.wantSkip, result.skipped, "skipped mismatch")
			assert.Equal(t, tc.wantMsg, result.message, "message mismatch")
			assert.Equal(t, tc.wantDetail, result.detail, "detail mismatch")
		})
	}
}

// TestDoctorSuppression_DaemonNotRunning verifies that daemon-dependent checks
// are suppressed when daemon is not running
func TestDoctorSuppression_DaemonNotRunning(t *testing.T) {
	t.Parallel()
	categories := getCachedDoctorChecks(t)

	// find the Daemon category
	var daemonCat *checkCategory
	for i := range categories {
		if categories[i].name == "Daemon" {
			daemonCat = &categories[i]
			break
		}
	}

	require.NotNil(t, daemonCat, "Daemon category should always be present")

	// when daemon is not running, we should see a single grouped skip
	// rather than multiple individual warnings
	if !cachedDoctorState.isDaemonRunning {
		// should have exactly one check (the grouped skip)
		assert.Equal(t, 1, len(daemonCat.checks), "should have single grouped daemon check when daemon not running")

		// the check should be skipped with the grouped message
		// message varies: "not started" if project initialized, "DAEMON NOT RUNNING" otherwise
		check := daemonCat.checks[0]
		assert.True(t, check.skipped, "daemon check should be skipped when daemon not running")
		validMessages := check.message == "DAEMON NOT RUNNING" || check.message == "not started"
		assert.True(t, validMessages, "message should indicate daemon not running, got: %s", check.message)
	}
}

// TestDoctorSuppression_NotLoggedIn verifies that login-dependent checks
// are suppressed when not logged in
func TestDoctorSuppression_NotLoggedIn(t *testing.T) {
	t.Parallel()
	categories := getCachedDoctorChecks(t)

	// find the SageOx Service category
	var serviceCat *checkCategory
	for i := range categories {
		if categories[i].name == "SageOx Service" {
			serviceCat = &categories[i]
			break
		}
	}

	require.NotNil(t, serviceCat, "SageOx Service category should always be present")

	// when not logged in, we should see a single grouped skip
	// rather than multiple individual warnings
	if !cachedDoctorState.isAuthenticated {
		// should have exactly one check (the grouped skip)
		assert.Equal(t, 1, len(serviceCat.checks), "should have single grouped service check when not logged in")

		// the check should be skipped with the grouped message
		check := serviceCat.checks[0]
		assert.True(t, check.skipped, "service check should be skipped when not logged in")
		assert.Contains(t, check.message, "NOT LOGGED IN", "message should indicate not logged in")
	}
}

// TestDoctorSuppression_GitRepoPaths verifies that git repo paths check
// is suppressed when not logged in
func TestDoctorSuppression_GitRepoPaths(t *testing.T) {
	t.Parallel()
	categories := getCachedDoctorChecks(t)

	// find the Git Repository Health category
	var gitCat *checkCategory
	for i := range categories {
		if categories[i].name == "Git Repository Health" {
			gitCat = &categories[i]
			break
		}
	}

	require.NotNil(t, gitCat, "Git Repository Health category should always be present")

	// find the git repo paths check
	var repoPathsCheck *checkResult
	for i := range gitCat.checks {
		if strings.Contains(gitCat.checks[i].name, "git repo paths") {
			repoPathsCheck = &gitCat.checks[i]
			break
		}
	}

	require.NotNil(t, repoPathsCheck, "git repo paths check should be present")

	// when not logged in, the check should be skipped
	if !cachedDoctorState.isAuthenticated {
		assert.True(t, repoPathsCheck.skipped, "git repo paths should be skipped when not logged in")
		assert.Contains(t, repoPathsCheck.message, "requires login", "message should indicate requires login")
	}
}
