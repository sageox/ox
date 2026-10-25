package store

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/testguard"
)

// TestReadBlob_GuardedOpen_ReadsBlobsAndPreservesConfig proves blob_reader opens
// source checkouts via gitopen.GuardedPlainOpen. Two guarantees:
//  1. blobs are still readable through the guarded/wrapped storer — the guard
//     must not break reads;
//  2. reading a blob from a source checkout never rewrites its .git/config —
//     the #819 invariant, at the blob_reader call site specifically.
//
// Reverting blob_reader.go to a raw git.PlainOpen on a writing go-git version
// would fail guarantee (2); a guard that broke object reads would fail (1).
func TestReadBlob_GuardedOpen_ReadsBlobsAndPreservesConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("short: builds a real git repository")
	}

	repoDir := t.TempDir()
	// MinimalEnv (allowlist) strips GIT_DIR/GIT_WORK_TREE/GIT_COMMON_DIR so an
	// inherited routing var can't retarget git outside repoDir.
	env := testguard.MinimalEnv([]string{
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@sageox.ai",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@sageox.ai",
	})
	runGit := func(args ...string) string {
		t.Helper()
		// Disable signing so the test is independent of the developer's global
		// git config (MinimalEnv's clean env can't reach the signing agent).
		full := append([]string{"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)
		cmd := exec.Command("git", full...)
		cmd.Dir = repoDir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	runGit("init", "-b", "main")
	content := "package sample\n\nfunc Sample() {}\n"
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "sample.go"), []byte(content), 0o644))
	runGit("add", "sample.go")
	runGit("commit", "-m", "init")
	blobHash := runGit("rev-parse", "HEAD:sample.go")

	// Give the checkout config the #819 shape (a git-crypt filter with quoted
	// values that a lossy re-marshal would re-quote) and snapshot it.
	cfgPath := filepath.Join(repoDir, ".git", "config")
	base, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfgPath, []byte(string(base)+
		"[filter \"git-crypt\"]\n\tsmudge = \"git-crypt\" smudge\n"), 0o644))
	snap, err := os.ReadFile(cfgPath)
	require.NoError(t, err)

	s, err := Open(t.TempDir())
	require.NoError(t, err)
	defer s.Close()
	_, err = s.Exec("INSERT INTO repos (name, path) VALUES (?, ?)", "sample", repoDir)
	require.NoError(t, err)

	got := s.ReadBlob(blobHash)
	assert.Equal(t, content, string(got),
		"ReadBlob must return the committed blob content through the guarded open")

	after, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, string(snap), string(after),
		"ReadBlob must not rewrite the source repo's .git/config (#819)")
}
