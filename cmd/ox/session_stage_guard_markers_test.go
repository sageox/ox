package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionStageGuard_RefusesConflictMarkers pins that no guarded writer can stage a session file
// carrying an autostash-pop conflict block: git add would commit it as resolved (#1055, #1189).
func TestSessionStageGuard_RefusesConflictMarkers(t *testing.T) {
	project, _ := newSessionCommitProject(t)
	sessionsDir := filepath.Join(project, ".sageox", "sessions")
	guard := newSessionStageGuard(project, sessionsDir)

	markedID := "2026-10-06T09-00-ann-OxMMMM"
	cleanID := "2026-10-06T09-30-bea-OxNNNN"
	for id, meta := range map[string]string{
		markedID: "{\n<<<<<<< Updated upstream\n\"title\":\"a\"\n=======\n\"title\":\"b\"\n>>>>>>> Stashed changes\n}\n",
		cleanID:  `{"title":"clean"}` + "\n",
	} {
		require.NoError(t, os.MkdirAll(filepath.Join(sessionsDir, id), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, id, "meta.json"), []byte(meta), 0o644))
	}

	out, err := guard.stage(context.Background())

	require.NoError(t, err)
	assert.Contains(t, out.Skipped, filepath.Join(".sageox", "sessions", markedID, "meta.json"))
	assert.Contains(t, out.Stage, filepath.Join(".sageox", "sessions", cleanID, "meta.json"), "negative control: a clean meta.json still stages")
	staged, _ := runIsolatedGit(t, project, "diff", "--cached", "--name-only")
	assert.NotContains(t, staged, markedID)
	assert.Contains(t, staged, cleanID)
}
