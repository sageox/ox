package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterruntime"
)

const boundAmpThread = "T-0b6a3c2e-7d41-4f2a-9c1e-5a8d2f6b9e10"

func ampSidecar(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(`{"type":"thread"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func startAmpRecording(t *testing.T, sidecar string) (string, *RecordingState) {
	t.Helper()
	project := setupRecordingTest(t, t.TempDir())
	state, err := StartRecording(project, StartRecordingOptions{
		AgentID: "OxBind", AdapterName: "amp", Username: "testuser",
		SessionFile: sidecar, AgentSessionID: boundAmpThread,
	})
	if err != nil {
		t.Fatal(err)
	}
	return project, state
}

func TestCheckNativeSourceBinding_AcceptsSidecarNamedForTheThread(t *testing.T) {
	_, state := startAmpRecording(t, ampSidecar(t, boundAmpThread+".jsonl"))
	if err := CheckNativeSourceBinding(state); err != nil {
		t.Fatalf("bound sidecar rejected: %v", err)
	}
	if state.SourceRejected {
		t.Fatal("bound sidecar quarantined")
	}
}

// Another thread's transcript must never be read into this recording, and
// the refusal must survive a restart: the whole recording is quarantined.
func TestCheckNativeSourceBinding_QuarantinesAnotherThreadsTranscript(t *testing.T) {
	project, state := startAmpRecording(t, ampSidecar(t, "T-other-thread.jsonl"))

	err := CheckNativeSourceBinding(state)
	if !errors.Is(err, adapterruntime.ErrUnboundSource) {
		t.Fatalf("foreign sidecar accepted: %v", err)
	}
	if !state.SourceRejected {
		t.Fatal("rejection not recorded in memory")
	}
	quarantined, err := LoadQuarantinedRecordingsForAgent(project, "OxBind")
	if err != nil || len(quarantined) != 1 || !quarantined[0].SourceRejected {
		t.Fatalf("quarantine not persisted: %+v, %v", quarantined, err)
	}
	if active, _ := LoadRecordingState(project); active != nil {
		t.Fatalf("quarantined recording still loads as active: %+v", active)
	}
	reloaded := quarantined[0]

	// A later check of the persisted state refuses before revalidating, even
	// if the path would now pass.
	reloaded.SessionFile = ampSidecar(t, boundAmpThread+".jsonl")
	err = CheckNativeSourceBinding(reloaded)
	if !errors.Is(err, adapterruntime.ErrUnboundSource) || !strings.Contains(err.Error(), "quarantined") {
		t.Fatalf("quarantined recording re-admitted: %v", err)
	}
}

// Recordings persisted before AdapterName existed only carry AgentType.
func TestCheckNativeSourceBinding_FallsBackToAgentType(t *testing.T) {
	state := &RecordingState{AgentType: "gemini", SessionFile: ampSidecar(t, "chat.json")}
	if err := CheckNativeSourceBinding(state); !errors.Is(err, adapterruntime.ErrUnboundSource) {
		t.Fatalf("legacy Gemini recording accepted: %v", err)
	}
	if !state.SourceRejected {
		t.Fatal("legacy rejection not recorded")
	}
}

// Turns captured before the sidecar was removed stay publishable.
func TestCheckNativeSourceBinding_AcceptsRemovedSidecar(t *testing.T) {
	sidecar := ampSidecar(t, boundAmpThread+".jsonl")
	_, state := startAmpRecording(t, sidecar)
	if err := os.Remove(sidecar); err != nil {
		t.Fatal(err)
	}
	if err := CheckNativeSourceBinding(state); err != nil || state.SourceRejected {
		t.Fatalf("removed bound sidecar quarantined: %v", err)
	}
}

// A later thread switch must not quarantine the first thread's captured turns.
func TestCheckNativeSourceBinding_AcceptsSidecarAfterThreadSwitch(t *testing.T) {
	_, state := startAmpRecording(t, ampSidecar(t, boundAmpThread+".jsonl"))
	state.RecordNativeSession(boundAmpThread, "startup", state.StartedAt)
	state.AgentSessionID = "T-1c7b4d3f-8e52-4a3b-8d2f-6b9e0a1c2d3e"
	if err := CheckNativeSourceBinding(state); err != nil || state.SourceRejected {
		t.Fatalf("thread switch quarantined the original sidecar: %v", err)
	}
}

// Amp may prime before its thread ID arrives; that recording is pending.
func TestCheckNativeSourceBinding_PendingAmpThreadIsNotQuarantined(t *testing.T) {
	state := &RecordingState{AdapterName: "amp"}
	if err := CheckNativeSourceBinding(state); err != nil || state.SourceRejected {
		t.Fatalf("pending Amp recording quarantined: %v", err)
	}
}

func TestCheckNativeSourceBinding_AmpThreadWithoutSidecarIsBound(t *testing.T) {
	_, state := startAmpRecording(t, "")
	if err := CheckNativeSourceBinding(state); err != nil {
		t.Fatalf("a valid thread ID with no sidecar yet must stay bound: %v", err)
	}
	if state.SourceRejected {
		t.Fatal("recording must not be quarantined")
	}
}

func TestCheckNativeSourceBinding_ReportsQuarantinePersistFailure(t *testing.T) {
	state := &RecordingState{
		AgentID:     "OxGem",
		AdapterName: "gemini",
		SessionID:   "missing",
		SessionPath: filepath.Join(t.TempDir(), "missing"),
	}
	err := CheckNativeSourceBinding(state)
	if err == nil || !strings.Contains(err.Error(), "persist source quarantine") {
		t.Fatalf("CheckNativeSourceBinding = %v, want persist failure", err)
	}
	if !state.SourceRejected {
		t.Fatal("in-memory state must still be quarantined")
	}
}
