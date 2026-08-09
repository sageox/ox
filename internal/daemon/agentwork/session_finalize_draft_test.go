package agentwork

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Draft-placeholder behavior in daemon anti-entropy (ADR-029).
//
// The stakes here are higher than in the CLI: every work class in this file
// either writes into a git-tracked session directory or spends LLM tokens, and
// the one path that must never fire on a draft — recoverRawFromSessionFile —
// writes REAL transcript bytes onto the tracked raw.jsonl, which breaks LFS
// linkage and makes the ledger reject every future push for the whole team.

const draftTestSessionID = "ses_01950000-0000-7000-8000-0000000000bb"

func writeDraftMeta(t *testing.T, sessionDir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(sessionDir, 0755))
	updated := time.Now().UTC()
	require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, &lfs.SessionMeta{
		Version:     "1.0",
		SessionName: filepath.Base(sessionDir),
		SessionID:   draftTestSessionID,
		AgentID:     "OxDraft",
		AgentType:   "claude-code",
		CreatedAt:   time.Now().UTC(),
		Draft:       true,
		TurnCount:   2,
		UpdatedAt:   &updated,
		Files:       map[string]lfs.FileRef{},
	}))
}

// writeFinalizedSession writes a session with a REAL raw.jsonl and a stub
// summary — the shape anti-entropy is supposed to pick up. Used as the
// negative control.
func writeFinalizedSession(t *testing.T, sessionDir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(sessionDir, 0755))
	raw := `{"type":"header","metadata":{"version":"1.0","agent_id":"OxReal"}}` + "\n" +
		`{"ts":"2026-01-01T00:01:00Z","type":"user","content":"real transcript content"}` + "\n" +
		`{"ts":"2026-01-01T00:02:00Z","type":"assistant","content":"a real reply"}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(raw), 0644))
	require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, &lfs.SessionMeta{
		Version: "1.0", SessionName: filepath.Base(sessionDir),
		AgentID: "OxReal", AgentType: "claude-code", CreatedAt: time.Now().UTC(),
	}))
}

// staleRecordingMarker writes an abandoned recording marker.
//
// The PID is 999999999, matching this package's existing convention
// (session_watcher_test.go: "PID that definitely doesn't exist"). NOT 999999 —
// that is well under Linux's usual pid_max of 4194304, so on a busy machine it
// can be a LIVE process. And the failure is silent rather than flaky: a live
// PID makes isStaleRecording return not-stale, the function returns zero items
// before ever reaching the draft guard, and the assertion passes even with the
// guard deleted.
// markDirAsDraft stamps draft:true onto an EXISTING meta.json, preserving
// whatever else the directory holds.
//
// Deliberately not WriteDraftSessionMeta: that refuses a directory containing
// raw.jsonl, which is the point of the guard on the write side. This models the
// state that arises anyway — a tail-watcher transcript landing in a directory a
// draft already claimed — and it is the only fixture in which the daemon's
// draft skip is the sole thing preventing work.
func markDirAsDraft(t *testing.T, sessionDir string) {
	t.Helper()
	meta, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	updated := time.Now().UTC()
	meta.Draft = true
	meta.TurnCount = 2
	meta.UpdatedAt = &updated
	meta.Files = map[string]lfs.FileRef{}
	require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, meta))
}

func staleRecordingMarker(t *testing.T, sessionDir string, age time.Duration) {
	t.Helper()
	body := `{"agent_id":"OxDraft","started_at":"` +
		time.Now().Add(-age).UTC().Format(time.RFC3339) + `","parent_pid":999999999}`
	path := filepath.Join(sessionDir, ".recording.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0644))
	old := time.Now().Add(-age)
	require.NoError(t, os.Chtimes(path, old, old))
}

// TestDetectInDir_SkipsDraftPlaceholders.
//
// The final row is MANDATORY. Without a case that yields work, this whole table
// passes with the draft logic deleted — a meta-only directory already returns
// zero items via the `!hasRaw` continue, so every draft row would be
// vacuously green.
func TestDetectInDir_SkipsDraftPlaceholders(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, sessionDir string)
		// wantItems: the fixture should produce anti-entropy work.
		wantItems bool
		// plantsRaw: the fixture deliberately puts a transcript in the tracked
		// directory, so the "no bytes recovered here" assertion does not apply
		// — the point of that row is that no WORK is produced despite it.
		plantsRaw bool
	}{
		{
			name:  "draft only",
			setup: func(t *testing.T, dir string) { writeDraftMeta(t, dir) },
		},
		{
			name: "draft with a stale recording marker",
			setup: func(t *testing.T, dir string) {
				writeDraftMeta(t, dir)
				staleRecordingMarker(t, dir, 26*time.Hour)
			},
		},
		{
			name: "draft with a server-authored summary",
			setup: func(t *testing.T, dir string) {
				writeDraftMeta(t, dir)
				require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.json"),
					[]byte(`{"title":"Zero-turn session"}`), 0644))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.md"),
					[]byte("SERVER_AUTHORED"), 0644))
			},
		},
		{
			name: "unreadable meta fails CLOSED",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.MkdirAll(dir, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"draft":tr`), 0644))
				staleRecordingMarker(t, dir, 26*time.Hour)
			},
		},
		{
			// THE ROW THAT ACTUALLY TESTS THE GUARD.
			//
			// Every other draft row above is skipped by the pre-existing
			// `!hasRaw` continue, so they all stay green with the draft guard
			// DELETED — they prove the table is not vacuous, not that the guard
			// works. This row is a draft-marked directory that ALSO holds a
			// real transcript, so without the guard detectInDir would finalize
			// it: spend LLM tokens on it, write artifacts into it, and race the
			// CLI that is about to purge it.
			//
			// Not hypothetical. The daemon's tail-watcher writes live
			// transcripts into this same git-tracked directory, so a draft
			// published while tail mode is running produces exactly this shape.
			name: "draft marked on a directory that ALSO holds a real transcript",
			setup: func(t *testing.T, dir string) {
				writeFinalizedSession(t, dir) // real raw.jsonl with substantive entries
				markDirAsDraft(t, dir)        // ...then stamped as a draft
			},
			plantsRaw: true,
		},
		{
			name:      "NEGATIVE CONTROL: real session with a stub summary yields work",
			setup:     func(t *testing.T, dir string) { writeFinalizedSession(t, dir) },
			wantItems: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ledgerPath := t.TempDir()
			sessionsDir := filepath.Join(ledgerPath, "sessions")
			sessionDir := filepath.Join(sessionsDir, "2026-01-01T00-00-testuser-OxD001")
			tc.setup(t, sessionDir)

			h := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))
			items, detectErr := h.Detect(ledgerPath)
			require.NoError(t, detectErr)

			if tc.wantItems {
				assert.NotEmpty(t, items,
					"the negative control must produce work, or this table proves nothing")
				return
			}
			assert.Empty(t, items, "a draft must produce no anti-entropy work")

			if !tc.plantsRaw {
				// The invariant that actually matters: anti-entropy must never
				// RECOVER transcript bytes onto the git-tracked path, which is
				// the LFS-linkage break.
				assert.NoFileExists(t, filepath.Join(sessionDir, "raw.jsonl"),
					"anti-entropy must never recover transcript bytes into a tracked draft directory")
			}
		})
	}
}

// TestDetectOrphanedForAgent_SkipsDraftPlaceholders.
//
// The second detection path that can reach recoverRawFromSessionFile. It scans
// the same git-tracked sessions/ directory as detectInDir but is guarded only
// by .recording.json presence, so it needed the same skip.
func TestDetectOrphanedForAgent_SkipsDraftPlaceholders(t *testing.T) {
	h := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))

	t.Run("meta-only draft", func(t *testing.T) {
		ledgerPath := t.TempDir()
		sessionDir := filepath.Join(ledgerPath, "sessions", "2026-01-01T00-00-testuser-OxD002")
		writeDraftMeta(t, sessionDir)
		staleRecordingMarker(t, sessionDir, 26*time.Hour)

		assert.Empty(t, h.DetectOrphanedForAgent(ledgerPath, "OxDraft", 999999999),
			"a draft must never be treated as an orphan to recover")
		assert.NoFileExists(t, filepath.Join(sessionDir, "raw.jsonl"))

		meta, err := lfs.ReadSessionMeta(sessionDir)
		require.NoError(t, err)
		assert.True(t, meta.IsDraft(), "the draft meta must be untouched")
	})

	t.Run("draft over a real transcript: the guard is the only thing acting", func(t *testing.T) {
		ledgerPath := t.TempDir()
		sessionDir := filepath.Join(ledgerPath, "sessions", "2026-01-01T00-00-testuser-OxD002")
		writeFinalizedSession(t, sessionDir)
		markDirAsDraft(t, sessionDir)
		staleRecordingMarker(t, sessionDir, 26*time.Hour)

		assert.Empty(t, h.DetectOrphanedForAgent(ledgerPath, "OxDraft", 999999999),
			"without the draft guard this directory produces orphan-recovery work")
	})

	t.Run("NEGATIVE CONTROL: a real orphan still yields work", func(t *testing.T) {
		ledgerPath := t.TempDir()
		sessionDir := filepath.Join(ledgerPath, "sessions", "2026-01-01T00-00-testuser-OxD00R")
		writeFinalizedSession(t, sessionDir)
		staleRecordingMarker(t, sessionDir, 26*time.Hour)

		assert.NotEmpty(t, h.DetectOrphanedForAgent(ledgerPath, "OxDraft", 999999999),
			"or the two rows above prove nothing")
	})
}

// TestClearDraft_OnPreserveEverythingMutation.
//
// The daemon's finalize does `next := current` to preserve fields it does not
// own — correct for redactions / produced_commits / linkage, but it would carry
// draft:true straight through finalization. The result would be a fully
// finalized session that every consumer keeps treating as provisional, that
// doctor refuses to repair, and whose own write is then REJECTED by the draft
// writer-invariant once a Files manifest is attached.
//
// This drives the actual mutation shape rather than asserting on ClearDraft in
// isolation, because the bug is in the interaction.
func TestClearDraft_OnPreserveEverythingMutation(t *testing.T) {
	sessionDir := t.TempDir()
	writeDraftMeta(t, sessionDir)

	// Exactly what writeMetaAndUploadLFS does.
	require.NoError(t, lfs.MutateSessionMeta(t.Context(), sessionDir,
		func(current *lfs.SessionMeta) (*lfs.SessionMeta, error) {
			next := current
			require.NotNil(t, next)
			next.ClearDraft()
			next.Title = "Real finalized session"
			next.EntryCount = 42
			next.Files = map[string]lfs.FileRef{"raw.jsonl": {OID: "sha256:abc", Size: 10}}
			return next, nil
		}))

	meta, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.False(t, meta.IsDraft(), "finalization must clear the draft marker")
	assert.Zero(t, meta.TurnCount)
	assert.Nil(t, meta.UpdatedAt)
	assert.Equal(t, draftTestSessionID, meta.SessionID, "the ses_ id must survive finalization")
	assert.Equal(t, "Real finalized session", meta.Title)

	// Byte-level: no residual draft key at all.
	raw, err := os.ReadFile(filepath.Join(sessionDir, "meta.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), `"draft"`)
}

// TestClearDraftOmitted_IsRejectedByWriterInvariant proves the guard above is
// load-bearing rather than cosmetic: forgetting ClearDraft does not merely
// leave a stale flag, it makes the entire meta write FAIL, which on the daemon
// path silently drops the LFS refs and leaves content committed as raw git
// blobs with no pointer files.
func TestClearDraftOmitted_IsRejectedByWriterInvariant(t *testing.T) {
	sessionDir := t.TempDir()
	writeDraftMeta(t, sessionDir)

	err := lfs.MutateSessionMeta(t.Context(), sessionDir,
		func(current *lfs.SessionMeta) (*lfs.SessionMeta, error) {
			next := current
			next.Files = map[string]lfs.FileRef{"raw.jsonl": {OID: "sha256:abc", Size: 10}}
			return next, nil // ClearDraft deliberately omitted
		})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not name artifacts")
}

// TestStageSessionInLedger_PurgesDraftAndPreservesSessionID.
//
// Two properties in one flow, both load-bearing:
//
//  1. A server-authored artifact in the draft directory has no counterpart in
//     the cache, so the copy loop alone would leave it in place and
//     missingArtifacts would then treat it as a real summary of the transcript.
//  2. The ses_ id must be read from the draft BEFORE the purge deletes the file
//     carrying it. For a recording whose raw.jsonl header predates the
//     SessionID field, the draft is the ONLY carrier — losing it mints a fresh
//     id and 404s a /c/ link already published in a PR body.
func TestStageSessionInLedger_PurgesDraftAndPreservesSessionID(t *testing.T) {
	// A REAL git repo. Without one, the handler's `git rm` fails, is swallowed
	// as a Warn, and every assertion below is satisfied by the os.RemoveAll
	// alone — so deleting the `git rm` entirely left this test green while the
	// daemon's purge silently degraded to a worktree-only delete, leaving the
	// server-authored artifact tracked in the index and the ledger dirty.
	ledgerPath := initTestGitRepo(t)
	const sessionName = "2026-01-01T00-00-testuser-OxD003"

	// git-tracked draft, polluted by a server-authored summary
	destDir := filepath.Join(ledgerPath, "sessions", sessionName)
	writeDraftMeta(t, destDir)
	require.NoError(t, os.WriteFile(filepath.Join(destDir, "summary.json"),
		[]byte(`{"title":"Zero-turn session","files_changed":[{"path":"SERVER.md"}]}`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(destDir, "summary.md"),
		[]byte("SERVER_AUTHORED_SENTINEL"), 0644))

	// COMMIT them. In production the draft is committed and pushed, and the
	// server's summary arrives via a pull — so `git rm` has tracked files to
	// remove. With everything merely untracked, `git rm --ignore-unmatch` is a
	// silent no-op and the index assertion below cannot distinguish a working
	// purge from a missing one.
	runGitCmd(t, ledgerPath, "add", "-A")
	runGitCmd(t, ledgerPath, "commit", "--no-verify", "-q", "-m", "draft + server summary")

	// the real recording, waiting in the cache
	cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName)
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"),
		[]byte(`{"type":"header"}`+"\n"+`{"type":"user","content":"real"}`+"\n"), 0644))

	h := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))
	payload := &SessionFinalizePayload{SessionDir: cacheDir, LedgerPath: ledgerPath}
	require.NoError(t, h.stageSessionInLedger(payload))

	assert.Equal(t, destDir, payload.SessionDir, "payload must point at the staged location")
	assert.Equal(t, draftTestSessionID, payload.PreservedSessionID,
		"the draft's ses_ id must be captured before the purge deletes it")

	assert.NoFileExists(t, filepath.Join(destDir, "summary.json"),
		"a server-authored summary of the zero-turn draft must not survive staging")
	assert.NoFileExists(t, filepath.Join(destDir, "summary.md"))
	assert.FileExists(t, filepath.Join(destDir, "raw.jsonl"), "the real transcript must be staged")

	// The INDEX, not just the worktree. os.RemoveAll alone leaves the
	// server-authored files tracked: the ledger reports dirty, the next
	// checkout restores them, and anti-entropy picks them up again. Only the
	// `git rm` half produces a staged deletion.
	staged := gitOutput(t, ledgerPath, "diff", "--cached", "--name-status")
	assert.Contains(t, staged, "D\tsessions/"+sessionName+"/summary.json",
		"the purge must stage a DELETION, not merely remove the file from the worktree")
	assert.Contains(t, staged, "D\tsessions/"+sessionName+"/summary.md")
}

// initTestGitRepo creates a real git repo with one commit, so index-level
// assertions are possible. Reuses this package's existing runGitCmd/gitOutput
// helpers (session_finalize_git_test.go) rather than adding a third pair.
func initTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGitCmd(t, dir, "init", "--quiet")
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitkeep"), nil, 0644))
	runGitCmd(t, dir, "add", ".gitkeep")
	runGitCmd(t, dir, "commit", "--no-verify", "-q", "-m", "init")
	return dir
}

// TestStageSessionInLedger_LeavesFinalizedSessionAlone is the negative control
// for the purge. Without it, "always purge the destination" passes the test
// above while destroying a legitimate prior upload.
func TestStageSessionInLedger_LeavesFinalizedSessionAlone(t *testing.T) {
	ledgerPath := initTestGitRepo(t)
	const sessionName = "2026-01-01T00-00-testuser-OxD004"

	destDir := filepath.Join(ledgerPath, "sessions", sessionName)
	require.NoError(t, os.MkdirAll(destDir, 0755))
	require.NoError(t, lfs.WriteSessionMetaOnly(destDir, &lfs.SessionMeta{
		Version: "1.0", SessionName: sessionName, SessionID: draftTestSessionID,
		CreatedAt: time.Now(), Title: "already finalized",
		Files: map[string]lfs.FileRef{"raw.jsonl": {OID: "sha256:abc", Size: 10}},
	}))
	require.NoError(t, os.WriteFile(filepath.Join(destDir, "summary.md"), []byte("REAL SUMMARY"), 0644))

	cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName)
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"), []byte(`{"type":"header"}`+"\n"), 0644))

	h := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))
	payload := &SessionFinalizePayload{SessionDir: cacheDir, LedgerPath: ledgerPath}
	require.NoError(t, h.stageSessionInLedger(payload))

	body, err := os.ReadFile(filepath.Join(destDir, "summary.md"))
	require.NoError(t, err)
	assert.Equal(t, "REAL SUMMARY", string(body),
		"a finalized session's artifacts must not be purged")
	assert.Empty(t, payload.PreservedSessionID, "no draft was superseded, so nothing to carry")
}
