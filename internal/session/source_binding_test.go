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
