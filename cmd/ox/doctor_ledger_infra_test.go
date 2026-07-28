package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeSparseCheckout writes a ledger's .git/info/sparse-checkout file.
func writeSparseCheckout(t *testing.T, ledgerDir, content string) {
	t.Helper()
	sparseDir := filepath.Join(ledgerDir, ".git", "info")
	require.NoError(t, os.MkdirAll(sparseDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sparseDir, "sparse-checkout"), []byte(content), 0o644))
}

// TestCheckLedgerSparseCheckout_WithSageox verifies that a ledger repo
// carrying every required cone entry passes the check.
func TestCheckLedgerSparseCheckout_WithSageox(t *testing.T) {
	t.Parallel()

	ledgerDir := initBareGitRepo(t)
	writeSparseCheckout(t, ledgerDir, "/*\n!/*/\n.sageox\nsessions\ndata/plans\n")

	result := checkLedgerSparseCheckoutAtPath(ledgerDir, false)

	assert.True(t, result.passed, "should pass when every required dir is in sparse-checkout")
	assert.False(t, result.skipped)
}

// TestCheckLedgerSparseCheckout_ConeModeSlashFormat is the regression for the
// format the check actually meets in the wild. `git sparse-checkout set` in
// cone mode writes entries as "/.sageox/" — slashes on both sides — so the
// original exact-string comparison against ".sageox" never matched a single
// real ledger, and the check reported a false failure on every one of them.
func TestCheckLedgerSparseCheckout_ConeModeSlashFormat(t *testing.T) {
	t.Parallel()

	ledgerDir := initBareGitRepo(t)
	writeSparseCheckout(t, ledgerDir,
		"/*\n!/*/\n/data/\n!/data/*/\n/data/plans/\n/.sageox/\n/.sync/\n")

	result := checkLedgerSparseCheckoutAtPath(ledgerDir, false)

	assert.True(t, result.passed, "cone-mode entries like /.sageox/ must satisfy the check")
}

// TestCheckLedgerSparseCheckout_MissingDataPlans covers the cone every existing
// machine has baked in: correct in every respect except data/plans, which git
// then deletes from the working tree on the sync scheduler's next refresh.
func TestCheckLedgerSparseCheckout_MissingDataPlans(t *testing.T) {
	t.Parallel()

	ledgerDir := initBareGitRepo(t)
	writeSparseCheckout(t, ledgerDir, "/*\n!/*/\n/.sageox/\n/.sync/\n/sessions/\n")

	result := checkLedgerSparseCheckoutAtPath(ledgerDir, false)

	assert.False(t, result.passed, "should fail when data/plans is missing")
	assert.Contains(t, result.detail, "data/plans", "detail should name the missing dir and its impact")
}

// TestMissingSparseConeDirs covers the pure matcher directly — the surrounding
// check is mostly path resolution.
func TestMissingSparseConeDirs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{"bare names", ".sageox\ndata/plans\n", nil},
		{"cone-mode slashes", "/.sageox/\n/data/plans/\n", nil},
		{"empty file", "", []string{".sageox", "data/plans"}},
		{"only sageox", "/.sageox/\n", []string{"data/plans"}},
		{"only plans", "/data/plans/\n", []string{".sageox"}},
		{
			// A negation line must not be read as the directory it mentions.
			"negation lines are not matches",
			"/*\n!/*/\n/data/\n!/data/*/\n",
			[]string{".sageox", "data/plans"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, missingSparseConeDirs(tt.content))
		})
	}
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

	result := checkLedgerSparseCheckoutAtPath(ledgerDir, false)

	assert.False(t, result.passed, "should fail when .sageox missing from sparse-checkout")
	assert.False(t, result.skipped)
	assert.NotEmpty(t, result.detail, "should include fix instructions")
}

// TestCheckLedgerSparseCheckout_NoLedger verifies that a missing ledger
// results in a skip, not an error.
func TestCheckLedgerSparseCheckout_NoLedger(t *testing.T) {
	t.Parallel()

	result := checkLedgerSparseCheckoutAtPath("", false)

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
