package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/sessionhistory"

	"github.com/sageox/ox/pkg/codexhistory"
	"github.com/stretchr/testify/require"
)

func historyFixture(t *testing.T, home, dir, id, cwd, source, text string, now time.Time) string {
	t.Helper()
	p := filepath.Join(home, dir, id+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0700))
	header, _ := json.Marshal(map[string]any{"timestamp": now.Add(-time.Hour).Format(time.RFC3339Nano), "type": "session_meta", "payload": map[string]any{"id": id, "cwd": cwd, "source": source}})
	entry, _ := json.Marshal(map[string]any{"timestamp": now.Add(-time.Hour).Format(time.RFC3339Nano), "type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": text}}}})
	require.NoError(t, os.WriteFile(p, append(append(header, '\n'), append(entry, '\n')...), 0600))
	require.NoError(t, os.Chtimes(p, now.Add(-time.Hour), now.Add(-time.Hour)))
	return p
}
func importTestGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}
func TestSessionImportPreviewRepositoryBoundariesAndNoMutations(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	ledger := t.TempDir()
	worktree := filepath.Join(t.TempDir(), "worktree")
	other := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	importTestGit(t, root, "init")
	importTestGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	importTestGit(t, root, "worktree", "add", "--detach", worktree, "HEAD")
	importTestGit(t, other, "init")
	now := time.Now()
	ids := []string{"01a0a62a-f6d2-7a62-9213-fd142782db91", "01a0a62a-f6d2-7a62-9213-fd142782db92", "01a0a62a-f6d2-7a62-9213-fd142782db93", "01a0a62a-f6d2-7a62-9213-fd142782db94"}
	p := historyFixture(t, home, "sessions/2020/01/01", ids[0], root, "cli", "hello", now)
	historyFixture(t, home, "archived_sessions", ids[1], worktree, "cli", "archived", now)
	historyFixture(t, home, "sessions", ids[2], other, "cli", "unrelated", now)
	historyFixture(t, home, "sessions", ids[3], root, "subagent", "worker", now)
	before, err := os.ReadFile(p)
	require.NoError(t, err)
	report, err := scanImport(context.Background(), root, ledger, importDestination{RepoID: "repo_test"}, &importOptions{}, now)
	require.NoError(t, err)
	require.Len(t, report.Candidates, 2)
	for _, c := range report.Candidates {
		require.Equal(t, "uncertain", c.Status, "absence of receipts never proves legacy eligibility")
	}
	after, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, before, after)
	files, err := os.ReadDir(ledger)
	require.NoError(t, err)
	require.Empty(t, files, "preview must not create a cache, receipt, lock, or journal")
	importTestGit(t, root, "diff", "--exit-code")
}
func TestSessionImportPreviewRedactsBeforeDisplayingTitle(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	ledger := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	importTestGit(t, root, "init")
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox"), 0700))
	policy := filepath.Join(root, ".sageox", "REDACT.md")
	require.NoError(t, os.WriteFile(policy, []byte("```redact\nliteral \"Project Falcon\" -> [REDACTED_CODENAME]\n```\n"), 0600))
	now := time.Now()
	historyFixture(t, home, "sessions", "01a0a62a-f6d2-7a62-9213-fd142782db91", root, "cli", "Project Falcon", now)
	report, err := scanImport(context.Background(), root, ledger, importDestination{}, &importOptions{}, now)
	require.NoError(t, err)
	require.Len(t, report.Candidates, 1)
	require.NotContains(t, report.Candidates[0].Title, "Project Falcon")
	require.Equal(t, 1, report.Candidates[0].RedactedEntries)
	require.NoError(t, os.WriteFile(policy, []byte("```redact\nregex \"[\" -> hidden\n```\n"), 0600))
	_, err = scanImport(context.Background(), root, ledger, importDestination{}, &importOptions{}, now)
	require.ErrorContains(t, err, "invalid redaction policy")
}
func TestSessionImportSnapshotIdentity(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	p := historyFixture(t, home, "sessions", "01a0a62a-f6d2-7a62-9213-fd142782db91", "/repo", "cli", "first", now)
	first, err := codexhistory.Stream(context.Background(), p, nil)
	require.NoError(t, err)
	content, err := os.ReadFile(p)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p, []byte(strings.Replace(string(content), "first", "other", 1)), 0600))
	second, err := codexhistory.Stream(context.Background(), p, nil)
	require.NoError(t, err)
	require.Equal(t, first.Generation, second.Generation, "resumed sessions share their header generation")
	require.NotEqual(t, first.Digest, second.Digest)
	require.NotEqual(t, importedName("repo", first), importedName("repo", second))
}

type failedImportStreamAdapter struct{ sessionhistory.Adapter }

func (a failedImportStreamAdapter) Stream(context.Context, string, func(adapterprotocol.RawEntry) error) (sessionhistory.Snapshot, error) {
	return sessionhistory.Snapshot{}, fmt.Errorf("source disappeared after inspection")
}
func TestSessionImportReportsStreamFailureUsingInspectedIdentity(t *testing.T) {
	root, home, ledger := t.TempDir(), t.TempDir(), t.TempDir()
	importTestGit(t, root, "init")
	t.Setenv("CODEX_HOME", home)
	id := "019c6d2e-27b0-798d-aaed-b036114dc63a"
	historyFixture(t, home, "sessions", id, root, "cli", "hello", time.Now())
	original := codexImportAdapter
	codexImportAdapter = failedImportStreamAdapter{original}
	t.Cleanup(func() { codexImportAdapter = original })
	report, err := scanImport(context.Background(), root, ledger, importDestination{}, &importOptions{IDs: []string{id}}, time.Now())
	require.NoError(t, err)
	require.Len(t, report.Candidates, 1)
	require.Equal(t, id, report.Candidates[0].NativeID)
	require.Equal(t, "failed", report.Candidates[0].Status)
	require.Contains(t, report.Candidates[0].Reason, "disappeared")
	files, err := os.ReadDir(ledger)
	require.NoError(t, err)
	require.Empty(t, files)
}
