package agentwork

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Abandoned-draft reclaim (#966).
//
// `draft: true` is written at recording start and cleared only by finalize, so
// a client process that dies in between freezes it set forever. The draft guard
// used to run BEFORE the liveness probe, which made every such session
// permanently invisible to anti-entropy — two real sessions holding 1151 and
// 628 entries sat unuploaded for 21h behind a healthy daemon, and
// `--force-session-uploads` reported items=0.
//
// The guard now consults isStaleRecording first, so it can tell a LIVE draft
// (still skipped — ADR-029) from an ABANDONED one (reclaimed). These tests pin
// both halves plus the fail-closed arm that must not move with them.

const (
	ledgerSessionsSubdir = "sessions"
	cacheSessionsSubdir  = ".sageox/cache/sessions"
)

// sessionsDirFor resolves one of the two directories Detect scans. Which one a
// fixture lives in is load-bearing: recovery into the git-tracked
// <ledger>/sessions tree is blocked outright, while the cache tree is where
// recoverRawFromSessionFile actually runs.
func sessionsDirFor(ledgerPath, subdir string) string {
	return filepath.Join(ledgerPath, filepath.FromSlash(subdir))
}

func TestDetect_ReclaimsAbandonedDraft(t *testing.T) {
	const sessionName = "2026-09-16T18-54-testuser-OxPcx9"

	tests := []struct {
		name string
		// subdir picks the scan directory the fixture is planted in.
		subdir string
		setup  func(t *testing.T, sessionDir string)
		// wantItems: anti-entropy must produce work for this session.
		wantItems bool
		// wantMarkerKept: .recording.json must survive untouched, proving the
		// scan never reached the clear/recover path.
		wantMarkerKept bool
	}{
		{
			// THE REGRESSION. Draft flag frozen by a dead client, with a real
			// transcript already on disk. Before the fix this returned zero
			// items on every cycle, forever.
			name:      "abandoned draft holding a transcript (ledger)",
			subdir:    ledgerSessionsSubdir,
			setup:     abandonedDraftWithTranscript,
			wantItems: true,
		},
		{
			// Same shape in the ledger cache — the tree StartRecording writes
			// to, and the only one where recovery may write bytes.
			name:      "abandoned draft holding a transcript (cache)",
			subdir:    cacheSessionsSubdir,
			setup:     abandonedDraftWithTranscript,
			wantItems: true,
		},
		{
			// ADR-029 still holds: a draft whose client is alive is an
			// in-progress marker, not stranded work.
			name:   "live draft holding a transcript",
			subdir: ledgerSessionsSubdir,
			setup: func(t *testing.T, dir string) {
				writeFinalizedSession(t, dir)
				markDirAsDraft(t, dir)
				liveRecordingMarker(t, dir, 26*time.Hour)
			},
			wantMarkerKept: true,
		},
		{
			// No marker at all. Absence of evidence is not proof of death, and
			// this is the shape of the ordinary placeholder the CLI refreshes
			// every turn, so the conservative skip must survive.
			name:   "draft holding a transcript with no recording marker",
			subdir: ledgerSessionsSubdir,
			setup: func(t *testing.T, dir string) {
				writeFinalizedSession(t, dir)
				markDirAsDraft(t, dir)
			},
		},
		{
			// A dead PID must NOT buy a pass through the fail-closed arm. An
			// unreadable meta.json is the #956 corruption class; falling
			// through reaches recoverRawFromSessionFile, which would write real
			// transcript bytes and break LFS linkage for the whole team.
			// Planted in the CACHE tree deliberately — that is where recovery
			// would actually run, so the surviving marker is real evidence and
			// not an artifact of the tracked-path block.
			name:   "unreadable meta with a dead PID still fails CLOSED",
			subdir: cacheSessionsSubdir,
			setup: func(t *testing.T, dir string) {
				writeFinalizedSession(t, dir)
				require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"),
					[]byte(`{"draft":tr`), 0644))
				staleRecordingMarker(t, dir, 26*time.Hour)
			},
			wantMarkerKept: true,
		},
		{
			// Non-draft control: unchanged by this fix, and the row that keeps
			// the table honest.
			name:   "NEGATIVE CONTROL: abandoned non-draft still yields work",
			subdir: ledgerSessionsSubdir,
			setup: func(t *testing.T, dir string) {
				writeFinalizedSession(t, dir)
				staleRecordingMarker(t, dir, 26*time.Hour)
			},
			wantItems: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ledgerPath := t.TempDir()
			sessionDir := filepath.Join(sessionsDirFor(ledgerPath, tc.subdir), sessionName)
			tc.setup(t, sessionDir)

			h := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))
			items, err := h.Detect(ledgerPath)
			require.NoError(t, err)

			if tc.wantItems {
				require.Len(t, items, 1, "the abandoned session must be queued for finalization")
				assert.Equal(t, sessionFinalizeType+":"+sessionName, items[0].DedupKey)
			} else {
				assert.Empty(t, items)
			}

			if tc.wantMarkerKept {
				assert.FileExists(t, filepath.Join(sessionDir, recordingMarker),
					"the scan must not have reached the clear/recover path")
			}
		})
	}
}

// abandonedDraftWithTranscript is the exact #966 shape: a real transcript on
// disk, meta.json frozen at draft:true, and a recording marker naming a process
// that is gone.
func abandonedDraftWithTranscript(t *testing.T, sessionDir string) {
	t.Helper()
	writeFinalizedSession(t, sessionDir)
	markDirAsDraft(t, sessionDir)
	staleRecordingMarker(t, sessionDir, 26*time.Hour)
}

// TestDetect_WarnsAboutSessionsSkippedWithContent.
//
// The silence was half the bug: `items=0` reads exactly like a healthy ledger,
// and the only trace of two stranded sessions was a Debug line nobody sees.
// A skip is routine; a skip while a non-empty raw.jsonl sits in the directory
// is an anomaly and must be countable.
func TestDetect_WarnsAboutSessionsSkippedWithContent(t *testing.T) {
	const (
		liveDraft  = "2026-09-16T18-54-testuser-OxLIVE"
		unreadable = "2026-09-16T20-00-testuser-OxBADM"
		emptyDraft = "2026-09-16T21-00-testuser-OxEMPT"
	)

	ledgerPath := t.TempDir()
	sessionsDir := sessionsDirFor(ledgerPath, ledgerSessionsSubdir)

	// skipped WITH content — both must be counted
	liveDir := filepath.Join(sessionsDir, liveDraft)
	writeFinalizedSession(t, liveDir)
	markDirAsDraft(t, liveDir)
	liveRecordingMarker(t, liveDir, 26*time.Hour)

	badDir := filepath.Join(sessionsDir, unreadable)
	writeFinalizedSession(t, badDir)
	require.NoError(t, os.WriteFile(filepath.Join(badDir, "meta.json"), []byte(`{"draft":tr`), 0644))

	// skipped with NO content — routine, must NOT be counted or the signal
	// drowns in every ordinary per-turn draft placeholder.
	writeDraftMeta(t, filepath.Join(sessionsDir, emptyDraft))

	var buf bytes.Buffer
	h := NewSessionFinalizeHandlerForTest(slog.New(slog.NewJSONHandler(&buf, nil)))
	items, err := h.Detect(ledgerPath)
	require.NoError(t, err)
	require.Empty(t, items)

	records := decodeLogRecords(t, &buf)

	complete := findLogRecord(t, records, "session finalize detect complete")
	assert.Equal(t, float64(2), complete["skipped_with_content"],
		"the routine empty placeholder must not inflate the anomaly count")

	warn := findLogRecord(t, records, "sessions skipped while holding transcript content")
	assert.Equal(t, "WARN", warn["level"], "Debug is why this went unnoticed for 21h")
	assert.Equal(t, float64(1), warn["draft_not_provably_abandoned"])
	assert.Equal(t, float64(1), warn["unreadable_meta"])
	assert.ElementsMatch(t, []any{liveDraft, unreadable}, warn["sessions"],
		"the user needs the names to run `ox agent <id> session recover`")
}

// TestDetect_NoWarnWhenNothingIsStranded is the negative control for the counter:
// a clean ledger must stay quiet, or the Warn becomes noise and gets ignored.
func TestDetect_NoWarnWhenNothingIsStranded(t *testing.T) {
	ledgerPath := t.TempDir()
	writeFinalizedSession(t, filepath.Join(sessionsDirFor(ledgerPath, ledgerSessionsSubdir),
		"2026-09-16T18-54-testuser-OxCLEAN"))

	var buf bytes.Buffer
	h := NewSessionFinalizeHandlerForTest(slog.New(slog.NewJSONHandler(&buf, nil)))
	items, err := h.Detect(ledgerPath)
	require.NoError(t, err)
	require.Len(t, items, 1)

	records := decodeLogRecords(t, &buf)
	assert.Equal(t, float64(0),
		findLogRecord(t, records, "session finalize detect complete")["skipped_with_content"])
	for _, rec := range records {
		assert.NotEqual(t, "sessions skipped while holding transcript content", rec["msg"])
	}
}

func decodeLogRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	for scanner.Scan() {
		var rec map[string]any
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &rec))
		out = append(out, rec)
	}
	require.NoError(t, scanner.Err())
	return out
}

func findLogRecord(t *testing.T, records []map[string]any, msg string) map[string]any {
	t.Helper()
	for _, rec := range records {
		if rec["msg"] == msg {
			return rec
		}
	}
	t.Fatalf("no log record with msg %q", msg)
	return nil
}

// TestDetectOrphanedForAgent_ReclaimsAbandonedDraft covers the second copy of
// the same ordering bug (the agent-exit path). The live-draft half — the
// invariant this must not break — lives in
// TestDetectOrphanedForAgent_SkipsDraftPlaceholders.
func TestDetectOrphanedForAgent_ReclaimsAbandonedDraft(t *testing.T) {
	const (
		agentID     = "OxDraft"
		sessionName = "2026-09-16T20-00-testuser-OxLtF2"
	)

	tests := []struct {
		name      string
		subdir    string
		setup     func(t *testing.T, sessionDir string)
		wantItems bool
	}{
		{
			name:      "abandoned draft holding a transcript (ledger)",
			subdir:    ledgerSessionsSubdir,
			setup:     abandonedDraftWithTranscript,
			wantItems: true,
		},
		{
			name:      "abandoned draft holding a transcript (cache)",
			subdir:    cacheSessionsSubdir,
			setup:     abandonedDraftWithTranscript,
			wantItems: true,
		},
		{
			// The fail-closed arm is ordered ABOVE the liveness cross-check on
			// this path, so a dead PID must not reach recovery here either.
			name:   "unreadable meta with a dead PID still fails CLOSED",
			subdir: cacheSessionsSubdir,
			setup: func(t *testing.T, dir string) {
				writeFinalizedSession(t, dir)
				require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"),
					[]byte(`{"draft":tr`), 0644))
				staleRecordingMarker(t, dir, 26*time.Hour)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ledgerPath := t.TempDir()
			tc.setup(t, filepath.Join(sessionsDirFor(ledgerPath, tc.subdir), sessionName))

			h := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))
			// heartbeatPID 0: the agent is gone, so the caller has no live PID
			// to offer. The verdict must come from the marker's parent_pid.
			items := h.DetectOrphanedForAgent(ledgerPath, agentID, 0)

			if tc.wantItems {
				require.Len(t, items, 1)
				assert.Equal(t, sessionFinalizeType+":"+sessionName, items[0].DedupKey)
			} else {
				assert.Empty(t, items)
			}
		})
	}
}

// TestForceDetect_ReachesAbandonedDraft.
//
// `ox doctor --force-session-uploads` exists precisely for "I believe something
// is stuck", and it routes straight through Manager.ForceDetect ->
// handler.Detect. The draft guard sat upstream of it, so the one flag a user
// reaches for could not touch these sessions: the incident report shows two
// runs, 15 minutes apart, both printing items=0.
func TestForceDetect_ReachesAbandonedDraft(t *testing.T) {
	const sessionName = "2026-09-16T18-54-testuser-OxPcx9"

	m, _ := newTestManager(NewMockRunner(false), func() *config.AgentWorkerConfig {
		return enabledConfigWith(1, 100)
	})
	m.ledgerPath = t.TempDir()
	m.RegisterHandler(NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler)))

	abandonedDraftWithTranscript(t,
		filepath.Join(sessionsDirFor(m.ledgerPath, cacheSessionsSubdir), sessionName))

	assert.Positive(t, m.ForceDetect(), "--force-session-uploads must reach a dead-PID draft")
	assert.Contains(t, drainDedupKeys(m), sessionFinalizeType+":"+sessionName)
}

// drainDedupKeys empties the manager's queue and returns what was in it.
// ForceDetect also enqueues scheduled agent tasks (doctor, etc.), so a non-zero
// return alone does not prove THIS session was queued — the dedup key does.
func drainDedupKeys(m *Manager) []string {
	var keys []string
	for {
		item := m.queue.Dequeue()
		if item == nil {
			return keys
		}
		keys = append(keys, item.DedupKey)
	}
}

// TestDetect_AbandonedDraftMetaSurvivesReclaim.
//
// Reclaiming must not mutate meta.json. The ses_ id published in the draft is
// the only carrier for a /c/<id> link that may already be in a PR body, and
// finalize (not detect) is what clears the draft flag.
func TestDetect_AbandonedDraftMetaSurvivesReclaim(t *testing.T) {
	ledgerPath := t.TempDir()
	sessionDir := filepath.Join(sessionsDirFor(ledgerPath, cacheSessionsSubdir),
		"2026-09-16T18-54-testuser-OxPcx9")
	abandonedDraftWithTranscript(t, sessionDir)

	before, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)

	h := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))
	items, err := h.Detect(ledgerPath)
	require.NoError(t, err)
	require.Len(t, items, 1)

	after, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.True(t, after.IsDraft(), "detect must not clear the draft flag; finalize does")
	assert.Equal(t, before.SessionID, after.SessionID, "the published ses_ id must not rotate")
}
