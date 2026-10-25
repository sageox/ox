package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/codedb/store"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// TestIndexInProcess_AfterSelfHeal_RepopulatesCode is the command-altitude
// regression for the init-container crash-loop's silent-data-loss twin: when
// codedb.Open self-heals a corrupt bleve sub-index (emptying it and writing a
// .needs_reindex marker mid-open), a one-shot in-process `ox index` must
// escalate to a full rebuild. Otherwise incremental indexing skips the commits
// already recorded in SQLite and code search stays permanently empty — with no
// error to signal it.
//
// Failure prevented: `ox index` "succeeding" against a mid-write-corrupted
// cache while leaving code search empty (worse than the crash-loop, because it
// is invisible). Drives the real command path, not a store helper in isolation.
func TestIndexInProcess_AfterSelfHeal_RepopulatesCode(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git + real code indexing")
	}

	repoDir := newIndexTestRepo(t)
	t.Chdir(repoDir)

	// Resolve the cache dir exactly as indexCodeInProcess does (findGitRoot
	// returns the symlink-resolved toplevel on macOS, so recomputing from the
	// raw temp path could point at a different directory).
	gitRoot := findGitRoot()
	require.NotEmpty(t, gitRoot, "git root must resolve")
	dataDir := resolveCodeDBDir(gitRoot)
	require.Truef(t, strings.HasPrefix(dataDir, gitRoot),
		"test must use a repo-local cache (%s), refusing to touch a shared cache", dataDir)

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	// 1. Baseline: build the cache and prove code search has content.
	require.NoError(t, indexCodeInProcess(cmd, nil, false), "baseline index")
	require.Greater(t, codeIndexDocCount(t, dataDir), uint64(0),
		"baseline code index must have documents")

	// 2. Simulate a kill-9 mid-write: tear the code sub-index bolt so bbolt
	// cannot open it at all — the exact unopenable-bolt class from the incident.
	boltPath := filepath.Join(dataDir, "bleve", "code", "store", "root.bolt")
	require.FileExists(t, boltPath)
	require.NoError(t, os.WriteFile(boltPath, []byte("corrupted"), 0o600))

	// 3. Re-run incrementally. Open self-heals the code sub-index (empty +
	// marker); the in-process path must notice the marker and rebuild fully.
	require.NoError(t, indexCodeInProcess(cmd, nil, false), "reindex after corruption")

	// 4. Code search must be repopulated, and the marker cleared by the wipe.
	require.Greater(t, codeIndexDocCount(t, dataDir), uint64(0),
		"code index must be repopulated after self-heal, not left silently empty")
	require.Empty(t, store.NeedsReindexMarkers(dataDir),
		"full rebuild must clear the self-heal marker")
}

// codeIndexDocCount opens the store at dataDir and returns the code sub-index
// document count — the observable proxy for "code search returns results".
func codeIndexDocCount(t *testing.T, dataDir string) uint64 {
	t.Helper()
	s, err := store.Open(dataDir)
	require.NoError(t, err)
	defer func() { _ = s.Close() }()
	n, err := s.CodeIndex.DocCount()
	require.NoError(t, err)
	return n
}

// newIndexTestRepo creates a throwaway git repo with one indexable Go file.
func newIndexTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command(args[0], args[1:]...)
		c.Dir = dir
		out, err := c.CombinedOutput()
		require.NoErrorf(t, err, "%v: %s", args, out)
	}
	run("git", "init")
	run("git", "config", "user.name", "Test User")
	run("git", "config", "user.email", "test@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package main\n\nfunc ZebrafishMarker() string { return \"hello\" }\n"), 0o644))
	run("git", "add", "main.go")
	run("git", "commit", "-m", "initial commit")
	return dir
}
