package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGitLockFilesResult(t *testing.T) {
	t.Parallel()

	now := time.Now()
	tests := []struct {
		name        string
		subdir      string                   // gitDir path segment; exercises quoting
		locks       map[string]time.Duration // lock name -> age
		wantPassed  bool
		wantWarning bool
	}{
		{name: "no locks passes", wantPassed: true},
		{name: "fresh lock warns", locks: map[string]time.Duration{"index.lock": time.Minute}, wantPassed: true, wantWarning: true},
		{name: "hour-old lock fails", locks: map[string]time.Duration{"index.lock": 2 * time.Hour}},
		{name: "path with a space stays one rm target", subdir: "my repo", locks: map[string]time.Duration{"index.lock": 2 * time.Hour}},
		{name: "path with a quote is escaped", subdir: "it's", locks: map[string]time.Duration{"HEAD.lock": 2 * time.Hour}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gitDir := filepath.Join(t.TempDir(), tt.subdir, ".git")
			require.NoError(t, os.MkdirAll(gitDir, 0o755))
			for lock, age := range tt.locks {
				p := filepath.Join(gitDir, lock)
				require.NoError(t, os.WriteFile(p, nil, 0o644))
				require.NoError(t, os.Chtimes(p, now.Add(-age), now.Add(-age)))
			}
			got := gitLockFilesResult("ledger git locks", gitDir, now)
			require.Equal(t, tt.wantPassed, got.passed)
			require.Equal(t, tt.wantWarning, got.warning)
			for lock := range tt.locks {
				// exact, shell-quoted path — never a one-element brace
				// expansion the shell won't expand, never split on a space
				want := "rm -- " + shellQuote(filepath.Join(gitDir, lock))
				require.Contains(t, got.detail, want)
			}
		})
	}
}

func TestLedgerGitLockFilesResult_InspectionErrors(t *testing.T) {
	t.Parallel()
	now := time.Now()

	notCloned := filepath.Join(t.TempDir(), "missing", ".git")

	fileNotDir := filepath.Join(t.TempDir(), ".git")
	require.NoError(t, os.WriteFile(fileNotDir, []byte("gitdir: elsewhere"), 0o644))

	cloned := filepath.Join(t.TempDir(), ".git")
	require.NoError(t, os.MkdirAll(cloned, 0o755))

	tests := []struct {
		name       string
		gitDir     string
		wantSkip   bool
		wantPassed bool
	}{
		{name: "missing ledger is skipped", gitDir: notCloned, wantSkip: true},
		{name: ".git file is skipped", gitDir: fileNotDir, wantSkip: true},
		{name: "cloned ledger is inspected", gitDir: cloned, wantPassed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ledgerGitLockFilesResult("ledger git locks", tt.gitDir, now)
			require.Equal(t, tt.wantSkip, got.skipped)
			require.Equal(t, tt.wantPassed, got.passed)
		})
	}

	t.Run("unreadable parent reports failure, not skip", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("needs POSIX permissions and a non-root user")
		}
		parent := t.TempDir()
		gitDir := filepath.Join(parent, "ledger", ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0o755))
		locked := filepath.Join(parent, "ledger")
		require.NoError(t, os.Chmod(locked, 0o000))
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

		got := ledgerGitLockFilesResult("ledger git locks", gitDir, now)
		require.False(t, got.skipped, "a permission error must not read as 'ledger not cloned'")
		require.False(t, got.passed)
		require.True(t, strings.Contains(got.detail, "permission denied"), "detail=%q", got.detail)
	})
}

// TestCheckLedgerGitLockFiles_LooksInTheLedger verifies `ox doctor` inspects
// the LEDGER's .git, not only the project repo's. Failure prevented: a stale
// ledger index.lock blocking every plan commit while doctor reported the
// project repo clean.
func TestCheckLedgerGitLockFiles_LooksInTheLedger(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	if got := checkLedgerGitLockFiles(); !got.skipped {
		t.Fatalf("uncloned ledger: got %+v, want skipped", got)
	}

	ledger := initPlanTestLedger(t, root)
	require.True(t, checkLedgerGitLockFiles().passed, "a clean ledger passes")

	lock := filepath.Join(ledger, ".git", "index.lock")
	require.NoError(t, os.WriteFile(lock, nil, 0o644))
	old := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(lock, old, old))
	got := checkLedgerGitLockFiles()
	require.False(t, got.passed, "a stale ledger lock must fail the check")
	require.Contains(t, got.detail, shellQuote(lock))
}
