package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/require"
)

func TestFindDoubleEncodedLFSPointerPaths(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")

	inner := []byte(lfs.FormatPointer("sha256:inner", 9914))
	outer := lfs.FormatPointer("sha256:outer", int64(len(inner)))
	path := filepath.Join(repo, "frame.jpg")
	require.NoError(t, os.WriteFile(path, []byte(outer), 0o644))
	run("add", "frame.jpg")
	run("commit", "-q", "-m", "outer pointer")
	require.NoError(t, os.WriteFile(path, inner, 0o644))

	paths, err := findDoubleEncodedLFSPointerPaths(repo)
	require.NoError(t, err)
	require.Equal(t, []string{"frame.jpg"}, paths)

	require.NoError(t, restoreRawLFSPointers(repo, paths))
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, []byte(outer), got)
}
