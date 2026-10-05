package session

import (
	"encoding/json"
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
	_, err = StartRecording(project, opts)
	if !errors.Is(err, ErrQuarantinedSessionPath) {
		t.Fatalf("a start landing in the quarantined recording's folder must be refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state.SessionPath, recordingFile)); err != nil {
		t.Fatalf("quarantined marker was lost: %v", err)
	}
	held, err := LoadQuarantinedRecordingsForAgent(project, "OxQuarantine")
	if err != nil || len(held) != 1 || !held[0].SourceRejected {
		t.Fatalf("the quarantined recording must stay findable: held=%v err=%v", held, err)
	}
}

// A quarantined recording is held for review, not recording: it must not be the
// agent's active recording, and the agent must be free to start the next one.
func TestQuarantinedRecordingLeavesTheAgentsActiveSlotFree(t *testing.T) {
	project := setupRecordingTest(t, t.TempDir())
	opts := StartRecordingOptions{AgentID: "OxSlot", AdapterName: "claude-code", Username: "testuser"}
	old, err := StartRecording(project, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := MarkSourceRejected(project, "OxSlot"); err != nil {
		t.Fatal(err)
	}
	active, err := LoadRecordingStateForAgent(project, "OxSlot")
	if err != nil || active != nil {
		t.Fatalf("a quarantined recording is not the agent's active one: %v err=%v", active, err)
	}
	if IsRecordingForAgent(project, "OxSlot") {
		t.Fatal("a quarantined recording must not count as recording")
	}

	// a minute later the agent starts its next recording beside the held one
	earlier := filepath.Join(filepath.Dir(old.SessionPath), "2001-02-03T04-05-testuser-OxSlot")
	if err := os.Rename(old.SessionPath, earlier); err != nil {
		t.Fatal(err)
	}
	if err := MutateRecordingStateFile(filepath.Join(earlier, recordingFile), func(s *RecordingState) error {
		s.SessionPath = earlier
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fresh, err := StartRecording(project, opts)
	if err != nil {
		t.Fatalf("the agent must be able to record again: %v", err)
	}
	active, err = LoadRecordingStateForAgent(project, "OxSlot")
	if err != nil || active == nil || active.SessionID != fresh.SessionID {
		t.Fatalf("the active recording is the new one: %v err=%v", active, err)
	}
	held, err := LoadQuarantinedRecordingsForAgent(project, "OxSlot")
	if err != nil || len(held) != 1 || held[0].SessionID != old.SessionID {
		t.Fatalf("the held recording must survive untouched: %v err=%v", held, err)
	}
	if _, err := LoadQuarantinedRecordingsForAgent(project, ""); !errors.Is(err, ErrEmptyPath) {
		t.Fatalf("an empty agent ID is refused: %v", err)
	}
}

func TestLoadQuarantinedRecordingsForAgentListsNewestFirst(t *testing.T) {
	project := setupRecordingTest(t, t.TempDir())
	base, err := StartRecording(project, StartRecordingOptions{AgentID: "OxOrder", AdapterName: "claude-code", Username: "testuser"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ClearRecordingStateAt(base.SessionPath, base.SessionID); err != nil {
		t.Fatal(err)
	}
	for i, age := range []time.Duration{3 * time.Hour, time.Hour, 2 * time.Hour} {
		dir := filepath.Join(filepath.Dir(base.SessionPath), "2001-02-03T04-0"+string(rune('1'+i))+"-testuser-OxOrder")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := SaveRecordingState(project, &RecordingState{
			AgentID: "OxOrder", SessionID: dir, SessionPath: dir, SourceRejected: true, StartedAt: time.Now().Add(-age),
		}); err != nil {
			t.Fatal(err)
		}
	}
	held, err := LoadQuarantinedRecordingsForAgent(project, "OxOrder")
	if err != nil || len(held) != 3 {
		t.Fatalf("held=%d err=%v", len(held), err)
	}
	if !held[0].StartedAt.After(held[1].StartedAt) || !held[1].StartedAt.After(held[2].StartedAt) {
		t.Fatalf("newest first expected: %v %v %v", held[0].StartedAt, held[1].StartedAt, held[2].StartedAt)
	}
}

// Quarantine is keyed to the recording, not the agent: once a newer recording
// holds the agent's slot an agent lookup would mark the wrong one.
func TestSetSourceRejectedAtRefusesAReplacedRecording(t *testing.T) {
	project := setupRecordingTest(t, t.TempDir())
	state, err := StartRecording(project, StartRecordingOptions{AgentID: "OxKey", AdapterName: "claude-code", Username: "testuser"})
	if err != nil {
		t.Fatal(err)
	}
	if err := SetSourceRejectedAt(state.SessionPath, state.SessionID+"-other", true); !errors.Is(err, ErrRecordingChanged) {
		t.Fatalf("a different recording must not be quarantined by mistake: %v", err)
	}
	loaded, err := LoadRecordingStateForAgent(project, "OxKey")
	if err != nil || loaded == nil || loaded.SourceRejected {
		t.Fatalf("the recording must be unchanged: %v err=%v", loaded, err)
	}
	if err := SetSourceRejectedAt(state.SessionPath, state.SessionID, true); err != nil {
		t.Fatal(err)
	}
	if err := SetSourceRejectedAt(state.SessionPath, state.SessionID, false); err != nil {
		t.Fatal(err)
	}
	if loaded, _ = LoadRecordingStateForAgent(project, "OxKey"); loaded == nil || loaded.SourceRejected {
		t.Fatalf("release must put the recording back in the agent's slot: %v", loaded)
	}
}

// Commands that look up "the" recording without an agent (a human's commit, the
// first-match loader) must not pick up one that is held for review.
func TestQuarantinedRecordingIsNotFoundAsTheRecording(t *testing.T) {
	project := setupRecordingTest(t, t.TempDir())
	workspace := t.TempDir()
	state, err := StartRecording(project, StartRecordingOptions{
		AgentID: "OxLookup", AdapterName: "claude-code", Username: "testuser", WorkspacePath: workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	if found, err := LoadRecordingState(project); err != nil || found == nil {
		t.Fatalf("an ordinary recording is found: %v err=%v", found, err)
	}
	if found, err := LoadRecordingStateForWorkspace(project, workspace); err != nil || found == nil {
		t.Fatalf("an ordinary recording is found by workspace: %v err=%v", found, err)
	}
	if err := SetSourceRejectedAt(state.SessionPath, state.SessionID, true); err != nil {
		t.Fatal(err)
	}
	if found, err := LoadRecordingState(project); err != nil || found != nil {
		t.Fatalf("a quarantined recording is not the recording: %v err=%v", found, err)
	}
	if found, err := LoadRecordingStateForWorkspace(project, workspace); err != nil || found != nil {
		t.Fatalf("a quarantined recording must not collect a human's commits: %v err=%v", found, err)
	}
}

// The phantom-stub sweep reaps header-only folders whose agent is gone. A
// quarantined recording is evidence held for review however little it holds.
func TestOrphanedStubSweepKeepsAQuarantinedRecording(t *testing.T) {
	sessions := t.TempDir()
	dir := filepath.Join(sessions, "2001-02-03T04-05-testuser-OxHeld")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(`{"type":"header","metadata":{}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := &RecordingState{
		AgentID: "OxHeld", AdapterName: "claude-code", SessionPath: dir,
		ParentPID: 999999999, // dead
	}
	save := func() {
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, recordingFile), data, 0o644); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-3 * time.Hour)
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}

	state.SourceRejected = true
	save()
	if res := CleanupOrphanedStubsInDir(sessions); res.Removed != 0 {
		t.Fatalf("a quarantined recording must not be swept: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, recordingFile)); err != nil {
		t.Fatalf("the quarantine marker must survive: %v", err)
	}

	// the same folder, not quarantined, is the phantom the sweep exists for
	state.SourceRejected = false
	save()
	if res := CleanupOrphanedStubsInDir(sessions); res.Removed != 1 {
		t.Fatalf("the sweep must still reap an ordinary dead header-only stub: %+v", res)
	}
}
