package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- GH #710 D2 call-site audit ---
//
// Making HasSubstantiveEntries reject pointer stubs is correct for the
// summarizer, but two callers meant something different by it and would
// become DESTRUCTIVE if they inherited the new semantics verbatim.
//
// Ghost cleanup is the dangerous one: it deletes .recording.json and then
// removeEmptyDir()s the session directory for anything it judges to have
// "no meaningful data". Pre-#710 a pointer stub counted as data and was
// spared. A naive flip would have made ghost cleanup delete synced
// sessions whose transcripts were safely in the content store — turning a
// summarization bug into permanent local data loss.

// ghostState builds a dead-parent recording state old enough to clear the
// grace period, so cleanup will actually consider it.
func ghostState(t *testing.T, sessionPath string) *RecordingState {
	t.Helper()
	require.NoError(t, os.MkdirAll(sessionPath, 0o755))

	state := &RecordingState{
		SessionPath: sessionPath,
		// PID 1 is init/launchd — alive, so use a PID that cannot be running.
		// 0x7FFFFFFF is above any real pid_max on Linux and macOS.
		ParentPID: 0x7FFFFFFF,
		StartedAt: time.Now().Add(-2 * GhostGracePeriod),
	}
	require.NoError(t, os.WriteFile(recordingStatePath(sessionPath), []byte("{}"), 0o644))
	return state
}

// TestGhostCleanup_PointerStubIsNotAGhost is the regression test for the
// destructive call site. It fails if ghost cleanup is ever rewired
// straight through HasSubstantiveEntries.
func TestGhostCleanup_PointerStubIsNotAGhost(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "2026-05-01T20-04-testuser-OxGHST")
	state := ghostState(t, sessionPath)
	writePointerStub(t, filepath.Join(sessionPath, "raw.jsonl"))

	result := cleanupGhosts([]*RecordingState{state})

	assert.Equal(t, 0, result.Removed,
		"a session whose transcript lives in the content store must never be deleted as a ghost")
	assert.DirExists(t, sessionPath, "the session directory must survive")
	assert.FileExists(t, recordingStatePath(sessionPath),
		"the recording marker must survive so recovery can still find it")
}

// TestGhostCleanup_StillRemovesRealGhosts proves the guard above did not
// simply disable ghost cleanup.
func TestGhostCleanup_StillRemovesRealGhosts(t *testing.T) {
	base := t.TempDir()

	headerOnly := filepath.Join(base, "2026-05-01T20-04-testuser-OxHDR")
	headerState := ghostState(t, headerOnly)
	require.NoError(t, os.WriteFile(filepath.Join(headerOnly, "raw.jsonl"),
		[]byte(`{"metadata":{},"type":"header"}`+"\n"), 0o644))

	noRaw := filepath.Join(base, "2026-05-01T20-04-testuser-OxNON")
	noRawState := ghostState(t, noRaw)

	result := cleanupGhosts([]*RecordingState{headerState, noRawState})

	assert.Equal(t, 2, result.Removed,
		"header-only and raw-less recordings are genuine ghosts and must still be cleaned")
}

// TestGhostCleanup_KeepsSessionsWithRealContent is the pre-existing
// contract, restated so a future refactor cannot quietly drop it.
func TestGhostCleanup_KeepsSessionsWithRealContent(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "2026-05-01T20-04-testuser-OxREAL")
	state := ghostState(t, sessionPath)
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, "raw.jsonl"),
		[]byte(`{"metadata":{},"type":"header"}`+"\n"+`{"type":"user","content":"hi"}`+"\n"), 0o644))

	result := cleanupGhosts([]*RecordingState{state})

	assert.Equal(t, 0, result.Removed, "a recording with entries is an orphan to recover, not a ghost")
	assert.DirExists(t, sessionPath)
}
