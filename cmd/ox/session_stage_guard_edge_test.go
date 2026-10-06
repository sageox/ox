package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionStageGuard_EdgeCases covers inputs the guard must refuse to rewrite or stage blindly:
// a restore whose cache copy cannot be kept, a tracked raw blob that is no pointer, and non-regular files.
func TestSessionStageGuard_EdgeCases(t *testing.T) {
	project, _ := newSessionCommitProject(t)
	sessionsDir := filepath.Join(project, ".sageox", "sessions")
	guard := newSessionStageGuard(project, sessionsDir)
	content := "{\"role\":\"user\"}\n"
	ref := lfs.NewFileRef([]byte(content))

	// manifest names the content, but the cache location is blocked: the file must stay hydrated and unstaged
	blockedID := "2026-10-06T15-00-hal-OxHHHH"
	writeSessionFixture(t, sessionsDir, blockedID, content, &ref)
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".sageox", "cache"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".sageox", "cache", "sessions"), []byte("a file, not a dir"), 0o644))

	// raw committed earlier (no pointer at HEAD), then edited: nothing vouches for the new bytes
	trackedID := "2026-10-06T16-00-ivy-OxIIII"
	writeSessionFixture(t, sessionsDir, trackedID, "committed raw\n", nil)
	mustRunGit(t, project, "add", "-A")
	mustRunGit(t, project, "commit", "-m", "raw already committed")
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, trackedID, "raw.jsonl"), []byte("edited raw\n"), 0o644))

	// a directory where an artifact is expected is not content and is passed through to git untouched
	dirID := "2026-10-06T17-00-jo-OxJJJJ"
	require.NoError(t, os.MkdirAll(filepath.Join(sessionsDir, dirID, "raw.jsonl", "inner"), 0o755))

	out := guard.check([]string{
		filepath.Join(".sageox", "sessions", blockedID, "raw.jsonl"),
		filepath.Join(".sageox", "sessions", trackedID, "raw.jsonl"),
		filepath.Join(".sageox", "sessions", dirID, "raw.jsonl"),
		filepath.Join("..", "outside.jsonl"),
	})

	assert.ElementsMatch(t, []string{
		filepath.Join(".sageox", "sessions", blockedID, "raw.jsonl"),
		filepath.Join(".sageox", "sessions", trackedID, "raw.jsonl"),
	}, out.Skipped)
	assert.Len(t, out.Stage, 2, "the directory and the out-of-tree path are not this guard's business")
	assert.Len(t, out.Skipped, 2)
	onDisk, _ := os.ReadFile(filepath.Join(sessionsDir, blockedID, "raw.jsonl"))
	assert.Equal(t, content, string(onDisk), "a failed restore leaves the hydrated file as it was")
}
