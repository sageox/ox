package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionStageGuard_Stage covers each way a hydrated artifact can reach the stager. Without the
// per-case guard, any one of them commits raw bytes where a pointer belongs and wedges the Ledger (#1174).
func TestSessionStageGuard_Stage(t *testing.T) {
	project, _ := newSessionCommitProject(t)
	sessionsDir := filepath.Join(project, ".sageox", "sessions")
	guard := newSessionStageGuard(project, sessionsDir)
	content := "{\"role\":\"user\"}\n"
	ref := lfs.NewFileRef([]byte(content))

	// committed pointer at HEAD, then hydrated with the exact bytes it names: restorable without a manifest
	committedID := "2026-10-06T09-00-ann-OxAAAA"
	writeSessionFixture(t, sessionsDir, committedID, lfs.FormatPointer(ref.OID, ref.Size), nil)
	mustRunGit(t, project, "add", "-A")
	mustRunGit(t, project, "commit", "-m", "pointer session")
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, committedID, "raw.jsonl"), []byte(content), 0o644))

	// Storage=git: raw content is the correct state and must stay stageable
	gitID := "2026-10-06T09-30-bea-OxBBBB"
	dir := filepath.Join(sessionsDir, gitID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.md"), []byte("hi!\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"),
		[]byte(`{"title":"git stored","files":{"summary.md":{"storage":"git","size":`+"4"+`}}}`), 0o644))

	// unreadable manifest: nothing vouches for the raw content
	badID := "2026-10-06T10-00-cy-OxCCCC"
	writeSessionFixture(t, sessionsDir, badID, "raw content\n", nil)
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, badID, "meta.json"), []byte("{not json"), 0o644))

	// a deleted tracked artifact has nothing to restore and must still be staged
	require.NoError(t, os.Remove(filepath.Join(project, ".sageox", "sessions", "2026-09-24T10-00-alice-OxAAAA", "meta.json")))

	out, err := guard.stage(context.Background())

	require.NoError(t, err)
	assert.Contains(t, out.Restored, filepath.Join(".sageox", "sessions", committedID, "raw.jsonl"))
	assert.Contains(t, out.Stage, filepath.Join(".sageox", "sessions", gitID, "summary.md"))
	assert.Contains(t, out.Stage, filepath.Join(".sageox", "sessions", "2026-09-24T10-00-alice-OxAAAA", "meta.json"))
	assert.Equal(t, []string{filepath.Join(".sageox", "sessions", badID, "raw.jsonl")}, out.Skipped)

	staged, _ := runIsolatedGit(t, project, "diff", "--cached", "--name-only")
	assert.NotContains(t, staged, badID+"/raw.jsonl")
	assert.Contains(t, staged, gitID+"/summary.md")
	assert.Contains(t, staged, "2026-09-24T10-00-alice-OxAAAA/meta.json", "the deletion is staged")
}

// TestSessionStageGuard_GuardIndex covers content already staged by a broad `git add -A`, which the
// doctor repair writers use. Without it the raw blob is committed or the commit is refused outright.
func TestSessionStageGuard_GuardIndex(t *testing.T) {
	project, _ := newSessionCommitProject(t)
	sessionsDir := filepath.Join(project, ".sageox", "sessions")
	content := "{\"role\":\"user\"}\n"
	ref := lfs.NewFileRef([]byte(content))
	writeSessionFixture(t, sessionsDir, "2026-10-06T11-00-dee-OxDDDD", content, &ref)
	writeSessionFixture(t, sessionsDir, "2026-10-06T12-00-eve-OxEEEE", "no oid\n", nil)
	mustRunGit(t, project, "add", "-A")

	out, err := newSessionStageGuard(project, sessionsDir).guardIndex(context.Background())

	require.NoError(t, err)
	assert.Len(t, out.Restored, 1)
	assert.Len(t, out.Skipped, 1)
	assert.Equal(t, lfs.FormatPointer(ref.OID, ref.Size), ledgerFile(t, project, ":0:.sageox/sessions/2026-10-06T11-00-dee-OxDDDD/raw.jsonl"),
		"the restored pointer is what is staged")
	tracked, _ := runIsolatedGit(t, project, "diff", "--cached", "--name-only")
	assert.NotContains(t, tracked, "OxEEEE/raw.jsonl", "the unknown artifact leaves the index")
	assert.Contains(t, tracked, "OxEEEE/meta.json")
	onDisk, err := os.ReadFile(filepath.Join(sessionsDir, "2026-10-06T12-00-eve-OxEEEE", "raw.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, "no oid\n", string(onDisk), "unstaging never touches content")
}

func TestSessionStageGuard_UnreadableRepoFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	guard := newSessionStageGuard(missing, filepath.Join(missing, "sessions"))

	_, stageErr := guard.stage(context.Background())
	_, indexErr := guard.guardIndex(context.Background())

	assert.Error(t, stageErr)
	assert.Error(t, indexErr)
	assert.Error(t, guard.gitPathspec(context.Background(), []string{"sessions/x"}, "add"))
}

// TestRunSessionCommit_NothingSafeToCommit covers a run where every changed artifact is unvouched raw
// content. The command must say so and create no commit, instead of committing an empty or raw set.
func TestRunSessionCommit_NothingSafeToCommit(t *testing.T) {
	project, _ := newSessionCommitProject(t)
	dir := filepath.Join(project, ".sageox", "sessions", "2026-10-06T13-00-fay-OxFFFF")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte("no oid\n"), 0o644))
	before, _ := runIsolatedGit(t, project, "rev-parse", "HEAD")

	require.NoError(t, runSessionCommit(sessionCommitCmd, nil))

	after, _ := runIsolatedGit(t, project, "rev-parse", "HEAD")
	assert.Equal(t, before, after)
}

// TestFixSessionUncommitted_GuardsHydratedArtifacts covers the doctor recovery commit, which used to run
// `git add sessions/` and a bare commit. Without the guard it re-creates the wedge it exists to prevent.
func TestFixSessionUncommitted_GuardsHydratedArtifacts(t *testing.T) {
	ledger := newWedgedLedger(t, false)
	mustRunGit(t, ledger, "reset", "--hard", "origin/main")
	own := lfs.NewFileRef([]byte(ownContent))
	writeSessionFixture(t, filepath.Join(ledger, "sessions"), "2026-10-06T11-00-ryan-OxRRRR", ownContent, &own)
	writeSessionFixture(t, filepath.Join(ledger, "sessions"), "2026-10-06T12-00-cy-OxUUUU", unknownContent2, nil)

	result := fixSessionUncommitted(ledger, 4)

	assert.True(t, result.passed, "%s %s", result.message, result.detail)
	assert.Equal(t, lfs.FormatPointer(own.OID, own.Size), ledgerFile(t, ledger, "HEAD:sessions/2026-10-06T11-00-ryan-OxRRRR/raw.jsonl"))
	tracked, _ := runIsolatedGit(t, ledger, "ls-tree", "-r", "--name-only", "HEAD")
	assert.NotContains(t, tracked, "OxUUUU/raw.jsonl")
	assert.Contains(t, tracked, "OxUUUU/meta.json")
}
