package agentwork

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const orphanedRaw = `{"metadata":{"agent_id":"OxKILL","agent_type":"claude","version":"1.0"},"type":"header"}
{"type":"user","content":"fix the login bug","seq":0,"timestamp":"2026-11-19T09:00:01Z"}
{"type":"assistant","content":"I see the issue in the auth handler.","seq":1,"timestamp":"2026-11-19T09:00:08Z"}
`

// orphanedUploadFixture is what a session stop leaves when the coding agent
// kills it mid-push at exit (sageox/ox#1936): the final capture in the cache,
// the upload marker bound to it, and .recording.json still in place because
// the stop died before clearing it. The recording is two minutes old and its
// owner (the agent) is dead, so the ghost grace period applies to it.
type orphanedUploadFixture struct {
	ledgerPath, sessionDir, rawPath, recPath, marker string
}

func newOrphanedUploadFixture(t *testing.T) orphanedUploadFixture {
	t.Helper()
	ledgerPath := t.TempDir()
	sessionDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", "2026-11-19T09-00-marcus-OxKILL")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	f := orphanedUploadFixture{
		ledgerPath: ledgerPath,
		sessionDir: sessionDir,
		rawPath:    filepath.Join(sessionDir, artifactRaw),
		recPath:    filepath.Join(sessionDir, recordingMarker),
		marker:     filepath.Join(sessionDir, session.UploadRetryPendingFile),
	}
	require.NoError(t, os.WriteFile(f.rawPath, []byte(orphanedRaw), 0o600))
	require.NoError(t, session.WriteUploadRetryPending(sessionDir, "ses_019d0000-0000-7000-8000-0000000000aa"))
	staleRecordingMarker(t, sessionDir, 2*time.Minute)
	return f
}

// holdCaptureLock holds the session's raw.jsonl capture lock until the test
// ends, as a stop does from its final drain through its upload.
func holdCaptureLock(t *testing.T, rawPath string) {
	t.Helper()
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = fileutil.WithFileLock(context.Background(), rawPath, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	t.Cleanup(func() { close(release); <-done })
}

// Failure prevented: a push killed when the coding agent exited left the
// session off the Ledger until the ghost grace period ran out, up to ten
// minutes after the recording started (sageox/ox#1936).
func TestDetect_OrphanedUploadSkipsTheGhostGrace(t *testing.T) {
	f := newOrphanedUploadFixture(t)
	h := NewSessionFinalizeHandler(slog.New(slog.DiscardHandler))

	items, err := h.Detect(f.ledgerPath)
	require.NoError(t, err)
	require.Len(t, items, 1, "the orphaned upload is picked up now, not after the grace period")
	payload := items[0].Payload.(*SessionFinalizePayload)
	assert.Equal(t, f.sessionDir, payload.SessionDir)
	assert.NoFileExists(t, f.recPath, "the dead recording is reclaimed")
	raw, err := os.ReadFile(f.rawPath)
	require.NoError(t, err)
	assert.Equal(t, orphanedRaw, string(raw), "the stop's final capture is kept byte for byte")
	assert.Equal(t, "ses_019d0000-0000-7000-8000-0000000000aa", session.PendingUploadID(f.sessionDir),
		"the publication ID the stop chose survives")
}

// The grace period still holds whenever there is no proof the capture ended.
func TestDetect_GhostGraceStillHoldsWithoutAnOrphanedUpload(t *testing.T) {
	t.Run("no upload marker", func(t *testing.T) {
		f := newOrphanedUploadFixture(t)
		require.NoError(t, os.Remove(f.marker))
		items, err := NewSessionFinalizeHandler(slog.New(slog.DiscardHandler)).Detect(f.ledgerPath)
		require.NoError(t, err)
		assert.Empty(t, items)
		assert.FileExists(t, f.recPath)
	})
	t.Run("a hook appended after the marker", func(t *testing.T) {
		f := newOrphanedUploadFixture(t)
		appended := orphanedRaw + `{"type":"user","content":"one more thing","seq":2,"timestamp":"2026-11-19T09:01:00Z"}` + "\n"
		require.NoError(t, os.WriteFile(f.rawPath, []byte(appended), 0o600))
		items, err := NewSessionFinalizeHandler(slog.New(slog.DiscardHandler)).Detect(f.ledgerPath)
		require.NoError(t, err)
		assert.Empty(t, items, "the marker no longer matches: this recording may still be live")
		assert.FileExists(t, f.recPath)
	})
	t.Run("the stop is still uploading", func(t *testing.T) {
		f := newOrphanedUploadFixture(t)
		holdCaptureLock(t, f.rawPath)
		items, err := NewSessionFinalizeHandler(slog.New(slog.DiscardHandler)).Detect(f.ledgerPath)
		require.NoError(t, err)
		assert.Empty(t, items, "never race a live stop")
		assert.FileExists(t, f.recPath)
	})
}

// Failure prevented: the prompt retry publishing a session someone deleted on
// SageOx while it streamed. Deleted sessions are never reported, and the
// recording keeps the grace period it had.
func TestOrphanedUpload_DeletedOnServerIsNeverRetried(t *testing.T) {
	f := newOrphanedUploadFixture(t)
	deletedOnServer(t, f.sessionDir)
	h := NewSessionFinalizeHandler(slog.New(slog.DiscardHandler))

	assert.False(t, h.HasNewOrphanedUpload(f.ledgerPath), "a deleted session is never reported for upload")
	items, err := h.Detect(f.ledgerPath)
	require.NoError(t, err)
	assert.Empty(t, items, "and detection does not reclaim it early either")
	assert.FileExists(t, f.rawPath, "the transcript stays local")
}

func TestHasNewOrphanedUpload(t *testing.T) {
	t.Run("reported once per marker", func(t *testing.T) {
		f := newOrphanedUploadFixture(t)
		h := NewSessionFinalizeHandler(slog.New(slog.DiscardHandler))
		assert.True(t, h.HasNewOrphanedUpload(f.ledgerPath))
		assert.False(t, h.HasNewOrphanedUpload(f.ledgerPath), "a marker already reported does not trigger another scan")

		// a later attempt rewrites the marker: report it again
		later := time.Now().Add(time.Minute)
		require.NoError(t, os.Chtimes(f.marker, later, later))
		assert.True(t, h.HasNewOrphanedUpload(f.ledgerPath))
	})
	t.Run("a finished stop that failed to push", func(t *testing.T) {
		f := newOrphanedUploadFixture(t)
		require.NoError(t, os.Remove(f.recPath), "a stop that ran to the end clears the recording")
		assert.True(t, NewSessionFinalizeHandler(slog.New(slog.DiscardHandler)).HasNewOrphanedUpload(f.ledgerPath))
	})
	t.Run("waits while the stop uploads, then reports", func(t *testing.T) {
		f := newOrphanedUploadFixture(t)
		h := NewSessionFinalizeHandler(slog.New(slog.DiscardHandler))
		held, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			_ = fileutil.WithFileLock(context.Background(), f.rawPath, func() error {
				close(held)
				<-release
				return nil
			})
		}()
		<-held
		assert.False(t, h.HasNewOrphanedUpload(f.ledgerPath), "the stop holds the capture lock: still uploading")
		close(release)
		<-done
		assert.True(t, h.HasNewOrphanedUpload(f.ledgerPath), "the stop is gone and the marker is still there")
	})
	t.Run("waits while the agent still records", func(t *testing.T) {
		f := newOrphanedUploadFixture(t)
		liveRecordingMarker(t, f.sessionDir, 2*time.Minute)
		assert.False(t, NewSessionFinalizeHandler(slog.New(slog.DiscardHandler)).HasNewOrphanedUpload(f.ledgerPath))
	})
	t.Run("no marker", func(t *testing.T) {
		f := newOrphanedUploadFixture(t)
		require.NoError(t, os.Remove(f.marker))
		assert.False(t, NewSessionFinalizeHandler(slog.New(slog.DiscardHandler)).HasNewOrphanedUpload(f.ledgerPath))
	})
}

// orphanProbeHandler counts detection scans and reports an orphaned upload
// when told to.
type orphanProbeHandler struct {
	mockHandler
	orphaned bool
	detects  int
}

func (h *orphanProbeHandler) Detect(ledgerPath string) ([]*WorkItem, error) {
	h.detects++
	return h.mockHandler.Detect(ledgerPath)
}

func (h *orphanProbeHandler) HasNewOrphanedUpload(string) bool { return h.orphaned }

// Failure prevented: the daemon left a retry-pending session alone until its
// next repair scan, every five minutes at best (sageox/ox#1936). The probe
// runs detection at once, past the cooldown, and queues the work.
func TestManager_RetryOrphanedUploads_DetectsPastTheCooldown(t *testing.T) {
	item := &WorkItem{Type: sessionFinalizeType, Priority: sessionFinalizePriority, DedupKey: sessionFinalizeType + ":orphaned"}
	h := &orphanProbeHandler{mockHandler: mockHandler{typ: sessionFinalizeType, detectItems: []*WorkItem{item}}}
	m, _ := newTestManager(NewMockRunner(true), nil)
	m.ledgerPath = t.TempDir()
	m.RegisterHandler(h)
	m.lastDetect[sessionFinalizeType] = time.Now() // a repair scan just ran

	m.retryOrphanedUploads(context.Background())
	assert.Equal(t, 0, h.detects, "nothing orphaned: no scan")

	h.orphaned = true
	m.retryOrphanedUploads(context.Background())
	assert.Equal(t, 1, h.detects, "an orphaned upload is detected now, cooldown or not")
	require.Eventually(t, func() bool { return h.processCalls.Load() == 1 }, 5*time.Second, 10*time.Millisecond,
		"and its work runs without waiting for a sync or the doctor timer")
}

func TestManager_RetryOrphanedUploads_RespectsTheWorkerSwitch(t *testing.T) {
	h := &orphanProbeHandler{mockHandler: mockHandler{typ: sessionFinalizeType}, orphaned: true}
	m, _ := newTestManager(NewMockRunner(false), func() *config.AgentWorkerConfig { return disabledConfig() })
	m.ledgerPath = t.TempDir()
	m.RegisterHandler(h)
	m.retryOrphanedUploads(context.Background())
	assert.Equal(t, 0, h.detects, "a disabled worker scans nothing, as on the doctor timer")
}
