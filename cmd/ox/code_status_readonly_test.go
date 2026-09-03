package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/paths"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// codeStatusJSON stands up a git project wired to a shared CodeDB dir, lets the
// caller seed and then break that dir, and returns the parsed `ox code status
// --json` envelope. Running the real command is the point: the defect being
// guarded is what a caller reads off this JSON, not what a helper returns.
func codeStatusJSON(t *testing.T, seed func(dataDir string)) map[string]any {
	t.Helper()

	projectRoot := t.TempDir()
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	for _, args := range [][]string{{"init"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "T"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = projectRoot
		require.NoError(t, cmd.Run(), "git %v", args)
	}

	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0o755))
	cfg, err := json.Marshal(map[string]string{"repo_id": "repo_01abc123", "endpoint": "https://sageox.ai"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), cfg, 0o644))

	dataDir := paths.CodeDBSharedDir("repo_01abc123", "https://sageox.ai")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	db, err := codedb.Open(dataDir)
	require.NoError(t, err)
	_, err = db.Store().Exec(`INSERT INTO repos (id, name, path) VALUES (1, 'demo', '/tmp/demo')`)
	require.NoError(t, err)
	_, err = db.Store().Exec(`INSERT INTO commits (id, repo_id, hash, author, message, timestamp) VALUES (1, 1, 'abc', 'a', 'm', 0)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	seed(dataDir)

	// chdir last: FindRepoRoot and the daemon socket both resolve from cwd, so
	// this is what keeps the test off any daemon running for the real repo.
	t.Chdir(projectRoot)

	var buf bytes.Buffer
	cmd := &cobra.Command{Use: codeStatusCmd.Use, RunE: codeStatusCmd.RunE}
	cmd.Flags().Bool("json", true, "")
	cmd.SetOut(&buf)
	require.NoError(t, cmd.RunE(cmd, nil))

	var out map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out), "output: %s", buf.String())
	return out
}

func requireChmodDeniesWrites(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("chmod does not make a directory unwritable on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod does not deny writes")
	}
	out, err := exec.Command("chmod", "-R", "a-w", dir).CombinedOutput()
	require.NoError(t, err, "chmod: %s", out)
	t.Cleanup(func() { _ = exec.Command("chmod", "-R", "u+w", dir).Run() })
}

// An index served from read-only media is real data, and status must both count
// it and say where it came from — a caller that sees read_only knows `ox code
// index` will refuse before it tries.
func TestCodeStatusJSON_ReadOnlyIndexIsCountedAndLabeled(t *testing.T) {
	out := codeStatusJSON(t, func(dataDir string) {
		requireChmodDeniesWrites(t, dataDir)
	})

	require.Equal(t, true, out["index_exists"])
	require.Equal(t, true, out["read_only"], "read-only index must be labeled: %v", out)
	require.Equal(t, float64(1), out["commits"], "read-only index must still be counted: %v", out)
	require.Nil(t, out["open_error"])
}

// The reported defect (#871): an index status could not open reported zeros
// under index_exists: true — byte-identical to a warm index holding nothing.
// An agent reads that as "nothing here", searches, finds nothing, and says so.
func TestCodeStatusJSON_UnreadableIndexIsNotReportedAsEmpty(t *testing.T) {
	out := codeStatusJSON(t, func(dataDir string) {
		// Truncate to a schema-less file: readable, openable, and carrying none
		// of the tables status counts — the shape of an index ox cannot use.
		require.NoError(t, os.WriteFile(filepath.Join(dataDir, store.MetadataDBFile), nil, 0o600))
		requireChmodDeniesWrites(t, dataDir)
	})

	require.Equal(t, true, out["index_exists"])
	require.NotEmpty(t, out["open_error"],
		"an index that could not be opened must not read as an empty one: %v", out)
}
