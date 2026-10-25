package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// staleTailMarker returns a marker file whose JSON is followed by bytes from a
// longer older document, the shape left by an in-place overwrite.
func staleTailMarker(t *testing.T, tail string) (sessionDir, path string) {
	t.Helper()
	sessionDir = t.TempDir()
	path = filepath.Join(sessionDir, recordingFile)
	data, err := json.MarshalIndent(&RecordingState{AgentID: "OxTail1", SessionPath: sessionDir}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(data, []byte(tail)...), 0o600))
	return sessionDir, path
}

func requireCleanMarker(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var state RecordingState
	require.NoError(t, json.Unmarshal(data, &state), "strict parse must pass after repair")
	require.Equal(t, "OxTail1", state.AgentID)
}

func TestReadRecordingStateFile_RepairsStaleTail(t *testing.T) {
	for _, tail := range []string{"\n}}", "}}", "\n9", "\ni"} {
		t.Run(strings.TrimSpace(tail), func(t *testing.T) {
			sessionDir, path := staleTailMarker(t, tail)

			state, err := ReadRecordingStateFile(sessionDir)
			require.NoError(t, err)
			require.NotNil(t, state)
			require.Equal(t, "OxTail1", state.AgentID)
			requireCleanMarker(t, path)
		})
	}
}

func TestLoadRecordingState_RepairsStaleTail(t *testing.T) {
	project, sessionsBase := setupRecordingTestWithSessionsBase(t, t.TempDir())
	dir := filepath.Join(sessionsBase, "stale-tail")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	data, err := json.MarshalIndent(&RecordingState{AgentID: "OxTail1", SessionPath: dir}, "", "  ")
	require.NoError(t, err)
	path := filepath.Join(dir, recordingFile)
	require.NoError(t, os.WriteFile(path, append(data, []byte("\n}}")...), 0o600))

	state, err := LoadRecordingState(project)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Equal(t, "OxTail1", state.AgentID)
	requireCleanMarker(t, path)
}

func TestReadRecordingStateFile_TruncatedStillFails(t *testing.T) {
	sessionDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, recordingFile), []byte(`{"agent_id":`), 0o600))
	_, err := ReadRecordingStateFile(sessionDir)
	require.ErrorContains(t, err, "parse recording state")
}

func TestMutateRecordingStateFile_DropsStaleTail(t *testing.T) {
	_, path := staleTailMarker(t, "\n}}")

	require.NoError(t, MutateRecordingStateFile(path, func(s *RecordingState) error {
		s.EntryCount = 7
		return nil
	}))

	requireCleanMarker(t, path)
}

// a shrinking save over a longer marker, with a reader racing it, must never
// leave or expose a stale tail.
func TestSaveRecordingState_ShrinkingOverwriteNeverTears(t *testing.T) {
	project, sessionsBase := setupRecordingTestWithSessionsBase(t, t.TempDir())
	sessionDir := filepath.Join(sessionsBase, "shrink")
	long := &RecordingState{AgentID: "OxTail1", SessionPath: sessionDir, OutputFile: strings.Repeat("x", 4096)}
	short := &RecordingState{AgentID: "OxTail1", SessionPath: sessionDir}
	require.NoError(t, SaveRecordingState(project, long))
	path := filepath.Join(sessionDir, recordingFile)

	deadline := time.Now().Add(300 * time.Millisecond)
	var wg sync.WaitGroup
	for _, st := range []*RecordingState{long, short} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				if err := SaveRecordingState(project, st); err != nil {
					t.Errorf("save: %v", err)
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for time.Now().Before(deadline) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("read: %v", err)
				return
			}
			var s RecordingState
			if err := json.Unmarshal(data, &s); err != nil {
				t.Errorf("reader saw a torn marker: %v", err)
				return
			}
		}
	}()
	wg.Wait()
	requireCleanMarker(t, path)
}
