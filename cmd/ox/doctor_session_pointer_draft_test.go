package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const draftName = "2026-10-03T05-19-ryan-Oxbz5x"

var draftArtifacts = map[string]string{
	"raw.jsonl":  "{\"role\":\"user\",\"who\":\"draft recording\"}\n",
	"session.md": "# draft session\n",
	"summary.md": "# draft summary\n",
}

// commitDraftArtifacts commits raw artifacts inside a draft session directory, the shape #1174's sweep
// left behind. A draft directory must hold only meta.json (.claude/rules/cache-only-design.md).
func commitDraftArtifacts(t *testing.T, ledger string, draftName string) {
	t.Helper()
	dir := filepath.Join(ledger, "sessions", draftName)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
		Version: "1.0", SessionName: draftName, Username: "ryan", AgentID: "OxAgent", AgentType: "claude-code",
		CreatedAt: time.Date(2026, 10, 3, 5, 19, 0, 0, time.UTC), Draft: true,
	}))
	for name, content := range draftArtifacts {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	mustRunGit(t, ledger, "add", "-A")
	mustRunGit(t, ledger, "commit", "-m", "draft artifacts swept in")
}

// TestRestoreUnpushedSessionPointers_UntracksDraftArtifacts covers raw artifacts committed inside a draft
// session directory. A draft cannot record them in meta.json and must never track them, so the repair
// removes them from the tree, keeps their bytes in the cache, and leaves meta.json alone. Without it the
// push validator refuses forever and no pointer can legally be written.
func TestRestoreUnpushedSessionPointers_UntracksDraftArtifacts(t *testing.T) {
	ctx := context.Background()
	ledger := newWedgedLedger(t, false)
	commitDraftArtifacts(t, ledger, draftName)
	metaBefore, err := os.ReadFile(filepath.Join(ledger, "sessions", draftName, "meta.json"))
	require.NoError(t, err)
	require.Error(t, lfs.ValidateUnpushedTip(ctx, ledger, "origin/main"))

	report, err := restoreUnpushedSessionPointers(ctx, ledger, true, newLFSStub(t).uploader("ryan"))

	require.NoError(t, err)
	assert.Empty(t, report.Unrepairable)
	assert.Len(t, report.Untracked, 3)
	assert.NoError(t, report.Remaining)
	tree, _ := runIsolatedGit(t, ledger, "ls-tree", "-r", "--name-only", "HEAD", "sessions/"+draftName)
	assert.Equal(t, "sessions/"+draftName+"/meta.json", tree, "the draft keeps only meta.json")
	metaAfter, err := os.ReadFile(filepath.Join(ledger, "sessions", draftName, "meta.json"))
	require.NoError(t, err)
	assert.Equal(t, metaBefore, metaAfter, "meta.json is untouched")
	for name, content := range draftArtifacts {
		cached, err := os.ReadFile(filepath.Join(ledger, ".sageox", "cache", "sessions", draftName, name))
		require.NoError(t, err, "the bytes of %s must survive in the cache", name)
		assert.Equal(t, content, string(cached))
	}
	subject, _ := runIsolatedGit(t, ledger, "log", "-1", "--format=%s")
	assert.Contains(t, subject, "doctor: restore LFS pointers")
	assert.Contains(t, subject, "untrack draft artifacts")
	mustRunGit(t, ledger, "push", "origin", "main")
	assert.Empty(t, mustGitStatus(t, ledger, "sessions/"+draftName), "no stray files are left in the draft directory")
}

// TestRestoreUnpushedSessionPointers_DraftCacheConflictFailsClosed covers a cache that already holds
// different bytes for a draft artifact. Untracking it would leave the committed bytes with no copy
// outside git history, so it stays tracked and is reported while the others are untracked.
func TestRestoreUnpushedSessionPointers_DraftCacheConflictFailsClosed(t *testing.T) {
	ledger := newWedgedLedger(t, false)
	commitDraftArtifacts(t, ledger, draftName)
	cache := filepath.Join(ledger, ".sageox", "cache", "sessions", draftName, "raw.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(cache), 0o755))
	require.NoError(t, os.WriteFile(cache, []byte("a different recording\n"), 0o600))

	report, err := restoreUnpushedSessionPointers(context.Background(), ledger, true, nil)

	require.NoError(t, err)
	require.Len(t, report.Unrepairable, 1)
	assert.Equal(t, "sessions/"+draftName+"/raw.jsonl", report.Unrepairable[0].Path)
	assert.Contains(t, report.Unrepairable[0].Reason, "differs")
	assert.Len(t, report.Untracked, 2)
	assert.Equal(t, draftArtifacts["raw.jsonl"], ledgerFile(t, ledger, "HEAD:sessions/"+draftName+"/raw.jsonl"))
	kept, _ := os.ReadFile(cache)
	assert.Equal(t, "a different recording\n", string(kept))
}

// TestRestoreUnpushedSessionPointers_BulkUntrackPassesSacredGuard covers a repair that removes more
// files than the sacred mass-delete guard allows in one commit. The bytes are in the cache, so the
// repair must go through, and it must not leave the override set for the rest of the process.
func TestRestoreUnpushedSessionPointers_BulkUntrackPassesSacredGuard(t *testing.T) {
	ledger := newWedgedLedger(t, false)
	for _, name := range []string{"2026-10-03T05-19-ryan-OxAAAA", "2026-10-03T06-19-ryan-OxBBBB"} {
		commitDraftArtifacts(t, ledger, name)
	}
	_, hadOverride := os.LookupEnv("OX_ALLOW_SACRED_MASS_DELETE")

	report, err := restoreUnpushedSessionPointers(context.Background(), ledger, true, nil)

	require.NoError(t, err)
	assert.Len(t, report.Untracked, 6)
	assert.True(t, report.Committed)
	assert.NoError(t, report.Remaining)
	_, stillSet := os.LookupEnv("OX_ALLOW_SACRED_MASS_DELETE")
	assert.Equal(t, hadOverride, stillSet, "the override must not outlive the commit")
}

// TestSessionStageGuard_RefusesDraftArtifacts covers the prevention: no automatic writer may stage a
// non-meta.json file inside a draft directory, and the refusal is one WARN per file.
func TestSessionStageGuard_RefusesDraftArtifacts(t *testing.T) {
	project, _ := newSessionCommitProject(t)
	sessionsDir := filepath.Join(project, ".sageox", "sessions")
	dir := filepath.Join(sessionsDir, draftName)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
		Version: "1.0", SessionName: draftName, Username: "ryan", AgentID: "OxAgent", AgentType: "claude-code",
		CreatedAt: time.Date(2026, 10, 3, 5, 19, 0, 0, time.UTC), Draft: true,
	}))
	for name, content := range draftArtifacts {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	out, err := newSessionStageGuard(project, sessionsDir).stage(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(".sageox", "sessions", draftName, "meta.json")}, out.Stage)
	assert.Len(t, out.Skipped, 3)
	for name := range draftArtifacts {
		assert.Equal(t, 1, strings.Count(logs.String(), draftName+"/"+name), "one WARN names %s", name)
	}
	staged, _ := runIsolatedGit(t, project, "diff", "--cached", "--name-only")
	assert.Equal(t, ".sageox/sessions/"+draftName+"/meta.json", staged)
}
