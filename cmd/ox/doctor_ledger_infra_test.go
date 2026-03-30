package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckLedgerSparseCheckout_WithSageox verifies that a ledger repo
// with .sageox in the sparse-checkout file passes the check.
func TestCheckLedgerSparseCheckout_WithSageox(t *testing.T) {
	t.Parallel()

	ledgerDir := initBareGitRepo(t)
	sparseDir := filepath.Join(ledgerDir, ".git", "info")
	require.NoError(t, os.MkdirAll(sparseDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(sparseDir, "sparse-checkout"),
		[]byte("/*\n!/*/\n.sageox\nsessions\n"),
		0o644,
	))

	result := checkLedgerSparseCheckoutAtPath(ledgerDir)

	assert.True(t, result.passed, "should pass when .sageox is in sparse-checkout")
	assert.False(t, result.skipped)
}

// TestCheckLedgerSparseCheckout_MissingSageox verifies that a ledger repo
// without .sageox in sparse-checkout fails the check.
func TestCheckLedgerSparseCheckout_MissingSageox(t *testing.T) {
	t.Parallel()

	ledgerDir := initBareGitRepo(t)
	sparseDir := filepath.Join(ledgerDir, ".git", "info")
	require.NoError(t, os.MkdirAll(sparseDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(sparseDir, "sparse-checkout"),
		[]byte("/*\n!/*/\nsessions\n"),
		0o644,
	))

	result := checkLedgerSparseCheckoutAtPath(ledgerDir)

	assert.False(t, result.passed, "should fail when .sageox missing from sparse-checkout")
	assert.False(t, result.skipped)
	assert.NotEmpty(t, result.detail, "should include fix instructions")
}

// TestCheckLedgerSparseCheckout_NoLedger verifies that a missing ledger
// results in a skip, not an error.
func TestCheckLedgerSparseCheckout_NoLedger(t *testing.T) {
	t.Parallel()

	result := checkLedgerSparseCheckoutAtPath("")

	assert.True(t, result.skipped, "should skip when no ledger path provided")
}

// TestCheckCodeDBConsistency_NeverIndexed verifies that when no index
// directory exists and daemon reports never indexed, the check skips.
func TestCheckCodeDBConsistency_NeverIndexed(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	nonExistent := filepath.Join(tmp, "codedb-does-not-exist")

	result := checkCodeDBConsistencyAtDir(nonExistent, nil)

	assert.True(t, result.skipped, "should skip when never indexed")
}

// TestCheckCodeDBConsistency_IndexMissing verifies that when daemon reports
// a previous successful index but the directory is gone, the check fails.
func TestCheckCodeDBConsistency_IndexMissing(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	nonExistent := filepath.Join(tmp, "codedb-gone")

	// simulate daemon reporting a past successful index
	fakeTime := "2025-01-15T10:00:00Z"
	result := checkCodeDBConsistencyWithLastIndexed(nonExistent, fakeTime)

	assert.False(t, result.passed, "should fail when index was built but is now missing")
	assert.False(t, result.skipped)
	assert.Contains(t, result.detail, "ox code index")
}

// initBareGitRepo creates a minimal git repo in a temp dir for testing.
func initBareGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", dir)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git init failed: %s", string(out))
	return dir
}

// checkLedgerSparseCheckoutAtPath is a testable helper that checks
// sparse-checkout at a given ledger path, bypassing config resolution.
func checkLedgerSparseCheckoutAtPath(ledgerPath string) checkResult {
	if ledgerPath == "" {
		return SkippedCheck("Ledger sparse checkout", "no ledger path", "")
	}

	if !isGitRepo(ledgerPath) {
		return SkippedCheck("Ledger sparse checkout", "not a git repo", "")
	}

	sparseFile := filepath.Join(ledgerPath, ".git", "info", "sparse-checkout")
	content, err := os.ReadFile(sparseFile)
	if err != nil {
		return SkippedCheck("Ledger sparse checkout", "sparse-checkout not enabled", "")
	}

	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == ".sageox" || trimmed == ".sageox/" {
			return PassedCheck("Ledger sparse checkout", ".sageox in sparse-checkout cone")
		}
	}

	return FailedCheck("Ledger sparse checkout",
		".sageox missing from sparse-checkout cone",
		"Without .sageox in the cone, codedb cache gets wiped on sparse-checkout set.\n"+
			"        Run `ox doctor` to auto-fix")
}

// checkCodeDBConsistencyAtDir is a testable helper that checks codedb
// consistency at a given data dir, with optional daemon stats.
func checkCodeDBConsistencyAtDir(dataDir string, cs *daemon.CodeDBStats) checkResult {
	indexDirExists := false
	if _, statErr := os.Stat(dataDir); statErr == nil {
		indexDirExists = true
	}

	if cs == nil {
		if !indexDirExists {
			return SkippedCheck("CodeDB consistency", "no index present", "")
		}
		return PassedCheck("CodeDB consistency", "index directory present")
	}

	if cs.LastIndexed.IsZero() && !indexDirExists {
		return SkippedCheck("CodeDB consistency", "never indexed", "")
	}

	if !cs.LastIndexed.IsZero() && !indexDirExists {
		return FailedCheck("CodeDB consistency",
			"index was built but is now missing",
			"codedb was last indexed at "+cs.LastIndexed.Format(time.RFC3339)+
				" but the index directory no longer exists.\n"+
				"        Run `ox code index` to rebuild")
	}

	return PassedCheck("CodeDB consistency", "index present and daemon aware")
}

// checkCodeDBConsistencyWithLastIndexed is a test helper that simulates
// a daemon response with a specific LastIndexed time.
func checkCodeDBConsistencyWithLastIndexed(dataDir, lastIndexedStr string) checkResult {
	t, _ := time.Parse(time.RFC3339, lastIndexedStr)
	cs := &daemon.CodeDBStats{
		LastIndexed: t,
	}
	return checkCodeDBConsistencyAtDir(dataDir, cs)
}
