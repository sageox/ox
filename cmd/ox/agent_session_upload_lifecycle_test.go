package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sessionUploadFixture struct {
	projectRoot string
	ledgerPath  string
	sessionName string
	rawContent  []byte
	result      *agentSessionResult
	state       *session.RecordingState
	refs        map[string]lfs.FileRef
}

func (f sessionUploadFixture) orphan() orphanedSession {
	return orphanedSession{
		SessionName: f.sessionName,
		CachePath:   f.state.SessionPath,
		Meta: &session.StoreMeta{
			SessionID: f.state.SessionID,
			AgentID:   f.state.AgentID,
			AgentType: f.state.AdapterName,
			Username:  "testuser",
			CreatedAt: f.state.StartedAt,
		},
		EntryCount: f.result.EntryCount,
	}
}

func newSessionUploadFixture(t *testing.T) sessionUploadFixture {
	t.Helper()
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	projectRoot := t.TempDir()
	ledgerPath := t.TempDir()
	cachePath := filepath.Join(t.TempDir(), "2026-09-01T03-00-testuser-OxLifecycle")
	require.NoError(t, os.MkdirAll(cachePath, 0o755))
	rawContent := []byte("{\"type\":\"header\",\"metadata\":{\"agent_id\":\"OxLifecycle\"}}\n" +
		"{\"type\":\"user\",\"content\":\"preserve me\"}\n" +
		"{\"type\":\"assistant\",\"content\":\"preserved\"}\n" +
		"{\"type\":\"footer\",\"entry_count\":2}\n")
	rawPath := filepath.Join(cachePath, ledgerFileRaw)
	require.NoError(t, os.WriteFile(rawPath, rawContent, 0o600))

	sessionName := filepath.Base(cachePath)
	return sessionUploadFixture{
		projectRoot: projectRoot,
		ledgerPath:  ledgerPath,
		sessionName: sessionName,
		rawContent:  rawContent,
		result: &agentSessionResult{
			RawPath:     rawPath,
			EntryCount:  2,
			SessionName: sessionName,
			Summary:     "A durable local summary",
		},
		state: &session.RecordingState{
			AgentID:     "OxLifecycle",
			AdapterName: "claude-code",
			SessionID:   "ses_019d0000-0000-7000-8000-000000000007",
			SessionPath: cachePath,
			StartedAt:   time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC),
			Title:       "Lifecycle recovery",
		},
		refs: map[string]lfs.FileRef{ledgerFileRaw: lfs.NewFileRef(rawContent)},
	}
}

func scriptedSessionUploadEffects(calls *[]string, refs map[string]lfs.FileRef, failAt string) sessionUploadEffects {
	mark := func(name string) error {
		*calls = append(*calls, name)
		if failAt == name {
			return errors.New(name + " failed")
		}
		return nil
	}
	return sessionUploadEffects{
		uploadLFS: func(_, _ string) (map[string]lfs.FileRef, error) {
			if err := mark("upload_lfs"); err != nil {
				return nil, err
			}
			return refs, nil
		},
		commitInitial: func(_, _ string) error { return mark("commit_initial") },
		commitRetry: func(_, _ string, _ bool) error {
			return mark("commit_retry")
		},
		reconcilePlans: func(_ string, _ []string, _, _ string) {
			_ = mark("reconcile_plans")
		},
		finalizeLinkage: func(_, _ string, _ *lfs.SessionMeta, _ string) []api.PRLinkMiss {
			_ = mark("finalize_linkage")
			return nil
		},
	}
}

func assertSessionBytesPreserved(t *testing.T, fixture sessionUploadFixture) {
	t.Helper()
	cacheBytes, err := os.ReadFile(fixture.result.RawPath)
	require.NoError(t, err)
	assert.Equal(t, fixture.rawContent, cacheBytes, "the cache remains the authoritative retry source")

	ledgerRaw := filepath.Join(fixture.ledgerPath, "sessions", fixture.sessionName, ledgerFileRaw)
	ledgerBytes, err := os.ReadFile(ledgerRaw)
	require.NoError(t, err)
	if lfs.IsPointerFile(ledgerRaw) {
		ref, err := lfs.ReadPointerFile(ledgerRaw)
		require.NoError(t, err)
		assert.Equal(t, fixture.refs[ledgerFileRaw], ref, "prepared pointers must describe the retained cache")
	} else {
		assert.Equal(t, fixture.rawContent, ledgerBytes)
	}
}

func TestUploadSessionToLedger_PreservesPriorStateAtFallibleBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failAt    string
		wantCalls []string
		wantRefs  bool
	}{
		{
			name:      "LFS upload failure",
			failAt:    "upload_lfs",
			wantCalls: []string{"upload_lfs"},
		},
		{
			name:      "git sync failure after durable manifest",
			failAt:    "commit_initial",
			wantCalls: []string{"upload_lfs", "commit_initial"},
			wantRefs:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSessionUploadFixture(t)
			var calls []string
			effects := scriptedSessionUploadEffects(&calls, fixture.refs, tc.failAt)

			err := uploadSessionToLedgerWithEffects(
				fixture.projectRoot, fixture.result, fixture.state,
				fixture.ledgerPath, fixture.sessionName, effects,
			)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.failAt+" failed")
			assert.Equal(t, tc.wantCalls, calls, "no later phase may run after a failed boundary")
			assertSessionBytesPreserved(t, fixture)

			meta, readErr := lfs.ReadSessionMeta(filepath.Join(fixture.ledgerPath, "sessions", fixture.sessionName))
			require.NoError(t, readErr, "metadata must be durable before external publication")
			assert.Equal(t, fixture.state.SessionID, meta.SessionID)
			assert.Equal(t, lfs.LinkageStatusStaged, meta.LinkageStatus)
			if tc.wantRefs {
				assert.Equal(t, fixture.refs, meta.Files)
			} else {
				assert.Empty(t, meta.Files)
			}
		})
	}
}

func TestUploadSessionToLedger_FullSuccessRunsOnlyPostPushEffects(t *testing.T) {
	fixture := newSessionUploadFixture(t)
	var calls []string
	effects := scriptedSessionUploadEffects(&calls, fixture.refs, "")
	effects.commitInitial = func(ledgerPath, sessionName string) error {
		ref, err := lfs.ReadPointerFile(filepath.Join(ledgerPath, "sessions", sessionName, ledgerFileRaw))
		require.NoError(t, err, "the first push must already contain the uploaded pointer")
		assert.Equal(t, fixture.refs[ledgerFileRaw], ref)
		calls = append(calls, "commit_initial")
		return nil
	}
	effects.finalizeLinkage = func(_, _ string, _ *lfs.SessionMeta, _ string) []api.PRLinkMiss {
		calls = append(calls, "finalize_linkage")
		return []api.PRLinkMiss{{
			PRURL:        "https://github.com/sageox/ox/pull/999",
			ExpectedLine: "SageOx-Session: https://sageox.ai/c/ses_test",
		}}
	}

	err := uploadSessionToLedgerWithEffects(
		fixture.projectRoot, fixture.result, fixture.state,
		fixture.ledgerPath, fixture.sessionName, effects,
	)

	require.NoError(t, err)
	assert.Equal(t, []string{
		"upload_lfs", "commit_initial", "reconcile_plans", "finalize_linkage",
	}, calls)
	ledgerRaw := filepath.Join(fixture.ledgerPath, "sessions", fixture.sessionName, ledgerFileRaw)
	assert.True(t, lfs.IsPointerFile(ledgerRaw), "the ledger copy contains the uploaded pointer")
	cacheBytes, readErr := os.ReadFile(fixture.result.RawPath)
	require.NoError(t, readErr)
	assert.Equal(t, fixture.rawContent, cacheBytes, "the local source survives through post-push processing")
	require.Len(t, fixture.result.PRLinkMisses, 1)
	assert.Contains(t, fixture.result.PRLinkMisses[0], "pull/999")
}

func TestUploadSessionToLedger_GitignoreFailureStopsBeforePush(t *testing.T) {
	fixture := newSessionUploadFixture(t)
	gitignorePath := filepath.Join(fixture.ledgerPath, "sessions", ".gitignore")
	require.NoError(t, os.MkdirAll(gitignorePath, 0o755))
	var calls []string
	effects := scriptedSessionUploadEffects(&calls, fixture.refs, "")

	err := uploadSessionToLedgerWithEffects(
		fixture.projectRoot, fixture.result, fixture.state,
		fixture.ledgerPath, fixture.sessionName, effects,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ensure .gitignore")
	assert.Equal(t, []string{"upload_lfs"}, calls, "push and all post-push effects must remain unreachable")
	assertSessionBytesPreserved(t, fixture)
	meta, readErr := lfs.ReadSessionMeta(filepath.Join(fixture.ledgerPath, "sessions", fixture.sessionName))
	require.NoError(t, readErr)
	assert.Equal(t, fixture.refs, meta.Files, "the uploaded manifest remains durable for doctor retry")
}

func TestUploadSessionToLedger_ReadOnlyIsNotHiddenByRecoveryWrapping(t *testing.T) {
	fixture := newSessionUploadFixture(t)
	var calls []string
	effects := scriptedSessionUploadEffects(&calls, fixture.refs, "")
	effects.uploadLFS = func(_, _ string) (map[string]lfs.FileRef, error) {
		calls = append(calls, "upload_lfs")
		return nil, api.ErrReadOnly
	}

	err := uploadSessionToLedgerWithEffects(
		fixture.projectRoot, fixture.result, fixture.state,
		fixture.ledgerPath, fixture.sessionName, effects,
	)

	assert.ErrorIs(t, err, api.ErrReadOnly)
	assert.Equal(t, api.ErrReadOnly, err, "the caller relies on the sentinel for membership guidance")
	assert.Equal(t, []string{"upload_lfs"}, calls)
	assertSessionBytesPreserved(t, fixture)
}

func TestSessionUploadOrchestration_FailedUploadThenRetryIsIdempotent(t *testing.T) {
	fixture := newSessionUploadFixture(t)
	var failedCalls []string
	failedEffects := scriptedSessionUploadEffects(&failedCalls, fixture.refs, "commit_initial")
	require.Error(t, uploadSessionToLedgerWithEffects(
		fixture.projectRoot, fixture.result, fixture.state,
		fixture.ledgerPath, fixture.sessionName, failedEffects,
	))
	assertSessionBytesPreserved(t, fixture)

	orphan := fixture.orphan()

	var retryCalls []string
	retryEffects := scriptedSessionUploadEffects(&retryCalls, fixture.refs, "")
	require.NoError(t, retrySessionUploadWithEffects(
		fixture.projectRoot, fixture.ledgerPath, orphan, retryEffects,
	))
	assert.Equal(t, []string{"upload_lfs", "commit_retry"}, retryCalls)

	sessionDir := filepath.Join(fixture.ledgerPath, "sessions", fixture.sessionName)
	firstMeta, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, fixture.state.SessionID, firstMeta.SessionID,
		"recovery must preserve the ID already made durable by the failed stop")
	assert.True(t, lfs.IsPointerFile(filepath.Join(sessionDir, ledgerFileRaw)))
	cacheBytes, err := os.ReadFile(fixture.result.RawPath)
	require.NoError(t, err)
	assert.Equal(t, fixture.rawContent, cacheBytes)

	// A second doctor pass after a crash between remote push and cache pruning
	// is safe: it replays from cache, retains identity, and converges again.
	retryCalls = nil
	require.NoError(t, retrySessionUploadWithEffects(
		fixture.projectRoot, fixture.ledgerPath, orphan, retryEffects,
	))
	assert.Equal(t, []string{"upload_lfs", "commit_retry"}, retryCalls)
	secondMeta, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, firstMeta.SessionID, secondMeta.SessionID)
	assert.Equal(t, firstMeta.Files, secondMeta.Files)
	assert.True(t, lfs.IsPointerFile(filepath.Join(sessionDir, ledgerFileRaw)))
}

// An identical retry must push the existing pointer commit before its caller
// can prune the source cache; a clean index does not prove publication.
func TestSessionUpload_RetriesUnpushedPointerCommit(t *testing.T) {
	for _, mode := range []string{"stop", "doctor"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newSessionUploadFixture(t)
			barePath, ledgerPath := createBareAndClone(t)
			isolatePushEnv(t, ledgerPath)
			fixture.ledgerPath = ledgerPath
			var calls []string
			effects := scriptedSessionUploadEffects(&calls, fixture.refs, "")
			effects.commitInitial = commitAndPushLedger
			effects.commitRetry = commitAndPushLedgerWithExtras
			publish := func() error {
				if mode == "doctor" {
					return retrySessionUploadWithEffects(fixture.projectRoot, ledgerPath, fixture.orphan(), effects)
				}
				return uploadSessionToLedgerWithEffects(fixture.projectRoot, fixture.result, fixture.state, ledgerPath, fixture.sessionName, effects)
			}

			runGit(t, ledgerPath, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "missing.git"))
			require.Error(t, publish())
			pendingHead := runGit(t, ledgerPath, "rev-parse", "HEAD")
			assert.NotEqual(t, pendingHead, runGit(t, barePath, "rev-parse", "HEAD"))
			assertSessionBytesPreserved(t, fixture)
			committedRaw := runGit(t, ledgerPath, "show", "HEAD:sessions/"+fixture.sessionName+"/raw.jsonl")
			_, _, err := lfs.ParsePointer(committedRaw)
			require.NoError(t, err, "failed publication must leave a pointer commit, never raw content")

			runGit(t, ledgerPath, "remote", "set-url", "--push", "origin", barePath)
			require.NoError(t, publish())
			assert.Equal(t, pendingHead, runGit(t, ledgerPath, "rev-parse", "HEAD"), "identical retry must not need a new commit")
			assert.Equal(t, pendingHead, runGit(t, barePath, "rev-parse", "HEAD"), "success must mean the pending commit reached the remote")
			assert.Empty(t, runGit(t, ledgerPath, "status", "--porcelain", "--", "sessions/"+fixture.sessionName+"/raw.jsonl"), "publication must leave no staged or unstaged pointer rewrites")
		})
	}
}

// Legacy content must be scrubbed before computing/uploading LFS objects;
// the later Git gate can only see their pointers.
func TestSessionUpload_ScansContentBeforeLFS(t *testing.T) {
	for _, mode := range []string{"stop", "doctor"} {
		for _, tc := range []struct {
			name         string
			allowSecrets bool
			staged       bool
		}{
			{name: "redact"},
			{name: "already staged", staged: true},
			{name: "override", allowSecrets: true, staged: true},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				fixture := newSessionUploadFixture(t)
				barePath, ledgerPath := createBareAndClone(t)
				fixture.ledgerPath = ledgerPath
				isolatePushEnv(t, ledgerPath)
				t.Setenv("OX_ALLOW_SECRETS", "")
				if tc.allowSecrets {
					t.Setenv("OX_ALLOW_SECRETS", "1")
				}
				const canary = "AKIAIOSFODNN7EXAMPLE"
				fixture.rawContent = []byte(strings.ReplaceAll(string(fixture.rawContent), "preserve me", canary))
				require.NoError(t, os.WriteFile(fixture.result.RawPath, fixture.rawContent, 0o600))
				fixture.result.SummaryMDPath = filepath.Join(fixture.state.SessionPath, ledgerFileSummaryMD)
				summary := []byte("# Legacy summary\n" + canary + "\n")
				require.NoError(t, os.WriteFile(fixture.result.SummaryMDPath, summary, 0o600))
				if tc.staged {
					sessionDir := filepath.Join(ledgerPath, "sessions", fixture.sessionName)
					require.NoError(t, os.MkdirAll(sessionDir, 0o755))
					require.NoError(t, os.WriteFile(filepath.Join(sessionDir, ledgerFileSummaryMD), summary, 0o600))
					runGit(t, ledgerPath, "add", "--sparse", sessionDir)
				}
				initialHead := runGit(t, ledgerPath, "rev-parse", "HEAD")

				var calls []string
				effects := scriptedSessionUploadEffects(&calls, nil, "")
				var uploadedRaw []byte
				effects.uploadLFS = func(_, sessionDir string) (map[string]lfs.FileRef, error) {
					assert.Equal(t, initialHead, runGit(t, ledgerPath, "rev-parse", "HEAD"), "content preparation must not amend the prior commit")
					var err error
					uploadedRaw, err = os.ReadFile(filepath.Join(sessionDir, ledgerFileRaw))
					require.NoError(t, err)
					refs := map[string]lfs.FileRef{ledgerFileRaw: lfs.NewFileRef(uploadedRaw)}
					if tc.allowSecrets {
						require.Contains(t, string(uploadedRaw), canary, "the existing explicit override remains effective")
						refs[ledgerFileSummaryMD] = lfs.NewFileRef(summary)
					} else {
						require.NotContains(t, string(uploadedRaw), canary, "LFS must never receive the detected credential")
						require.NoFileExists(t, filepath.Join(sessionDir, ledgerFileSummaryMD), "unredactable content must be quarantined before upload")
					}
					return refs, nil
				}
				effects.commitInitial = commitAndPushLedger
				effects.commitRetry = commitAndPushLedgerWithExtras
				var err error
				if mode == "doctor" {
					err = retrySessionUploadWithEffects(fixture.projectRoot, ledgerPath, fixture.orphan(), effects)
				} else {
					err = uploadSessionToLedgerWithEffects(fixture.projectRoot, fixture.result, fixture.state, ledgerPath, fixture.sessionName, effects)
				}
				require.NoError(t, err)
				remoteDir := "sessions/" + fixture.sessionName
				var meta lfs.SessionMeta
				require.NoError(t, json.Unmarshal([]byte(runGit(t, barePath, "show", "HEAD:"+remoteDir+"/meta.json")), &meta))
				assert.Equal(t, lfs.NewFileRef(uploadedRaw), meta.Files[ledgerFileRaw], "metadata must describe the redacted bytes actually uploaded")
				pointer := runGit(t, barePath, "show", "HEAD:"+remoteDir+"/raw.jsonl")
				oid, size, err := lfs.ParsePointer(pointer)
				require.NoError(t, err)
				assert.Equal(t, meta.Files[ledgerFileRaw].OID, oid)
				assert.Equal(t, int64(len(uploadedRaw)), size)
				assert.Empty(t, runGit(t, ledgerPath, "status", "--porcelain", "--", remoteDir+"/raw.jsonl"))
				if tc.allowSecrets {
					assert.Empty(t, meta.Redactions)
				} else {
					require.NotEmpty(t, meta.Redactions, "the audit must survive metadata updates after upload")
					assert.Empty(t, runGit(t, barePath, "ls-tree", "--name-only", "HEAD", remoteDir+"/summary.md"))
					quarantine := filepath.Join(ledgerPath, ".sageox", "cache", "quarantine", fixture.sessionName, ledgerFileSummaryMD)
					preserved, err := os.ReadFile(quarantine)
					require.NoError(t, err)
					assert.Equal(t, summary, preserved, "quarantine must preserve the original for recovery")
					require.FileExists(t, filepath.Join(ledgerPath, ".sageox", "cache", "redaction-debt", fixture.sessionName+".json"))
				}
			})
		}
	}
}

func TestRetrySessionUpload_PushFailureRemainsIncomplete(t *testing.T) {
	fixture := newSessionUploadFixture(t)
	orphan := fixture.orphan()

	var calls []string
	effects := scriptedSessionUploadEffects(&calls, fixture.refs, "commit_retry")
	err := retrySessionUploadWithEffects(
		fixture.projectRoot, fixture.ledgerPath, orphan, effects,
	)

	require.ErrorContains(t, err, "commit and push")
	assert.Equal(t, []string{"upload_lfs", "commit_retry"}, calls)
	_, statErr := os.Stat(fixture.state.SessionPath)
	assert.NoError(t, statErr, "failed retry must leave authoritative cache available")
	require.FileExists(t, filepath.Join(fixture.state.SessionPath, sessionUploadRetryPendingFile))

	// The committed final metadata used to make the next doctor pass skip this
	// cache directory forever. The pending marker must keep it discoverable.
	orphans, scanErr := findOrphanedSessionsInDir(filepath.Dir(fixture.state.SessionPath), fixture.ledgerPath)
	require.NoError(t, scanErr)
	require.Len(t, orphans, 1)
	assert.Equal(t, fixture.sessionName, orphans[0].SessionName)

	calls = nil
	successEffects := scriptedSessionUploadEffects(&calls, fixture.refs, "")
	require.NoError(t, retrySessionUploadWithEffects(
		fixture.projectRoot, fixture.ledgerPath, orphans[0], successEffects,
	))
	assert.Equal(t, []string{"upload_lfs", "commit_retry"}, calls)
	assert.True(t, lfs.IsPointerFile(filepath.Join(
		fixture.ledgerPath, "sessions", fixture.sessionName, ledgerFileRaw,
	)))

	require.NoError(t, os.RemoveAll(orphans[0].CachePath), "doctor prunes cache only after full success")
	orphans, scanErr = findOrphanedSessionsInDir(filepath.Dir(fixture.state.SessionPath), fixture.ledgerPath)
	require.NoError(t, scanErr)
	assert.Empty(t, orphans)
}

// Preparing a retry's redaction audit must not erase a previously durable
// manifest when the replacement LFS upload fails.
func TestRetrySessionUpload_LFSFailurePreservesManifest(t *testing.T) {
	fixture := newSessionUploadFixture(t)
	sessionDir := filepath.Join(fixture.ledgerPath, "sessions", fixture.sessionName)
	seedSessionMeta(t, sessionDir, fixture.sessionName)
	meta, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	meta.Files = fixture.refs
	require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, meta))

	var calls []string
	err = retrySessionUploadWithEffects(fixture.projectRoot, fixture.ledgerPath, fixture.orphan(),
		scriptedSessionUploadEffects(&calls, nil, "upload_lfs"))
	require.ErrorContains(t, err, "LFS upload")
	meta, err = lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, fixture.refs, meta.Files)
	assertSessionBytesPreserved(t, fixture)
}

// A failed quarantine must keep this upload pending instead of publishing a
// secret left in the index by an interrupted attempt.
func TestSessionUpload_QuarantineFailureRemainsRetryable(t *testing.T) {
	for _, mode := range []string{"stop", "doctor"} {
		for _, failure := range []string{"index locked", "rename blocked"} {
			t.Run(mode+"/"+failure, func(t *testing.T) {
				fixture := newSessionUploadFixture(t)
				_, fixture.ledgerPath = createBareAndClone(t)
				isolatePushEnv(t, fixture.ledgerPath)
				t.Setenv("OX_ALLOW_SECRETS", "")
				fixture.result.SummaryMDPath = filepath.Join(fixture.state.SessionPath, ledgerFileSummaryMD)
				summary := []byte("# Summary\nAKIAIOSFODNN7EXAMPLE\n")
				require.NoError(t, os.WriteFile(fixture.result.SummaryMDPath, summary, 0o600))
				sessionDir := filepath.Join(fixture.ledgerPath, "sessions", fixture.sessionName)
				require.NoError(t, os.MkdirAll(sessionDir, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(sessionDir, ledgerFileSummaryMD), summary, 0o600))
				runGit(t, fixture.ledgerPath, "add", "--sparse", sessionDir)
				blocker := filepath.Join(fixture.ledgerPath, ".git", "index.lock")
				if failure == "index locked" {
					require.NoError(t, os.WriteFile(blocker, nil, 0o600))
				} else {
					blocker = filepath.Join(fixture.ledgerPath, ".sageox", "cache", "quarantine", fixture.sessionName, ledgerFileSummaryMD)
					require.NoError(t, os.MkdirAll(blocker, 0o700))
				}
				var calls []string
				effects := scriptedSessionUploadEffects(&calls, fixture.refs, "")
				publish := func() error {
					if mode == "doctor" {
						return retrySessionUploadWithEffects(fixture.projectRoot, fixture.ledgerPath, fixture.orphan(), effects)
					}
					return uploadSessionToLedgerWithEffects(fixture.projectRoot, fixture.result, fixture.state, fixture.ledgerPath, fixture.sessionName, effects)
				}
				require.ErrorContains(t, publish(), "prepare session quarantine")
				assert.Empty(t, calls, "quarantine failure must stop before LFS upload or commit")
				assertSessionBytesPreserved(t, fixture)
				preserved, err := os.ReadFile(fixture.result.SummaryMDPath)
				require.NoError(t, err)
				assert.Equal(t, summary, preserved)
				require.NoError(t, os.RemoveAll(blocker))
				require.NoError(t, publish(), "retry should make progress after the local failure clears")
			})
		}
	}
}

func TestRetrySessionUpload_AcceptsLargeValidHeader(t *testing.T) {
	fixture := newSessionUploadFixture(t)

	var raw bytes.Buffer
	enc := json.NewEncoder(&raw)
	require.NoError(t, enc.Encode(map[string]any{
		"type": "header",
		"metadata": map[string]any{
			"agent_id":   fixture.state.AgentID,
			"agent_type": fixture.state.AdapterName,
			"session_id": fixture.state.SessionID,
			"model":      strings.Repeat("m", 70*1024),
		},
	}))
	require.NoError(t, enc.Encode(map[string]any{"type": "user", "content": "preserve me"}))
	require.NoError(t, enc.Encode(map[string]any{"type": "footer", "entry_count": 1}))
	require.Greater(t, bytes.IndexByte(raw.Bytes(), '\n'), 64*1024)

	fixture.rawContent = raw.Bytes()
	require.NoError(t, os.WriteFile(fixture.result.RawPath, fixture.rawContent, 0o600))
	fixture.result.EntryCount = 1
	fixture.refs = map[string]lfs.FileRef{ledgerFileRaw: lfs.NewFileRef(fixture.rawContent)}

	require.NoError(t, validateRawJSONLHeader(fixture.result.RawPath))
	var calls []string
	effects := scriptedSessionUploadEffects(&calls, fixture.refs, "")
	require.NoError(t, retrySessionUploadWithEffects(
		fixture.projectRoot, fixture.ledgerPath, fixture.orphan(), effects,
	))
	assert.Equal(t, []string{"upload_lfs", "commit_retry"}, calls)
}

func TestWriteSessionUploadRetryPending_RequiresExistingCache(t *testing.T) {
	err := writeSessionUploadRetryPending(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
}

func TestRetrySessionUpload_PendingMarkerFailureStopsBeforeMutation(t *testing.T) {
	fixture := newSessionUploadFixture(t)
	require.NoError(t, os.Mkdir(
		filepath.Join(fixture.state.SessionPath, sessionUploadRetryPendingFile),
		0o700,
	))

	var calls []string
	err := retrySessionUploadWithEffects(
		fixture.projectRoot,
		fixture.ledgerPath,
		fixture.orphan(),
		scriptedSessionUploadEffects(&calls, fixture.refs, ""),
	)
	require.ErrorContains(t, err, "record pending session upload retry")
	assert.Empty(t, calls, "ledger mutation must not begin without durable retry ownership")
}

func TestRetrySessionUpload_UnsafePointerPathRemainsRetryable(t *testing.T) {
	fixture := newSessionUploadFixture(t)
	fixture.refs["../escape.jsonl"] = lfs.NewFileRef([]byte("must not escape"))

	var calls []string
	err := retrySessionUploadWithEffects(
		fixture.projectRoot,
		fixture.ledgerPath,
		fixture.orphan(),
		scriptedSessionUploadEffects(&calls, fixture.refs, ""),
	)
	require.ErrorContains(t, err, "write LFS pointer files")
	require.FileExists(t, filepath.Join(fixture.state.SessionPath, sessionUploadRetryPendingFile))
}
