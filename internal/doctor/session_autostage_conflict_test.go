package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func autoStageGit(t *testing.T, dir string, args ...string) error {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	return cmd.Run()
}

func writeAutoStageFile(t *testing.T, ledger, rel, content string) {
	t.Helper()
	path := filepath.Join(ledger, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// TestSessionAutoStage_StagesNothingWhileUnmerged covers auto-stage on a stash-pop conflict.
// Without it, `git add sessions/` marks the conflict resolved with its markers still in the file (#1055).
func TestSessionAutoStage_StagesNothingWhileUnmerged(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git stash-pop conflict")
	}
	ledger := t.TempDir()
	meta := "sessions/2026-09-24T10-00-alice-OxAAAA/meta.json"
	require.NoError(t, autoStageGit(t, ledger, "init", "--initial-branch=main"))
	writeAutoStageFile(t, ledger, meta, "{\n  \"title\": \"base\"\n}\n")
	require.NoError(t, autoStageGit(t, ledger, "add", "sessions/"))
	require.NoError(t, autoStageGit(t, ledger, "commit", "-m", "base"))

	writeAutoStageFile(t, ledger, meta, "{\n  \"title\": \"local\"\n}\n")
	require.NoError(t, autoStageGit(t, ledger, "stash", "push", "-m", "autostash"))
	writeAutoStageFile(t, ledger, meta, "{\n  \"title\": \"upstream\"\n}\n")
	require.NoError(t, autoStageGit(t, ledger, "commit", "-am", "upstream"))
	require.Error(t, autoStageGit(t, ledger, "stash", "pop"), "stash pop must conflict")
	writeAutoStageFile(t, ledger, "sessions/2026-09-24T11-00-bob-OxBBBB/raw.jsonl", "{}\n")

	require.Empty(t, (&SessionAutoStageCheck{}).findUnstagedSessionFiles(ledger),
		"nothing may be staged while a session file is unmerged")
}
