package main

import (
	"bytes"
	"encoding/json"
	"io"
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

// runCodeStatus stands up a git project wired to a shared CodeDB dir, lets the
// caller seed and then break that dir, and runs the real `ox code status`.
// Running the real command is the point: the defect being guarded is what a
// caller reads off this output, not what a helper returns.
//
// Returns the human-readable text; when jsonMode is set the parsed `--json`
// envelope is returned instead and the text is empty. The human path prints
// through fmt.Print, so os.Stdout is what has to be captured.
func runCodeStatus(t *testing.T, jsonMode bool, seed func(dataDir string)) (map[string]any, string) {
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
	cmd.Flags().Bool("json", jsonMode, "")
	cmd.SetOut(&buf)

	stdout := os.Stdout
	r, w, pipeErr := os.Pipe()
	require.NoError(t, pipeErr)
	os.Stdout = w
	// Drain concurrently: a full pipe buffer would deadlock the command mid-print.
	drained := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		drained <- string(b)
	}()
	runErr := cmd.RunE(cmd, nil)
	require.NoError(t, w.Close())
	os.Stdout = stdout
	human := <-drained
	require.NoError(t, runErr)

	if !jsonMode {
		return nil, human
	}
	var out map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out), "output: %s", buf.String())
	return out, ""
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
	out, _ := runCodeStatus(t, true, func(dataDir string) {
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
	out, _ := runCodeStatus(t, true, func(dataDir string) {
		// Truncate to a schema-less file: readable, openable, and carrying none
		// of the tables status counts — the shape of an index ox cannot use.
		require.NoError(t, os.WriteFile(filepath.Join(dataDir, store.MetadataDBFile), nil, 0o600))
		requireChmodDeniesWrites(t, dataDir)
	})

	require.Equal(t, true, out["index_exists"])
	require.NotEmpty(t, out["open_error"],
		"an index that could not be opened must not read as an empty one: %v", out)
}

// The JSON envelope is not the only surface a coworker reads. Human output must
// carry the same two facts, and carry them where the eye lands first — on the
// Status line, not buried under counts that look healthy.
func TestCodeStatusHuman_LabelsReadOnlyAndUnreadable(t *testing.T) {
	t.Run("read-only index says so and points at the consequence", func(t *testing.T) {
		_, human := runCodeStatus(t, false, func(dataDir string) {
			requireChmodDeniesWrites(t, dataDir)
		})
		require.Contains(t, human, "read-only")
		require.Contains(t, human, "ox code index")
		require.NotContains(t, human, "unreadable")
	})

	t.Run("unreadable index reports the reason, not an empty one", func(t *testing.T) {
		_, human := runCodeStatus(t, false, func(dataDir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dataDir, store.MetadataDBFile), nil, 0o600))
			requireChmodDeniesWrites(t, dataDir)
		})
		require.Contains(t, human, "unreadable")
		require.Contains(t, human, "read-only")
		// "empty index" is the state this must never be confused with.
		require.NotContains(t, human, "empty index")
	})

	t.Run("absent index still reads as not indexed", func(t *testing.T) {
		_, human := runCodeStatus(t, false, func(dataDir string) {
			require.NoError(t, os.RemoveAll(dataDir))
		})
		require.Contains(t, human, "not indexed")
	})
}
