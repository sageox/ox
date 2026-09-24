package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupPreservesUndiscoveredClaudeHookSource(t *testing.T) {
	project := setupRecordingTest(t, t.TempDir())
	state, err := StartRecording(project, StartRecordingOptions{
		AgentID: "OxUnfound", AdapterName: "claude-code", WatchMode: "hook", Username: "testuser",
	})
	if err != nil {
		t.Fatal(err)
	}
	state.StartedAt = time.Now().Add(-72 * time.Hour)
	state.ParentPID = 999999999 // dead process: both cleanup paths would otherwise remove a header-only stub
	if err := SaveRecordingState(project, state); err != nil {
		t.Fatal(err)
	}
	cleanupStaleEmptyRecordings(project)
	if removed := cleanupGhosts([]*RecordingState{state}); removed.Removed != 0 {
		t.Fatalf("ghost cleanup removed uncertain recording: %+v", removed)
	}
	if _, err := os.Stat(filepath.Join(state.SessionPath, recordingFile)); err != nil {
		t.Fatalf("undiscovered native source must keep its marker: %v", err)
	}
}

func TestStartRecordingPreservesQuarantinedSource(t *testing.T) {
	project := setupRecordingTest(t, t.TempDir())
	native := filepath.Join(t.TempDir(), "native.jsonl")
	if err := os.WriteFile(native, []byte(`{"type":"session"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := StartRecordingOptions{
		AgentID: "OxQuarantine", AdapterName: "claude-code", Username: "testuser",
		SessionFile: native,
	}
	state, err := StartRecording(project, opts)
	if err != nil {
		t.Fatal(err)
	}
	state.SourceRejected = true
	state.StopIncomplete = true
	if err := SaveRecordingState(project, state); err != nil {
		t.Fatal(err)
	}
	if _, err := StartRecording(project, opts); !errors.Is(err, ErrAlreadyRecording) {
		t.Fatalf("prime must not replace quarantined recording: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state.SessionPath, recordingFile)); err != nil {
		t.Fatalf("quarantined marker was lost: %v", err)
	}
}
