package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustNewSecretRedactor() *session.Redactor {
	return session.NewRedactor()
}

func gitCmd(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	return exec.Command("git", args...)
}

// makeLedgerWithCommitAndOrigin sets up a synthetic ledger with origin/main
// established and a second commit on the local tree that hasn't been pushed.
// Returns the ledger working tree.
func makeLedgerWithCommitAndOrigin(t *testing.T, files map[string]string) string {
	t.Helper()
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "bare.git")
	work := filepath.Join(tmp, "work")
	require.NoError(t, os.MkdirAll(bare, 0755))
	require.NoError(t, os.MkdirAll(work, 0755))

	mustGit(t, bare, "init", "--bare", "--initial-branch=main")
	mustGit(t, work, "init", "--initial-branch=main")
	mustGit(t, work, "config", "user.email", "test@example.com")
	mustGit(t, work, "config", "user.name", "test")
	mustGit(t, work, "remote", "add", "origin", bare)

	// commit something benign on main + push (this is the "pushed history" baseline)
	require.NoError(t, os.WriteFile(filepath.Join(work, "README"), []byte("clean baseline"), 0644))
	mustGit(t, work, "add", "README")
	mustGit(t, work, "commit", "-m", "baseline")
	mustGit(t, work, "push", "-u", "origin", "main")

	// second commit with the test files — NOT pushed, this is the work to scrub
	for rel, content := range files {
		abs := filepath.Join(work, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0755))
		require.NoError(t, os.WriteFile(abs, []byte(content), 0644))
	}
	mustGit(t, work, "add", ".")
	mustGit(t, work, "commit", "-m", "session: test")
	return work
}

// TestSnapshotLedger_HashesContent verifies the snapshot captures every
// regular file and produces a deterministic hash for identical content.
func TestSnapshotLedger_HashesContent(t *testing.T) {
	work := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(work, "a.txt"), []byte("alpha"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(work, "b.txt"), []byte("beta"), 0644))

	out1, hash1, err := snapshotLedger(work, t.TempDir())
	require.NoError(t, err)
	require.NotEmpty(t, hash1)

	// snapshot file exists and is 0400
	info, err := os.Stat(out1)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0400), info.Mode().Perm())

	// snapshot content includes both files
	f, err := os.Open(out1)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	gz, err := gzip.NewReader(f)
	require.NoError(t, err)
	tr := tar.NewReader(gz)
	names := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		names[hdr.Name] = true
	}
	assert.True(t, names["a.txt"])
	assert.True(t, names["b.txt"])

	// re-snapshot with identical content → identical hash
	_, hash2, err := snapshotLedger(work, t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, hash1, hash2, "deterministic content hash")
}

// TestRedactFileInPlace_PreservesCleanLines verifies that only matched
// lines get redacted; everything else passes through unchanged.
func TestRedactFileInPlace_PreservesCleanLines(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "file.jsonl")
	original := strings.Join([]string{
		`{"text":"hello"}`,
		`{"text":"AKIAIOSFODNN7EXAMPLE"}`,
		`{"text":"clean line"}`,
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(target, []byte(original), 0644))

	r := mustNewSecretRedactor()
	require.NoError(t, redactFileInPlace(target, r))

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	out := string(got)

	assert.Contains(t, out, `{"text":"hello"}`)
	assert.Contains(t, out, `{"text":"clean line"}`)
	assert.NotContains(t, out, "AKIA", "AWS canary must be redacted out")
	assert.Contains(t, out, "[REDACTED_AWS_KEY]")
}

// TestClassifyFindingsByPushStatus_SplitsCorrectly verifies the
// pushed-vs-unpushed gate works: pre-existing committed-and-pushed
// content is flagged, fresh working-tree changes are not.
func TestClassifyFindingsByPushStatus_SplitsCorrectly(t *testing.T) {
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "bare.git")
	work := filepath.Join(tmp, "work")
	require.NoError(t, os.MkdirAll(bare, 0755))
	require.NoError(t, os.MkdirAll(work, 0755))
	mustGit(t, bare, "init", "--bare", "--initial-branch=main")
	mustGit(t, work, "init", "--initial-branch=main")
	mustGit(t, work, "config", "user.email", "test@example.com")
	mustGit(t, work, "config", "user.name", "test")
	mustGit(t, work, "remote", "add", "origin", bare)

	// Pushed: file with a canary in the very first commit, then push.
	require.NoError(t, os.MkdirAll(filepath.Join(work, "sessions"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(work, "sessions/old.jsonl"),
		[]byte("AKIAIOSFODNN7EXAMPLE\n"), 0644))
	mustGit(t, work, "add", ".")
	mustGit(t, work, "commit", "-m", "old session")
	mustGit(t, work, "push", "-u", "origin", "main")

	// Unpushed: a second file with another canary, committed but not pushed.
	require.NoError(t, os.WriteFile(filepath.Join(work, "sessions/new.jsonl"),
		[]byte("AKIAIOSFODNN7EXAMPLE\n"), 0644))
	mustGit(t, work, "add", "sessions/new.jsonl")
	mustGit(t, work, "commit", "-m", "new session")

	findings := []redactHistoryFinding{
		{Detector: "aws_access_key", Path: "sessions/old.jsonl", Line: 1},
		{Detector: "aws_access_key", Path: "sessions/new.jsonl", Line: 1},
	}
	pushed, unpushed, err := classifyFindingsByPushStatus(work, findings)
	require.NoError(t, err)
	assert.Len(t, pushed, 1, "old.jsonl should be classified as pushed")
	assert.Equal(t, "sessions/old.jsonl", pushed[0].Path)
	assert.Len(t, unpushed, 1, "new.jsonl should be classified as unpushed")
	assert.Equal(t, "sessions/new.jsonl", unpushed[0].Path)
}

// TestRunRedactHistoryWorkflow_DryRunListsButDoesntModify is the load-
// bearing test that --dry-run is truly inert. Fixtures use the
// session-scoped layout enforced by ox-zukx (scan iterates
// sessions/<name>/, not loose files at sessions/ root).
func TestRunRedactHistoryWorkflow_DryRunListsButDoesntModify(t *testing.T) {
	work := makeLedgerWithCommitAndOrigin(t, map[string]string{
		"sessions/leaky/raw.jsonl": "AKIAIOSFODNN7EXAMPLE\n",
	})
	leakPath := filepath.Join(work, "sessions/leaky/raw.jsonl")
	originalBytes, err := os.ReadFile(leakPath)
	require.NoError(t, err)
	backupDir := t.TempDir()
	out := &bytes.Buffer{}

	err = runRedactHistoryWorkflow(redactHistoryOptions{
		LedgerPath: work,
		DryRun:     true,
		BackupDir:  backupDir,
		Stdin:      strings.NewReader(""),
		Stdout:     out,
	})
	require.NoError(t, err)

	// file must be unchanged
	after, err := os.ReadFile(leakPath)
	require.NoError(t, err)
	assert.Equal(t, originalBytes, after, "dry-run must not modify files")

	// backup directory must remain empty
	entries, err := os.ReadDir(backupDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "dry-run must not create snapshots")

	// output must mention the finding and the path:line
	assert.Contains(t, out.String(), "aws_access_key")
	assert.Contains(t, out.String(), "sessions/leaky/raw.jsonl:1")
	// output must NOT leak the secret bytes
	assert.NotContains(t, out.String(), "AKIAIOSFODNN7EXAMPLE")
}

// TestRunRedactHistoryWorkflow_InteractiveYesRedacts verifies that
// answering "y" to the prompt actually scrubs the file and amends the
// commit, while preserving the canary in the snapshot. Also exercises
// the ox-8bfh path that appends a RedactionPass to meta.json — the
// fixture seeds a minimum-valid meta.json so MutateSessionMeta has
// something to mutate.
func TestRunRedactHistoryWorkflow_InteractiveYesRedacts(t *testing.T) {
	work := makeLedgerWithCommitAndOrigin(t, map[string]string{
		"sessions/leaky/raw.jsonl": "AKIAIOSFODNN7EXAMPLE\n",
		"sessions/leaky/meta.json": minimalMetaJSON("leaky", "OxLEAK"),
	})
	backupDir := t.TempDir()
	out := &bytes.Buffer{}

	err := runRedactHistoryWorkflow(redactHistoryOptions{
		LedgerPath: work,
		BackupDir:  backupDir,
		Stdin:      strings.NewReader("y\n"),
		Stdout:     out,
	})
	require.NoError(t, err)

	// the working-tree file must be scrubbed
	after, err := os.ReadFile(filepath.Join(work, "sessions/leaky/raw.jsonl"))
	require.NoError(t, err)
	assert.NotContains(t, string(after), "AKIA", "file must be redacted in place")
	assert.Contains(t, string(after), "[REDACTED_AWS_KEY]")

	// the snapshot must still contain the original bytes
	entries, err := os.ReadDir(backupDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "exactly one snapshot expected")
	snapPath := filepath.Join(backupDir, entries[0].Name())
	canaryInSnap := snapshotContainsBytes(t, snapPath, "AKIAIOSFODNN7EXAMPLE")
	assert.True(t, canaryInSnap, "snapshot must contain the pre-redaction bytes for recovery")

	// the amend should have happened — HEAD commit message is unchanged
	cmd := mustGitOutput(t, work, "log", "-1", "--format=%s")
	assert.Equal(t, "session: test", strings.TrimSpace(cmd))
}

// TestRunRedactHistoryWorkflow_NoSkipsLeavesFileUnchanged verifies that
// declining the prompt (answering "n") preserves the file as-is. Also
// asserts snapshot still happened — the safety rail fires whether or
// not the operator approves the change.
func TestRunRedactHistoryWorkflow_NoSkipsLeavesFileUnchanged(t *testing.T) {
	work := makeLedgerWithCommitAndOrigin(t, map[string]string{
		"sessions/leaky/raw.jsonl": "AKIAIOSFODNN7EXAMPLE\n",
	})
	leakPath := filepath.Join(work, "sessions/leaky/raw.jsonl")
	originalBytes, err := os.ReadFile(leakPath)
	require.NoError(t, err)
	backupDir := t.TempDir()
	out := &bytes.Buffer{}

	err = runRedactHistoryWorkflow(redactHistoryOptions{
		LedgerPath: work,
		BackupDir:  backupDir,
		Stdin:      strings.NewReader("n\n"),
		Stdout:     out,
	})
	require.NoError(t, err)

	after, err := os.ReadFile(leakPath)
	require.NoError(t, err)
	assert.Equal(t, originalBytes, after, "declining prompt must leave file unchanged")

	entries, err := os.ReadDir(backupDir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "snapshot fires even on decline; user still has recovery")
}

// TestRunRedactHistoryWorkflow_QuitAbortsWithoutWritingMore verifies that
// "q" stops the loop before processing subsequent files.
func TestRunRedactHistoryWorkflow_QuitAbortsWithoutWritingMore(t *testing.T) {
	work := makeLedgerWithCommitAndOrigin(t, map[string]string{
		"sessions/sess-a/raw.jsonl": "AKIAIOSFODNN7EXAMPLE\n",
		"sessions/sess-b/raw.jsonl": "AKIAIOSFODNN7EXAMPLE\n",
	})
	bPath := filepath.Join(work, "sessions/sess-b/raw.jsonl")
	originalB, err := os.ReadFile(bPath)
	require.NoError(t, err)
	out := &bytes.Buffer{}

	err = runRedactHistoryWorkflow(redactHistoryOptions{
		LedgerPath: work,
		BackupDir:  t.TempDir(),
		Stdin:      strings.NewReader("q\n"),
		Stdout:     out,
	})
	require.NoError(t, err)

	// sess-b raw.jsonl must be unchanged (we quit before reaching it OR after the first prompt)
	stillThere, err := os.ReadFile(bPath)
	require.NoError(t, err)
	assert.Equal(t, originalB, stillThere)
	assert.Contains(t, out.String(), "Aborting")
}

// minimalMetaJSON returns a JSON string for a meta.json that just clears
// the lfs.SessionMeta.Validate() invariants. Used by tests that exercise
// the real-write path (ox-8bfh appends a RedactionPass via
// MutateSessionMeta, which insists on an existing meta.json). Fields
// kept tiny on purpose so a future Validate() addition won't silently
// break — the test will fail loudly and prompt an explicit update here.
func minimalMetaJSON(sessionName, agentID string) string {
	return `{
  "version": "1.0",
  "session_name": "` + sessionName + `",
  "username": "test-user",
  "agent_id": "` + agentID + `",
  "agent_type": "claude-code",
  "created_at": "2026-05-11T00:00:00Z",
  "files": {}
}
`
}

// mustGitOutput runs git -C dir args... and returns stdout.
func mustGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	allArgs := append([]string{"-C", dir}, args...)
	out, err := gitCmd(t, allArgs...).Output()
	require.NoError(t, err)
	return string(out)
}

// snapshotContainsBytes returns true if the tar.gz at path contains the
// given byte substring in any file's content.
func snapshotContainsBytes(t *testing.T, path, needle string) bool {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	gz, err := gzip.NewReader(f)
	require.NoError(t, err)
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return false
		}
		if !hdr.FileInfo().Mode().IsRegular() {
			continue
		}
		buf, err := io.ReadAll(tr)
		if err != nil {
			return false
		}
		if strings.Contains(string(buf), needle) {
			return true
		}
	}
	return false
}
