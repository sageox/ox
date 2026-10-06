package lfs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// countingGitOnPath puts a git shim first on PATH that appends every
// invocation's subcommand to a log, so a test can count subprocesses.
func countingGitOnPath(t *testing.T) (logPath string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	shimDir := t.TempDir()
	logPath = filepath.Join(shimDir, "calls.log")
	shim := fmt.Sprintf("#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in cat-file) echo cat-file >> %q;; esac; done\nexec %q \"$@\"\n", logPath, realGit)
	require.NoError(t, os.WriteFile(filepath.Join(shimDir, "git"), []byte(shim), 0o755))
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func catFileCalls(t *testing.T, logPath string) int {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(t, err)
	return strings.Count(string(data), "cat-file")
}

// A long unpushed history of plain-git data files must not cost one cat-file
// per blob: only session artifacts at the tip need inspection.
func TestValidateUnpushedTip_LongHistoryOfDataFilesIsCheap(t *testing.T) {
	ledger, _ := initLedgerWithRemote(t)

	const commits = 300
	for i := 0; i < commits; i++ {
		rel := filepath.Join("data", "github", "2026", "09", fmt.Sprintf("pr-%d.json", i))
		require.NoError(t, os.MkdirAll(filepath.Join(ledger, filepath.Dir(rel)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(ledger, rel), []byte(fmt.Sprintf(`{"n":%d}`, i)), 0o644))
		git(t, ledger, "add", rel)
		git(t, ledger, "commit", "-q", "-m", fmt.Sprintf("data %d", i), "--no-verify")
	}
	// raw session artifact at the tip where an LFS pointer belongs
	require.NoError(t, os.MkdirAll(filepath.Join(ledger, "sessions", "s1"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ledger, "sessions", "s1", "raw.jsonl"), []byte("raw transcript, not a pointer\n"), 0o644))
	git(t, ledger, "add", "sessions/s1/raw.jsonl")
	git(t, ledger, "commit", "-q", "-m", "session", "--no-verify")

	upstream := git(t, ledger, "rev-parse", "--abbrev-ref", "@{upstream}")
	logPath := countingGitOnPath(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	err := ValidateUnpushedTip(ctx, ledger, upstream)
	elapsed := time.Since(start)

	require.Error(t, err, "raw session artifact at the tip must still be rejected")
	require.Contains(t, err.Error(), "sessions/s1/raw.jsonl")
	calls := catFileCalls(t, logPath)
	t.Logf("cat-file calls=%d elapsed=%s", calls, elapsed)
	require.LessOrEqual(t, calls, 2, "validator must inspect only session artifacts, not every unpushed data blob")
}
