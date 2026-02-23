//go:build !short

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDoctorFreshInstall_NoWarnings verifies that after a fresh `ox init` in a new repo,
// `ox doctor` reports no warnings or failures.
//
// Rationale: Users should never start in a "dirty" or "bad" state. A fresh install
// should be clean and ready to use without any remediation steps.
//
// This is a critical invariant for user experience - if init creates a broken state,
// users lose trust in the tool immediately.
func TestDoctorFreshInstall_NoWarnings(t *testing.T) {
	// create a fresh git repo
	tmpDir := testGitRepo(t)

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(tmpDir)

	// simulate ox init by creating the minimal required structure
	// (we can't call initCmd directly due to auth requirements, so we create the structure)
	createFreshSageoxStructure(t, tmpDir)

	// run doctor checks with default options (no fix)
	opts := doctorOptions{
		fix:      false,
		verbose:  true, // see all checks for debugging
		forceYes: true, // non-interactive
	}
	categories := runDoctorChecks(opts)

	// collect all warnings, failures, and fixable issues
	var warnings []string
	var failures []string
	var fixableIssues []string

	for _, cat := range categories {
		for _, check := range cat.checks {
			collectIssues(check, cat.name, &warnings, &failures)
			collectFixableIssues(check, cat.name, &fixableIssues)
		}
	}

	// authentication is expected to fail in test environment (no token)
	// filter out auth-related issues since this test is about project structure
	warnings = filterTestEnvironmentIssues(warnings)
	failures = filterTestEnvironmentIssues(failures)
	fixableIssues = filterTestEnvironmentIssues(fixableIssues)

	// assert no warnings
	if len(warnings) > 0 {
		t.Errorf("fresh install should have no warnings, got %d:\n%v", len(warnings), warnings)
	}

	// assert no failures (except auth which is expected)
	if len(failures) > 0 {
		t.Errorf("fresh install should have no failures, got %d:\n%v", len(failures), failures)
	}

	// assert no fixable issues - a fresh install should be clean with nothing to fix
	if len(fixableIssues) > 0 {
		t.Errorf("fresh install should have no fixable issues (no [--fix] or [auto-fix] needed), got %d:\n%v",
			len(fixableIssues), fixableIssues)
	}
}

// TestDoctorFreshInstall_EmptyRepo_NoWarnings verifies that in an empty repo
// (no ox init yet), doctor reports appropriate status without confusing warnings.
func TestDoctorFreshInstall_EmptyRepo_NoWarnings(t *testing.T) {
	// create a fresh git repo with no .sageox
	tmpDir := testGitRepo(t)

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(tmpDir)

	// run doctor checks with default options
	opts := doctorOptions{
		fix:      false,
		verbose:  true,
		forceYes: true,
	}
	categories := runDoctorChecks(opts)

	// in an empty repo, we expect .sageox check to be skipped (not a warning)
	// the user simply hasn't run init yet
	var unexpectedWarnings []string

	for _, cat := range categories {
		for _, check := range cat.checks {
			// skip expected issues in an empty repo test environment
			if isExpectedEmptyRepoIssue(cat.name, check) {
				continue
			}
			if check.warning && !check.skipped {
				unexpectedWarnings = append(unexpectedWarnings, cat.name+": "+check.name+": "+check.message)
			}
		}
	}

	if len(unexpectedWarnings) > 0 {
		t.Errorf("empty repo should not have unexpected warnings, got:\n%v", unexpectedWarnings)
	}
}

// isExpectedEmptyRepoIssue returns true if the check result is expected in an
// empty repo test environment (no .sageox, no auth, no daemon, no API)
func isExpectedEmptyRepoIssue(category string, check checkResult) bool {
	// Authentication is expected to fail in test environment
	if category == "Authentication" {
		return true
	}
	// .sageox missing should be skipped, not a warning
	if check.name == ".sageox" && check.skipped {
		return true
	}
	// Integration: Agent file not found is expected in empty repo
	if category == "Integration" && strings.Contains(check.message, "none found") {
		return true
	}
	// Git remotes - test repos have no remotes
	if strings.Contains(check.name, "Git remotes") {
		return true
	}
	// Discovery not run is optional/expected
	if strings.Contains(check.name, "discovery") {
		return true
	}
	// API unavailable in test environment
	if strings.Contains(check.message, "API unavailable") || strings.Contains(check.message, "unreachable") {
		return true
	}
	// Daemon not running in tests
	if strings.Contains(check.name, "heartbeat") {
		return true
	}
	// Team not registered in tests
	if strings.Contains(check.message, "not registered") {
		return true
	}
	// Ledger for sessions not provisioned in test environment
	if strings.Contains(check.name, "ledger for sessions") &&
		strings.Contains(check.message, "not provisioned") {
		return true
	}
	return false
}

// createFreshSageoxStructure creates the minimal .sageox structure that ox init would create
func createFreshSageoxStructure(t *testing.T, gitRoot string) {
	t.Helper()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755), "failed to create .sageox")

	// use the same config creation as ensureSageoxConfig() - uses config.GetDefaultProjectConfig()
	result := ensureSageoxConfig(gitRoot)
	require.True(t, result == configCreated || result == configPreserved, "failed to create config.json")

	// create repo marker (ox init creates .repo_<uuid>)
	repoID := "repo_01test000000000000000000"
	markerPath := filepath.Join(sageoxDir, ".repo_"+repoID[5:]) // strip "repo_" prefix
	require.NoError(t, os.WriteFile(markerPath, []byte("{}"), 0644), "failed to create repo marker")

	// README.md
	readmeContent := GetSageoxReadmeContent(nil)
	require.NoError(t, os.WriteFile(
		filepath.Join(sageoxDir, "README.md"),
		[]byte(readmeContent),
		0644,
	), "failed to create README.md")

	// .gitignore for .sageox directory - match what init creates
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	require.NoError(t, os.WriteFile(gitignorePath, []byte(sageoxGitignoreContent), 0644),
		"failed to create .sageox/.gitignore")

	// AGENTS.md in repo root with canonical SageOx format (both header and footer markers)
	agentsContent := OxPrimeCheckBlock + "\n# AI Agent Instructions\n\n" + OxPrimeLine + "\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(gitRoot, "AGENTS.md"),
		[]byte(agentsContent),
		0644,
	), "failed to create AGENTS.md")

	// install project-level Claude Code hooks (ox init does this)
	require.NoError(t, InstallProjectClaudeHooks(gitRoot), "failed to install project Claude hooks")

	// .gitignore in repo root (ensure sageox files are properly handled)
	rootGitignore := `# SageOx
.sageox/*.local.*
.sageox/discovered.jsonl
`
	require.NoError(t, os.WriteFile(
		filepath.Join(gitRoot, ".gitignore"),
		[]byte(rootGitignore),
		0644,
	), "failed to create root .gitignore")

	// commit the initial structure so git status is clean
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = gitRoot
	require.NoError(t, cmd.Run(), "failed to git add")

	cmd = exec.Command("git", "commit", "-m", "Initial SageOx setup")
	cmd.Dir = gitRoot
	require.NoError(t, cmd.Run(), "failed to git commit")
}

// collectIssues recursively collects warnings and failures from check results
func collectIssues(check checkResult, category string, warnings, failures *[]string) {
	prefix := category + ": " + check.name + ": "

	if check.warning {
		*warnings = append(*warnings, prefix+check.message)
	}
	if !check.passed && !check.skipped && !check.warning {
		*failures = append(*failures, prefix+check.message)
	}

	// recurse into children
	for _, child := range check.children {
		collectIssues(child, category, warnings, failures)
	}
}

// collectFixableIssues recursively collects checks that have fixes available
// (i.e., would show [--fix] or [auto-fix] badges in output)
func collectFixableIssues(check checkResult, category string, fixable *[]string) {
	prefix := category + ": " + check.name + ": "

	// a fixable issue is one that:
	// 1. is not passed (has a problem)
	// 2. is not skipped (is applicable)
	// 3. has a fix level that's not check-only
	if !check.passed && !check.skipped && check.fixLevel != "" && check.fixLevel != FixLevelCheckOnly {
		*fixable = append(*fixable, prefix+check.message+" ["+string(check.fixLevel)+"]")
	}

	// recurse into children
	for _, child := range check.children {
		collectFixableIssues(child, category, fixable)
	}
}

// TestDoctorFreshCheckout_NoSideEffectDirectories verifies that running doctor
// on a freshly checked-out repo (with .sageox/ committed) does not create
// the sibling _sageox directory as a side-effect. This was the root cause of
// bug #35: checkStorageHealth created an empty ledger directory, which then
// caused checkGitRepoPaths to fail with "empty directory".
func TestDoctorFreshCheckout_NoSideEffectDirectories(t *testing.T) {
	// create a fresh git repo simulating a checkout that already has .sageox/
	tmpDir := testGitRepo(t)

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(tmpDir)

	// simulate a checkout that already has .sageox/ committed (like cloning ox)
	createFreshSageoxStructure(t, tmpDir)

	// record the parent directory contents before doctor
	parentDir := filepath.Dir(tmpDir)
	beforeEntries, err := os.ReadDir(parentDir)
	require.NoError(t, err)
	beforeNames := make(map[string]bool)
	for _, e := range beforeEntries {
		beforeNames[e.Name()] = true
	}

	// run doctor checks (no --fix)
	opts := doctorOptions{
		fix:      false,
		verbose:  false,
		forceYes: true,
	}
	_ = runDoctorChecks(opts)

	// verify no new directories were created in the parent
	afterEntries, err := os.ReadDir(parentDir)
	require.NoError(t, err)

	var newDirs []string
	for _, e := range afterEntries {
		if !beforeNames[e.Name()] && e.IsDir() {
			newDirs = append(newDirs, e.Name())
		}
	}

	repoName := filepath.Base(tmpDir)
	siblingName := repoName + "_sageox"
	for _, dir := range newDirs {
		if dir == siblingName || strings.HasSuffix(dir, "_sageox") {
			t.Errorf("ox doctor created sibling directory %q as side-effect; "+
				"health checks must not create filesystem state", dir)
		}
	}
}

// filterTestEnvironmentIssues removes issues that are expected in a test environment
// without real credentials, API access, or daemon running
func filterTestEnvironmentIssues(issues []string) []string {
	var filtered []string
	for _, issue := range issues {
		// skip auth-related issues
		if strings.Contains(issue, "Authentication") ||
			strings.Contains(issue, "not logged in") ||
			strings.Contains(issue, "Logged in") {
			continue
		}
		// skip API connectivity issues (test environment has no API)
		if strings.Contains(issue, "API unavailable") ||
			strings.Contains(issue, "unreachable") ||
			strings.Contains(issue, "not registered") {
			continue
		}
		// skip daemon-related issues (no daemon in test)
		if strings.Contains(issue, "heartbeat") ||
			strings.Contains(issue, "no heartbeat") {
			continue
		}
		// skip git remote issues (test repos have no remotes)
		if strings.Contains(issue, "no remotes configured") ||
			strings.Contains(issue, "Git remotes") {
			continue
		}
		// skip repo paths when no ledger/team contexts configured
		if strings.Contains(issue, "git repo paths") &&
			strings.Contains(issue, "no repos configured") {
			continue
		}
		// skip ox in PATH - not expected in test environment
		if strings.Contains(issue, "ox in PATH") {
			continue
		}
		// skip discovery - optional and not run in tests
		if strings.Contains(issue, "discovery") &&
			strings.Contains(issue, "not run") {
			continue
		}
		// skip ledger for sessions - not provisioned in test environment
		if strings.Contains(issue, "ledger for sessions") &&
			strings.Contains(issue, "not provisioned") {
			continue
		}
		filtered = append(filtered, issue)
	}
	return filtered
}
