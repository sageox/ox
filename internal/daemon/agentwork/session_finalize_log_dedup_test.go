package agentwork

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturedLogger records everything at Info and above, the level a production
// daemon logs at, so a line that has dropped to Debug is invisible here exactly
// as it is in the real log.
func capturedLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})), &buf
}

// countLogLines counts lines at the given level whose message contains msg.
func countLogLines(buf *bytes.Buffer, level, msg string) int {
	n := 0
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "level="+level) && strings.Contains(line, msg) {
			n++
		}
	}
	return n
}

func TestWarnOnce_WarnsFirstThenDebugPerSiteAndSession(t *testing.T) {
	t.Parallel()

	logger, buf := capturedLogger()
	h := NewSessionFinalizeHandler(logger)

	for i := 0; i < 5; i++ {
		h.warnOnce("site-a", "s1", "condition a")
	}
	assert.Equal(t, 1, countLogLines(buf, "WARN", "condition a"), "repeats drop to Debug")

	h.warnOnce("site-a", "s2", "condition a")
	assert.Equal(t, 2, countLogLines(buf, "WARN", "condition a"), "another session is its own first occurrence")

	h.warnOnce("site-b", "s1", "condition b")
	assert.Equal(t, 1, countLogLines(buf, "WARN", "condition b"), "the same session at another site is independent")
}

// Failure prevented: a ledger with a backlog logged one INFO line per session
// per scan ("session needs upload", ~13,900 a day) on top of the scan summary.
// The summary must still say how many, so the information is not lost.
func TestDetect_UploadOnlyBacklogIsOneSummaryNotOneLinePerSession(t *testing.T) {
	logger, buf := capturedLogger()
	handler := NewSessionFinalizeHandler(logger)
	ledgerPath := t.TempDir()
	const sessions = 4
	for i := 0; i < sessions; i++ {
		writeProductionShapedSession(t, ledgerPath, "2026-01-15T10-0"+string(rune('0'+i))+"-testuser-OxBACKLOG")
	}

	for scan := 0; scan < 3; scan++ {
		items, err := handler.Detect(ledgerPath)
		require.NoError(t, err)
		require.Len(t, items, sessions)
	}

	assert.Zero(t, countLogLines(buf, "INFO", "session needs upload"), "per-session lines are Debug now")
	assert.Equal(t, 3, countLogLines(buf, "INFO", "session finalize detect complete"), "one summary per scan")
	assert.Equal(t, 3, strings.Count(buf.String(), "upload_only=4"), "the summary carries the count")
}

// Failure prevented: a session whose meta.json cannot be parsed re-announced
// itself at Warn on every detect scan (and per dead agent) for as long as it
// existed. It must warn once per daemon lifetime; the aggregate "sessions
// skipped while holding transcript content" line keeps reporting the count.
func TestDetect_UnreadableMetaWarnsOncePerSession(t *testing.T) {
	logger, buf := capturedLogger()
	handler := NewSessionFinalizeHandler(logger)
	ledgerPath := t.TempDir()
	for _, name := range []string{"2026-01-15T10-00-testuser-OxBAD1", "2026-01-15T11-00-testuser-OxBAD2"} {
		dir := filepath.Join(ledgerPath, "sessions", name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte("{not json"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(testRawContent), 0o644))
	}

	for scan := 0; scan < 5; scan++ {
		_, err := handler.Detect(ledgerPath)
		require.NoError(t, err)
	}

	assert.Equal(t, 2, countLogLines(buf, "WARN", "skipping session with unreadable meta.json"),
		"one warning per session, not one per session per scan")
	assert.Equal(t, 5, countLogLines(buf, "WARN", "sessions skipped while holding transcript content"),
		"the per-scan aggregate still reports the stranded count")
}

// Same condition on the agent-exit path, which runs per dead agent, plus the
// deferral warning when a busy capture writer holds the raw lock.
func TestDetectOrphanedForAgent_WarnsOncePerSession(t *testing.T) {
	t.Run("unreadable meta", func(t *testing.T) {
		logger, buf := capturedLogger()
		handler := NewSessionFinalizeHandler(logger)
		ledgerPath := t.TempDir()
		dir := filepath.Join(ledgerPath, "sessions", "2026-01-15T10-00-testuser-OxORPH")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte("{not json"), 0o644))

		for i := 0; i < 5; i++ {
			assert.Empty(t, handler.DetectOrphanedForAgent(ledgerPath, "OxORPH", 99999999))
		}

		assert.Equal(t, 1, countLogLines(buf, "WARN", "skipping session with unreadable meta.json"))
	})

	t.Run("recovery deferred behind a busy capture writer", func(t *testing.T) {
		logger, buf := capturedLogger()
		handler := NewSessionFinalizeHandler(logger)
		handler.captureLockWait = 20 * time.Millisecond
		ledgerPath := t.TempDir()
		sessionDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", "orphan-OxBUSY")
		require.NoError(t, os.MkdirAll(sessionDir, 0o700))
		rawPath := filepath.Join(sessionDir, artifactRaw)
		require.NoError(t, os.WriteFile(rawPath, []byte(testRawContent), 0o600))
		writeRecordingState(t, filepath.Join(sessionDir, recordingMarker), session.RecordingState{
			AgentID: "OxBUSY", ParentPID: 99999999, SessionPath: sessionDir,
			StartedAt: time.Now().Add(-time.Hour),
		})

		locked, release, held := make(chan struct{}), make(chan struct{}), make(chan struct{})
		go func() {
			defer close(held)
			_ = fileutil.WithFileLock(context.Background(), rawPath, func() error {
				close(locked)
				<-release
				return nil
			})
		}()
		t.Cleanup(func() { close(release); <-held })
		<-locked

		for i := 0; i < 4; i++ {
			assert.Empty(t, handler.DetectOrphanedForAgent(ledgerPath, "OxBUSY", 99999999))
		}

		assert.Equal(t, 1, countLogLines(buf, "WARN", "orphaned recording recovery deferred"),
			"a busy writer defers every pass; only the first announces it")
	})
}
