package agentwork

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Interrupted metadata promotion must leave completed cache content discoverable.
func TestSessionFinalize_InterruptedDraftPromotionRemainsRetryable(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git and local LFS service")
	}
	bare, ledgerPath := setupBareAndCloneLedger(t)
	const name = "2026-10-21T18-06-test-OxRETRY"
	cacheDir := writeProductionShapedSession(t, ledgerPath, name)
	target := filepath.Join(ledgerPath, "sessions", name)
	writeDraftMeta(t, target)
	draft, err := lfs.ReadSessionMeta(target)
	require.NoError(t, err)
	draft.RepoID = "repo-preserved"
	require.NoError(t, lfs.WriteSessionMetaOnly(target, draft))
	runGitCmd(t, ledgerPath, "add", "--sparse", "sessions")
	runGitCmd(t, ledgerPath, "commit", "-m", "session draft")
	runGitCmd(t, ledgerPath, "push", "origin", "HEAD")
	before := gitOutput(t, bare, "rev-parse", "HEAD")
	draftBytes, err := os.ReadFile(filepath.Join(target, "meta.json"))
	require.NoError(t, err)
	payload := &SessionFinalizePayload{SessionDir: cacheDir, RawPath: filepath.Join(cacheDir, "raw.jsonl"), LedgerPath: ledgerPath}
	stored, err := session.ReadSessionFromPath(payload.RawPath)
	require.NoError(t, err)
	handler := newGitBackedHandler()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler.SetDaemonContext(ctx)

	require.NoError(t, gitutil.WithRepoLock(context.Background(), ledgerPath, func() error {
		require.NoError(t, handler.preserveLedgerMetaInCache(payload))
		// Cancel at the boundary between the identity seed and the next metadata
		// write. The held lock makes that write fail without a timeout or sleep.
		return fileutil.WithFileLock(context.Background(), filepath.Join(cacheDir, "meta.json"), func() error {
			cancel()
			_, err := handler.writeMetaAndUploadLFS(payload, stored, &session.SummarizeResponse{})
			require.ErrorIs(t, err, context.Canceled)
			return nil
		})
	}))
	assert.Equal(t, before, gitOutput(t, ledgerPath, "rev-parse", "HEAD"))
	assert.Equal(t, before, gitOutput(t, bare, "rev-parse", "HEAD"))
	currentDraft, err := os.ReadFile(filepath.Join(target, "meta.json"))
	require.NoError(t, err)
	assert.Equal(t, draftBytes, currentDraft)
	raw, err := os.ReadFile(payload.RawPath)
	require.NoError(t, err)
	assert.Equal(t, testRawContent, string(raw))

	retry := newGitBackedHandler()
	enableLocalFinalizeLFS(t, retry, ledgerPath)
	pending, err := retry.Detect(ledgerPath)
	require.NoError(t, err)
	require.Len(t, pending, 1, "completed content must survive a fresh detector")
	require.True(t, pending[0].Payload.(*SessionFinalizePayload).UploadOnly)
	require.NoError(t, retry.ProcessResult(pending[0], &RunResult{}))
	assert.NoDirExists(t, cacheDir)
	assert.Equal(t, gitOutput(t, ledgerPath, "rev-parse", "HEAD"), gitOutput(t, bare, "rev-parse", "HEAD"))
	var published lfs.SessionMeta
	require.NoError(t, json.Unmarshal([]byte(gitOutput(t, bare, "show", "HEAD:sessions/"+name+"/meta.json")), &published))
	assert.False(t, published.IsDraft())
	assert.Equal(t, draftTestSessionID, published.SessionID)
	assert.Equal(t, draft.RepoID, published.RepoID)
	assert.Equal(t, lfs.NewFileRef(raw), published.Files["raw.jsonl"])
}

// A canceled finalizer waiting behind GC must not write artifacts or metadata
// into the clone that GC is about to replace.
func TestSessionFinalize_PreparationWaitsForRepoLock(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repository")
	}
	_, ledgerPath := setupBareAndCloneLedger(t)
	cacheDir := writeProductionShapedSession(t, ledgerPath, "2026-10-21T18-06-test-OxLOCK")
	for _, name := range requiredArtifacts {
		require.NoError(t, os.Remove(filepath.Join(cacheDir, name)))
	}
	handler := newGitBackedHandler()
	enableLocalFinalizeLFS(t, handler, ledgerPath)
	// Skip git probes so a canceled pre-write subprocess cannot stand in for
	// the mutation lock. Without that lock, artifact generation still runs.
	handler.skipGit = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler.SetDaemonContext(ctx)
	item := &WorkItem{Type: sessionFinalizeType, Payload: &SessionFinalizePayload{
		SessionDir: cacheDir, RawPath: filepath.Join(cacheDir, "raw.jsonl"), LedgerPath: ledgerPath, Missing: requiredArtifacts,
	}}
	require.NoError(t, gitutil.WithRepoLock(context.Background(), ledgerPath, func() error {
		// Cancellation is the synchronization barrier; no timing assumptions or
		// live daemon are needed to exercise the occupied-lock failure path.
		cancel()
		err := handler.ProcessResult(item, &RunResult{Output: preserveSuccessOutput})
		assert.Error(t, err, "waiting for the mutation lock must fail before preparation")
		for _, name := range append(append([]string{}, requiredArtifacts...), "meta.json") {
			assert.NoFileExists(t, filepath.Join(cacheDir, name), "preparation ran without the repo lock")
		}
		return nil
	}))
	assert.FileExists(t, filepath.Join(cacheDir, "raw.jsonl"))
}

// Preparation errors must reach the retry manager without replacing published
// metadata or pruning the source. Repairing the cause must allow a later pass.
func TestSessionFinalize_PreparationFailurePreservesSessionUntilRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git and local LFS service")
	}
	for _, tc := range []struct {
		name, fault, wantErr string
		uploadOnly           bool
	}{
		{name: "missing transcript", fault: "read", wantErr: "read session for export"},
		{name: "blocked artifact write", fault: "artifact", wantErr: "write summary.json"},
		{name: "corrupt published metadata", fault: "metadata", wantErr: "read published session metadata"},
		{name: "active rebase regular", fault: "rebase", wantErr: "broken rebase state"},
		{name: "active rebase upload-only", fault: "rebase", wantErr: "broken rebase state", uploadOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bare, ledgerPath := setupBareAndCloneLedger(t)
			const name = "2026-01-15T15-00-test-OxPREP"
			cacheDir := writeProductionShapedSession(t, ledgerPath, name)
			if !tc.uploadOnly {
				for _, artifact := range requiredArtifacts {
					require.NoError(t, os.Remove(filepath.Join(cacheDir, artifact)))
				}
			}
			target := filepath.Join(ledgerPath, "sessions", name)
			writeDraftMeta(t, target)
			metaPath := filepath.Join(target, "meta.json")
			originalMeta, err := os.ReadFile(metaPath)
			require.NoError(t, err)
			runGitCmd(t, ledgerPath, "add", "--sparse", "sessions")
			runGitCmd(t, ledgerPath, "commit", "-m", "session draft")
			runGitCmd(t, ledgerPath, "push", "origin", "HEAD")
			before := gitOutput(t, ledgerPath, "rev-parse", "HEAD")
			rawPath := filepath.Join(cacheDir, "raw.jsonl")
			preservedRaw := rawPath
			var repair func()
			switch tc.fault {
			case "read":
				preservedRaw = rawPath + ".saved"
				require.NoError(t, os.Rename(rawPath, preservedRaw))
				repair = func() { require.NoError(t, os.Rename(preservedRaw, rawPath)) }
			case "artifact":
				blockedPath := filepath.Join(cacheDir, "summary.json")
				require.NoError(t, os.Mkdir(blockedPath, 0o755))
				repair = func() { require.NoError(t, os.Remove(blockedPath)) }
			case "metadata":
				// Its own metadata marks this cache as pending work, rather than
				// a read-only download that correctly skips an unreadable target.
				cacheMeta, err := lfs.ReadSessionMeta(target)
				require.NoError(t, err)
				cacheMeta.ClearDraft()
				require.NoError(t, lfs.WriteSessionMetaOnly(cacheDir, cacheMeta))
				require.NoError(t, os.WriteFile(metaPath, []byte(`{"draft":`), 0o644))
				repair = func() { require.NoError(t, os.WriteFile(metaPath, originalMeta, 0o644)) }
			case "rebase":
				rebaseDir := filepath.Join(ledgerPath, ".git", "rebase-merge")
				require.NoError(t, os.Mkdir(rebaseDir, 0o755))
				repair = func() { require.NoError(t, os.Remove(rebaseDir)) }
			}
			beforeMeta, err := os.ReadFile(metaPath)
			require.NoError(t, err)
			beforeStatus := gitOutput(t, ledgerPath, "status", "--porcelain", "--untracked-files=all", "--", "sessions")
			handler := newGitBackedHandler()
			enableLocalFinalizeLFS(t, handler, ledgerPath)
			item := &WorkItem{Type: sessionFinalizeType, Payload: &SessionFinalizePayload{
				SessionDir: cacheDir, RawPath: rawPath, LedgerPath: ledgerPath, UploadOnly: tc.uploadOnly, Missing: requiredArtifacts,
			}}
			require.ErrorContains(t, handler.ProcessResult(item, &RunResult{Output: preserveSuccessOutput}), tc.wantErr)
			assert.Equal(t, before, gitOutput(t, ledgerPath, "rev-parse", "HEAD"))
			assert.Equal(t, before, gitOutput(t, bare, "rev-parse", "HEAD"))
			assert.Equal(t, beforeStatus, gitOutput(t, ledgerPath, "status", "--porcelain", "--untracked-files=all", "--", "sessions"))
			afterMeta, err := os.ReadFile(metaPath)
			require.NoError(t, err)
			assert.Equal(t, beforeMeta, afterMeta)
			raw, err := os.ReadFile(preservedRaw)
			require.NoError(t, err, "preparation must preserve the source cache")
			assert.Equal(t, testRawContent, string(raw))

			repair()
			pending, err := handler.Detect(ledgerPath)
			require.NoError(t, err)
			require.Len(t, pending, 1, "a repaired preparation failure must remain retryable")
			require.NoError(t, handler.ProcessResult(pending[0], &RunResult{Output: preserveSuccessOutput}))
			assert.NoDirExists(t, cacheDir)
			assert.Equal(t, gitOutput(t, ledgerPath, "rev-parse", "HEAD"), gitOutput(t, bare, "rev-parse", "HEAD"))
			published, err := lfs.ReadSessionMeta(target)
			require.NoError(t, err)
			assert.False(t, published.IsDraft())
			assert.Equal(t, draftTestSessionID, published.SessionID)
		})
	}
}

// Upload-only recovery must promote existing cache drafts without losing identity or fields.
func TestSessionFinalize_UploadOnlyPromotesDraftMetadata(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git and local LFS service")
	}
	bare, ledgerPath := setupBareAndCloneLedger(t)
	const name = "2026-10-21T18-06-test-OxDRAFT"
	cacheDir := writeProductionShapedSession(t, ledgerPath, name)
	target := filepath.Join(ledgerPath, "sessions", name)
	writeDraftMeta(t, target)
	meta, err := lfs.ReadSessionMeta(target)
	require.NoError(t, err)
	meta.RepoID, meta.UserID, meta.Model = "repo-preserved", "user-preserved", "model-preserved"
	require.NoError(t, lfs.WriteSessionMetaOnly(cacheDir, meta))
	runGitCmd(t, ledgerPath, "add", "--sparse", "sessions")
	runGitCmd(t, ledgerPath, "commit", "-m", "session draft")
	runGitCmd(t, ledgerPath, "push", "origin", "HEAD")
	handler := newGitBackedHandler()
	enableLocalFinalizeLFS(t, handler, ledgerPath)
	item := &WorkItem{Type: sessionFinalizeType, Payload: &SessionFinalizePayload{
		SessionDir: cacheDir, RawPath: filepath.Join(cacheDir, "raw.jsonl"), LedgerPath: ledgerPath, UploadOnly: true,
	}}
	require.NoError(t, handler.ProcessResult(item, &RunResult{}))
	var published lfs.SessionMeta
	require.NoError(t, json.Unmarshal([]byte(gitOutput(t, bare, "show", "HEAD:sessions/"+name+"/meta.json")), &published))
	assert.False(t, published.IsDraft())
	assert.Equal(t, draftTestSessionID, published.SessionID)
	assert.Equal(t, meta.RepoID, published.RepoID)
	assert.Equal(t, meta.UserID, published.UserID)
	assert.Equal(t, meta.Model, published.Model)
	assert.NotEmpty(t, published.Files["raw.jsonl"].OID)
	assert.NoDirExists(t, cacheDir)
}

// A non-cooperating writer can still restore the old metadata in the index.
// Check the actual publication snapshot, including an identical retry, before
// accepting success and deleting the source cache.
func TestSessionFinalize_RejectsRestoredDraftSnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git and local LFS service")
	}
	for _, existingArtifacts := range []bool{false, true} {
		t.Run(map[bool]string{false: "new artifacts", true: "identical retry"}[existingArtifacts], func(t *testing.T) {
			bare, ledgerPath := setupBareAndCloneLedger(t)
			const name = "2026-01-01T12-00-test-OxSNAP"
			cacheDir := writeProductionShapedSession(t, ledgerPath, name)
			target := filepath.Join(ledgerPath, "sessions", name)
			writeDraftMeta(t, target)
			if existingArtifacts {
				for _, filename := range append([]string{"raw.jsonl"}, requiredArtifacts...) {
					data, err := os.ReadFile(filepath.Join(cacheDir, filename))
					require.NoError(t, err)
					if filename != "summary.json" {
						ref := lfs.NewFileRef(data)
						data = []byte(lfs.FormatPointer(ref.OID, ref.Size))
					}
					require.NoError(t, os.WriteFile(filepath.Join(target, filename), data, 0o644))
				}
			}
			require.NoError(t, lfs.EnsureSessionsGitignore(filepath.Dir(target)))
			runGitCmd(t, ledgerPath, "add", "--sparse", "sessions")
			runGitCmd(t, ledgerPath, "commit", "-m", "historical session placeholder")
			runGitCmd(t, ledgerPath, "push", "origin", "HEAD")
			before := gitOutput(t, ledgerPath, "rev-parse", "HEAD")
			handler := newGitBackedHandler()
			enableLocalFinalizeLFS(t, handler, ledgerPath)
			handler.afterStageTestHook = func() {
				runGitCmd(t, ledgerPath, "restore", "--source=HEAD", "--staged", "--worktree", "--", "sessions/"+name+"/meta.json")
			}
			item := &WorkItem{Type: sessionFinalizeType, Payload: &SessionFinalizePayload{
				SessionDir: cacheDir, RawPath: filepath.Join(cacheDir, "raw.jsonl"), LedgerPath: ledgerPath, UploadOnly: true,
			}}
			err := handler.ProcessResult(item, &RunResult{})
			require.ErrorContains(t, err, "draft metadata")
			assert.Equal(t, before, gitOutput(t, ledgerPath, "rev-parse", "HEAD"))
			assert.Equal(t, before, gitOutput(t, bare, "rev-parse", "HEAD"))
			data, err := os.ReadFile(filepath.Join(cacheDir, "raw.jsonl"))
			require.NoError(t, err)
			assert.Equal(t, testRawContent, string(data))
			if existingArtifacts {
				assert.Empty(t, gitOutput(t, ledgerPath, "diff", "--cached", "--name-only"), "fixture must exercise the no-op commit branch")
			}
		})
	}
}
