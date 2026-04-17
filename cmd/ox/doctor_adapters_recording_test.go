package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
)

// TestCheckRecordingAdapters_MissingAdapter verifies the doctor surfaces a
// critical finding when an active recording names an adapter that isn't
// discoverable. This catches the #519 RC hypothesis where ox-adapter-claude-code
// is missing from the user's $PATH.
// Failure prevented: silent hook no-ops on every session.
func TestCheckRecordingAdapters_MissingAdapter(t *testing.T) {
	projectRoot := t.TempDir()
	// use the legacy read-fallback path so LoadAllRecordingStates sees our state
	// without needing a full ledger/XDG setup.
	sessionsDir := filepath.Join(projectRoot, "sessions", "2026-04-17-test")
	state := &session.RecordingState{
		AgentID:     "Oxtest1",
		AdapterName: "totally-not-a-real-adapter",
		SessionPath: sessionsDir,
		StartedAt:   time.Now().UTC(),
	}
	if err := session.SaveRecordingState(projectRoot, state); err != nil {
		t.Fatalf("SaveRecordingState: %v", err)
	}

	results := checkRecordingAdapters(projectRoot)
	if len(results) == 0 {
		t.Fatal("expected at least one check result, got none")
	}

	var hit bool
	for _, r := range results {
		if strings.Contains(r.name, "totally-not-a-real-adapter") {
			hit = true
			if r.passed {
				t.Errorf("expected failed check, got passed (name=%q)", r.name)
			}
			if r.priority != "critical" {
				t.Errorf("expected priority=critical, got %q (name=%q)", r.priority, r.name)
			}
			if !strings.Contains(r.detail, "#519") {
				t.Errorf("expected detail to cross-link issue #519 for discoverability: detail=%q", r.detail)
			}
		}
	}
	if !hit {
		t.Errorf("expected a check result for the missing adapter, got %d results", len(results))
	}
}

// TestCheckRecordingAdapters_NoRecordings verifies the check is a silent no-op
// when no recordings are active — we shouldn't spam doctor output for idle
// projects.
func TestCheckRecordingAdapters_NoRecordings(t *testing.T) {
	results := checkRecordingAdapters(t.TempDir())
	if len(results) != 0 {
		t.Errorf("expected no results with no active recordings, got %d", len(results))
	}
}
