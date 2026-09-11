//go:build integration

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFreshInstall_MockServer_InitThenDoctor simulates the exact flow a new user follows:
//
//  1. Has an existing repo (git repo with commits)
//  2. Runs ox init --quiet --team <id>
//  3. Immediately runs ox doctor --json (NO ox sync in between)
//
// Uses a mock API server so this test runs without credentials.
// The test captures and logs EVERY warning/failure that doctor reports.
// This is the diagnostic test -- the report IS the deliverable.
func TestFreshInstall_MockServer_InitThenDoctor(t *testing.T) {
	oxBin := buildOxBinary(t)
	mockServer := startMockSageoxAPI(t)
	repoDir := testGitRepo(t)
	envVars := setupIsolatedAuth(t, mockServer.URL)

	testguard.StopDaemonCleanup(t, oxBin, repoDir, envVars)

	// === STEP 1: ox init ===
	initOutput, initExit, initDuration := testguard.RunOx(t, oxBin, repoDir, envVars,
		"init", "--quiet", "--team", "team_test123")

	t.Logf("ox init completed: exit=%d duration=%s", initExit, initDuration)

	require.Equal(t, 0, initExit, "ox init must succeed before doctor evidence is meaningful; output:\n%s", initOutput)

	// Match the documented quick start: persist init's repository changes before
	// asking doctor to distinguish product health from normal uncommitted setup.
	gitAdd := testguard.OxCmd(t, "git", repoDir, nil, "add", "-A")
	require.NoError(t, gitAdd.Run())
	gitCommit := testguard.OxCmd(t, "git", repoDir, nil,
		"-c", "commit.gpgsign=false", "commit", "-m", "initialize SageOx")
	commitOutput, err := gitCommit.CombinedOutput()
	require.NoError(t, err, "commit initialized files: %s", commitOutput)

	// === STEP 2: ox doctor (after init commit, NO ox sync) ===
	doctorOutput, doctorExit, doctorDuration := testguard.RunOx(t, oxBin, repoDir, envVars,
		"doctor", "--json", "--verbose")

	t.Logf("ox doctor completed: exit=%d duration=%s", doctorExit, doctorDuration)
	require.Equal(t, 0, doctorExit, "doctor must succeed when the cloud twin reports no issues; output:\n%s", doctorOutput)

	// === STEP 3: parse and report ===
	doctorJSON := parseDoctorJSON(t, doctorOutput)

	report := catalogReport(doctorJSON)
	report.InitOutput = initOutput
	report.InitExitCode = initExit
	report.InitDuration = initDuration
	report.DoctorOutput = doctorOutput
	report.DoctorExitCode = doctorExit
	report.DoctorDuration = doctorDuration

	logReport(t, report)

	// === ASSERTIONS ===
	require.NotNil(t, doctorJSON, "doctor must produce parseable JSON; output:\n%s", doctorOutput)
	assert.Greater(t, len(doctorJSON.Categories), 0, "doctor should return at least one category")
	assert.Greater(t, mockServer.cloudDoctorAuthedCalls.Load(), int32(0),
		"compiled ox must exercise the current authenticated cloud doctor route")
	assert.Zero(t, mockServer.cloudDoctorLegacyCalls.Load(),
		"compiled ox must not fall back after the current route succeeds")
	assert.Zero(t, mockServer.cloudDoctorSingularCalls.Load(),
		"compiled ox must not use the obsolete singular cloud doctor route")

	t.Logf("Doctor summary: passed=%d warnings=%d failed=%d skipped=%d",
		doctorJSON.Summary.Passed, doctorJSON.Summary.Warnings,
		doctorJSON.Summary.Failed, doctorJSON.Summary.Skipped)

	// assert the bootstrapping grace period is working:
	// git repo paths should NOT be a failure right after init (it should be info/warning)
	for _, f := range report.Failures {
		if strings.Contains(f.Name, "git repo paths") {
			t.Errorf("git repo paths should not be a failure right after init; got: %s (priority=%s)", f.Message, f.Priority)
		}
	}

	// filter out expected test-environment issues (no real daemon, no real API connectivity beyond mock)
	// NOTE: category-level exclusions are broad. A regression inside an excluded
	// category will not be caught by this test. The real-infra test
	// (TestFreshInstall_RealRepo_InitThenDoctor) covers them.
	unexpectedFailures := filterExpectedE2EIssues(report.Failures)
	unexpectedWarnings := filterExpectedE2EIssues(report.Warnings)

	if len(unexpectedFailures) > 0 {
		t.Errorf("unexpected failures after fresh install (%d):", len(unexpectedFailures))
		for _, f := range unexpectedFailures {
			t.Errorf("  %s > %s: %s [fix: %s]", f.Category, f.Name, f.Message, f.FixLevel)
		}
	}

	if len(unexpectedWarnings) > 0 {
		t.Errorf("unexpected warnings after fresh install (%d):", len(unexpectedWarnings))
		for _, w := range unexpectedWarnings {
			t.Errorf("  %s > %s: %s [fix: %s]", w.Category, w.Name, w.Message, w.FixLevel)
		}
	}
}

func TestParseDoctorJSONOutput(t *testing.T) {
	payload := `{
  "categories": [
    {"name": "setup", "checks": []}
  ],
  "summary": {"passed": 1}
}`

	tests := []struct {
		name    string
		output  string
		wantErr bool
	}{
		{name: "clean pretty JSON", output: payload},
		{name: "logs around pretty JSON", output: "INFO starting doctor\n" + payload + "\nINFO complete"},
		{name: "unrelated braces before payload", output: "DEBUG response={not-json}\n" + payload},
		{name: "no payload", output: "INFO doctor failed before output", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDoctorJSONOutput(tt.output)
			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Len(t, got.Categories, 1)
		})
	}
}

// TestFreshInstall_RealRepo_InitThenDoctor runs the same flow against real
// infrastructure using steveyegge/beads as the test repo.
//
// Requires environment variables:
//
//	SAGEOX_TEST_TOKEN    - a valid API access token for the test endpoint
//	SAGEOX_TEST_ENDPOINT - the SageOx API endpoint (default: https://test.sageox.ai)
//	SAGEOX_TEST_TEAM_ID  - team ID to associate the repo with
//
// This test captures what a REAL customer would see.
func TestFreshInstall_RealRepo_InitThenDoctor(t *testing.T) {
	token := os.Getenv("SAGEOX_TEST_TOKEN")
	if token == "" {
		t.Skip("SAGEOX_TEST_TOKEN not set; skipping real infrastructure test")
	}

	endpointURL := os.Getenv("SAGEOX_TEST_ENDPOINT")
	if endpointURL == "" {
		endpointURL = "https://test.sageox.ai"
	}

	teamID := os.Getenv("SAGEOX_TEST_TEAM_ID")
	if teamID == "" {
		t.Skip("SAGEOX_TEST_TEAM_ID not set; skipping real infrastructure test")
	}

	oxBin := buildOxBinary(t)
	repoDir := cloneTestRepo(t, "https://github.com/steveyegge/beads.git")
	t.Logf("cloned steveyegge/beads to %s", repoDir)

	envVars := setupRealAuth(t, endpointURL, token)

	testguard.StopDaemonCleanup(t, oxBin, repoDir, envVars)

	// === STEP 1: ox init ===
	initOutput, initExit, initDuration := testguard.RunOx(t, oxBin, repoDir, envVars,
		"init", "--quiet", "--team", teamID)

	t.Logf("ox init completed: exit=%d duration=%s", initExit, initDuration)

	require.Equal(t, 0, initExit, "ox init must succeed before sync/doctor evidence is meaningful; output:\n%s", initOutput)

	// === STEP 2: ox doctor (immediately, NO ox sync) ===
	doctorOutput, doctorExit, doctorDuration := testguard.RunOx(t, oxBin, repoDir, envVars,
		"doctor", "--json", "--verbose")

	t.Logf("ox doctor completed: exit=%d duration=%s", doctorExit, doctorDuration)

	// === STEP 3: parse and report ===
	doctorJSON := parseDoctorJSON(t, doctorOutput)

	report := catalogReport(doctorJSON)
	report.InitOutput = initOutput
	report.InitExitCode = initExit
	report.InitDuration = initDuration
	report.DoctorOutput = doctorOutput
	report.DoctorExitCode = doctorExit
	report.DoctorDuration = doctorDuration

	logReport(t, report)

	// === ASSERTIONS ===
	require.NotNil(t, doctorJSON, "doctor should produce valid JSON output")
	assert.Greater(t, len(doctorJSON.Categories), 0, "doctor should return categories")

	t.Logf("Doctor summary: passed=%d warnings=%d failed=%d skipped=%d",
		doctorJSON.Summary.Passed, doctorJSON.Summary.Warnings,
		doctorJSON.Summary.Failed, doctorJSON.Summary.Skipped)

	// for real infra test, log ALL issues without filtering -- purely diagnostic
	if len(report.Failures) > 0 {
		t.Logf("REAL INFRA: %d failures found (this is diagnostic, not necessarily a test failure)", len(report.Failures))
	}
	if len(report.Warnings) > 0 {
		t.Logf("REAL INFRA: %d warnings found (this is diagnostic, not necessarily a test failure)", len(report.Warnings))
	}
}

// TestFreshInstall_MockServer_SyncUnavailableThenDoctorStillWorks proves an
// unavailable sync prerequisite fails closed without preventing the next
// diagnostic command from working. This is a survivability check, not a claim
// that partial sync state was repaired; stateful retry coverage lives in the
// daemon and session lifecycle suites.
func TestFreshInstall_MockServer_SyncUnavailableThenDoctorStillWorks(t *testing.T) {
	oxBin := buildOxBinary(t)
	mockServer := startMockSageoxAPI(t)
	repoDir := testGitRepo(t)
	envVars := setupIsolatedAuth(t, mockServer.URL)

	testguard.StopDaemonCleanup(t, oxBin, repoDir, envVars)

	// step 1: init
	initOutput, initExit, initDuration := testguard.RunOx(t, oxBin, repoDir, envVars,
		"init", "--quiet", "--team", "team_test123")

	t.Logf("ox init: exit=%d duration=%s", initExit, initDuration)
	require.Equal(t, 0, initExit, "init must succeed before recovery evidence is meaningful; output:\n%s", initOutput)
	gitAdd := testguard.OxCmd(t, "git", repoDir, nil, "add", "-A")
	require.NoError(t, gitAdd.Run())
	gitCommit := testguard.OxCmd(t, "git", repoDir, nil,
		"-c", "commit.gpgsign=false", "commit", "-m", "initialize SageOx")
	require.NoError(t, gitCommit.Run())

	// step 2: sync fails at the unavailable daemon boundary
	syncOutput, syncExit, syncDuration := testguard.RunOx(t, oxBin, repoDir, envVars, "sync")
	t.Logf("ox sync: exit=%d duration=%s output:\n%s", syncExit, syncDuration, syncOutput)
	require.NotEqual(t, 0, syncExit, "sync must fail when daemon startup is explicitly disabled")
	assert.Contains(t, syncOutput, "daemon start disabled",
		"failure must identify the unavailable boundary rather than reporting false success")

	// step 3: doctor remains independently usable
	doctorOutput, doctorExit, doctorDuration := testguard.RunOx(t, oxBin, repoDir, envVars,
		"doctor", "--json", "--verbose")
	require.Equal(t, 0, doctorExit,
		"doctor must recover after the failed sync without requiring repository repair; output:\n%s", doctorOutput)

	doctorJSON := parseDoctorJSON(t, doctorOutput)
	report := catalogReport(doctorJSON)
	report.InitOutput = initOutput
	report.InitExitCode = initExit
	report.InitDuration = initDuration
	report.DoctorOutput = doctorOutput
	report.DoctorExitCode = doctorExit
	report.DoctorDuration = doctorDuration

	t.Log("=== WITH SYNC (comparison test) ===")
	logReport(t, report)

	require.NotNil(t, doctorJSON, "doctor must produce parseable JSON; output:\n%s", doctorOutput)
	assert.Greater(t, mockServer.cloudDoctorAuthedCalls.Load(), int32(0),
		"recovery must reach the current authenticated cloud doctor route")
	assert.Zero(t, mockServer.cloudDoctorLegacyCalls.Load(),
		"recovery must not hide a current-route regression behind the legacy fallback")
	assert.Zero(t, mockServer.cloudDoctorSingularCalls.Load(),
		"recovery must not use the obsolete singular route")
	require.Empty(t, filterExpectedE2EIssues(report.Failures),
		"unavailable sync must not leave an unexpected doctor failure: %+v", report.Failures)
	require.Empty(t, filterExpectedE2EIssues(report.Warnings),
		"unavailable sync must not leave an unexpected doctor warning: %+v", report.Warnings)
	t.Logf("With-sync summary: passed=%d warnings=%d failed=%d skipped=%d",
		doctorJSON.Summary.Passed, doctorJSON.Summary.Warnings,
		doctorJSON.Summary.Failed, doctorJSON.Summary.Skipped)
}

// filterExpectedE2EIssues removes issues that are expected in an E2E test
// environment with mock server (no real daemon, no real git repos to clone).
func filterExpectedE2EIssues(checks []ReportCheck) []ReportCheck {
	var unexpected []ReportCheck
	for _, check := range checks {
		if isExpectedE2EIssue(check) {
			continue
		}
		unexpected = append(unexpected, check)
	}
	return unexpected
}

// isExpectedE2EIssue returns true if the check is expected to fail/warn
// in the E2E test environment.
// IMPORTANT: Keep this tight — only exclude issues inherent to mock/test environments.
// Do NOT exclude issues that indicate real bugs (unstaged files, uncommitted changes, etc.).
func isExpectedE2EIssue(check ReportCheck) bool {
	cat := check.Category
	msg := check.Message
	name := check.Name

	// info-priority checks are informational, not actionable — always expected
	if check.Priority == "info" {
		return true
	}

	// daemon not running -- expected in E2E test (we don't start one)
	if cat == "Daemon" {
		return true
	}

	// authentication issues -- mock server token may not pass all checks
	if cat == "Authentication" {
		return true
	}

	// no real git remotes in test repo
	if containsAny(name, "Git remotes", "remotes") ||
		containsAny(msg, "no remotes configured") {
		return true
	}

	// team context not cloned in test
	if cat == "Team Context" {
		return true
	}

	// sessions/ledger not provisioned locally (no daemon to clone)
	if containsAny(name, "ledger for sessions") && containsAny(msg, "not provisioned") {
		return true
	}

	// ox in PATH -- not expected in isolated test
	if containsAny(name, "ox in PATH") {
		return true
	}

	// Adapter binaries -- the E2E harness builds only the ox binary into a
	// scratch dir, never its ten ox-adapter-* siblings, so "1/10 present" is
	// the correct reading of a correct environment, not a defect under test.
	if containsAny(name, "Adapter binaries") {
		return true
	}

	// discovery not run -- optional
	if containsAny(name, "discovery") {
		return true
	}

	// API connectivity -- mock server may not serve all endpoints
	if containsAny(msg, "API unavailable", "unreachable", "not registered") {
		return true
	}

	// SageOx service checks -- mock may not cover all
	if cat == "SageOx Service" {
		return true
	}

	// cloud diagnostics
	if cat == "Cloud Diagnostics" {
		return true
	}

	// version/update checks
	if cat == "Updates" {
		return true
	}

	// Historical commits predate session capture in this isolated repository.
	if cat == "Sessions" && name == "session trailer coverage" {
		return true
	}

	return false
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
