package agentwork

import (
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
)

func TestDetect(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	// create one incomplete session (missing all artifacts) and one complete session
	ledgerPath := t.TempDir()
	sessionsDir := filepath.Join(ledgerPath, "sessions")

	// incomplete session
	incompleteDir := filepath.Join(sessionsDir, "2026-01-06T14-32-testuser-Ox1234")
	if err := os.MkdirAll(incompleteDir, 0755); err != nil {
		t.Fatal(err)
	}
	rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code"}}
{"type":"user","content":"hello","seq":1}
{"type":"assistant","content":"hi there","seq":2}
`
	if err := os.WriteFile(filepath.Join(incompleteDir, "raw.jsonl"), []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}

	// complete session
	completeDir := filepath.Join(sessionsDir, "2026-01-06T15-00-testuser-Ox5678")
	if err := os.MkdirAll(completeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(completeDir, "raw.jsonl"), []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range requiredArtifacts {
		if err := os.WriteFile(filepath.Join(completeDir, name), []byte("done"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 incomplete session, got %d", len(items))
	}

	item := items[0]
	if item.Type != sessionFinalizeType {
		t.Errorf("expected type %q, got %q", sessionFinalizeType, item.Type)
	}
	if item.Priority != sessionFinalizePriority {
		t.Errorf("expected priority %d, got %d", sessionFinalizePriority, item.Priority)
	}

	payload, ok := item.Payload.(*SessionFinalizePayload)
	if !ok {
		t.Fatalf("payload is not *SessionFinalizePayload: %T", item.Payload)
	}
	if len(payload.Missing) != len(requiredArtifacts) {
		t.Errorf("expected %d missing artifacts, got %d: %v", len(requiredArtifacts), len(payload.Missing), payload.Missing)
	}
}

func TestDetect_NoSessions(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	// empty ledger with no sessions/ dir
	ledgerPath := t.TempDir()
	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items for missing sessions dir, got %d", len(items))
	}

	// create empty sessions/ dir
	if err := os.MkdirAll(filepath.Join(ledgerPath, "sessions"), 0755); err != nil {
		t.Fatal(err)
	}
	items, err = handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items for empty sessions dir, got %d", len(items))
	}
}

func TestDetect_SkipsInvalidSessions(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	ledgerPath := t.TempDir()
	sessionsDir := filepath.Join(ledgerPath, "sessions")

	// session dir with no raw.jsonl
	noRawDir := filepath.Join(sessionsDir, "2026-01-06T14-32-testuser-NoRaw")
	if err := os.MkdirAll(noRawDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noRawDir, "summary.md"), []byte("orphan"), 0644); err != nil {
		t.Fatal(err)
	}

	// legacy dirs that should be skipped
	for _, name := range []string{"raw", "events"} {
		d := filepath.Join(sessionsDir, name)
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}

	// regular file (not a dir)
	if err := os.WriteFile(filepath.Join(sessionsDir, "stray-file.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items for invalid sessions, got %d", len(items))
	}
}

func TestDetect_SkipsLegacyDirs(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	ledgerPath := t.TempDir()
	sessionsDir := filepath.Join(ledgerPath, "sessions")

	// "raw" and "events" are legacy dirs that should be ignored
	for _, name := range []string{"raw", "events"} {
		d := filepath.Join(sessionsDir, name)
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "raw.jsonl"), []byte(`{"type":"user","content":"x"}`), 0644); err != nil {
			t.Fatal(err)
		}
	}

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
}

// TestDetect_StaleRecordingWithRaw verifies that a session abandoned by Ctrl-C
// (has .recording.json + raw.jsonl, but no session stop) is detected and
// finalized after the stale threshold. This is the core anti-entropy test.
func TestDetect_StaleRecordingWithRaw(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	ledgerPath := t.TempDir()
	sessionName := "2026-01-10T09-00-testuser-OxCTRL"
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	// raw.jsonl with real content (as if hooks wrote entries before Ctrl-C)
	rawContent := `{"metadata":{"agent_id":"OxCTRL","agent_type":"claude","version":"1.0"},"type":"header"}
{"type":"user","content":"fix the login bug","seq":0,"timestamp":"2026-01-10T09:00:01Z"}
{"type":"tool","content":"","seq":1,"timestamp":"2026-01-10T09:00:05Z","tool_name":"Read","tool_input":"{\"file_path\":\"/src/auth.go\"}"}
{"type":"assistant","content":"I see the issue in the auth handler.","seq":2,"timestamp":"2026-01-10T09:00:08Z"}
`
	if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}

	// .recording.json with StartedAt > 24h ago (simulates abandoned session)
	recState := map[string]any{
		"started_at": time.Now().Add(-25 * time.Hour).Format(time.RFC3339),
		"agent_id":   "OxCTRL",
		"session_id": "test-ctrl-c-session",
	}
	recData, _ := json.Marshal(recState)
	recPath := filepath.Join(sessionDir, recordingMarker)
	if err := os.WriteFile(recPath, recData, 0644); err != nil {
		t.Fatal(err)
	}

	// Detect should find this stale session
	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 stale session, got %d", len(items))
	}

	// .recording.json should have been removed by Detect
	if _, statErr := os.Stat(recPath); !os.IsNotExist(statErr) {
		t.Error(".recording.json should have been removed after stale detection")
	}

	// payload should reference the correct session
	payload, ok := items[0].Payload.(*SessionFinalizePayload)
	if !ok {
		t.Fatalf("unexpected payload type: %T", items[0].Payload)
	}
	if payload.SessionDir != sessionDir {
		t.Errorf("session dir mismatch: got %q", payload.SessionDir)
	}
	if len(payload.Missing) != len(requiredArtifacts) {
		t.Errorf("expected %d missing artifacts, got %d", len(requiredArtifacts), len(payload.Missing))
	}
}

// TestDetect_RecentRecordingSkipped verifies that active sessions (< 24h old)
// are NOT finalized — they're still in progress.
func TestDetect_RecentRecordingSkipped(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	ledgerPath := t.TempDir()
	sessionName := "2026-01-10T10-00-testuser-OxACTV"
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	// raw.jsonl exists
	rawContent := `{"metadata":{"agent_id":"OxACTV","agent_type":"claude","version":"1.0"},"type":"header"}
{"type":"user","content":"hello","seq":0,"timestamp":"2026-01-10T10:00:01Z"}
`
	if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}

	// .recording.json is recent (1 hour ago — well within 24h threshold)
	recState := map[string]any{
		"started_at": time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
		"agent_id":   "OxACTV",
	}
	recData, _ := json.Marshal(recState)
	recPath := filepath.Join(sessionDir, recordingMarker)
	if err := os.WriteFile(recPath, recData, 0644); err != nil {
		t.Fatal(err)
	}

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(items) != 0 {
		t.Errorf("expected 0 items for active session, got %d", len(items))
	}

	// .recording.json should still exist (not removed)
	if _, statErr := os.Stat(recPath); statErr != nil {
		t.Error(".recording.json should still exist for active sessions")
	}
}

// TestDetect_StaleRecordingWithoutRaw verifies that stale recordings with no
// raw.jsonl and no recoverable session file are skipped and their marker is cleared.
func TestDetect_StaleRecordingWithoutRaw(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	ledgerPath := t.TempDir()
	sessionName := "2026-01-10T08-00-testuser-OxNORA"
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	// no raw.jsonl — just the marker (no session_file means recovery will fail)
	recState := map[string]any{
		"started_at": time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
		"agent_id":   "OxNORA",
	}
	recData, _ := json.Marshal(recState)
	recPath := filepath.Join(sessionDir, recordingMarker)
	if err := os.WriteFile(recPath, recData, 0644); err != nil {
		t.Fatal(err)
	}

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(items) != 0 {
		t.Errorf("expected 0 items for stale recording without raw.jsonl, got %d", len(items))
	}

	// marker should be cleared for unrecoverable stale recordings
	if _, statErr := os.Stat(recPath); !os.IsNotExist(statErr) {
		t.Error(".recording.json should have been removed for unrecoverable stale recording")
	}
}

// TestDetect_DeadPID verifies that a recording with a dead parent PID is
// detected as stale once past the ghost grace period.
func TestDetect_DeadPID(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	pid := deadPID(t)

	// started_at must be older than GhostGracePeriod (10min) so the grace
	// period doesn't shield it — dead PID should trigger stale detection.
	recState := map[string]any{
		"started_at": time.Now().Add(-15 * time.Minute).Format(time.RFC3339),
		"agent_id":   "OxDEAD",
		"parent_pid": pid,
	}
	ledgerPath, _, recPath := setupRecordingSession(t, "2026-03-13T10-00-testuser-OxDEAD", recState)

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 stale session (dead PID), got %d", len(items))
	}

	// .recording.json should be removed
	if _, statErr := os.Stat(recPath); !os.IsNotExist(statErr) {
		t.Error(".recording.json should have been removed for dead PID session")
	}

	payload, ok := items[0].Payload.(*SessionFinalizePayload)
	if !ok {
		t.Fatalf("unexpected payload type: %T", items[0].Payload)
	}
	if len(payload.Missing) != len(requiredArtifacts) {
		t.Errorf("expected %d missing artifacts, got %d", len(requiredArtifacts), len(payload.Missing))
	}
}

// TestDetect_LivePID verifies that a recording with a live parent PID and
// recent start time is NOT considered stale. The live PID confirms the process
// is still running, and the recent timestamp is within the 24h threshold.
func TestDetect_LivePID(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	// use our own PID — guaranteed alive, with recent start time
	recState := map[string]any{
		"started_at": time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
		"agent_id":   "OxLIVE",
		"parent_pid": os.Getpid(),
	}
	ledgerPath, _, recPath := setupRecordingSession(t, "2026-03-13T10-00-testuser-OxLIVE", recState)

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(items) != 0 {
		t.Errorf("expected 0 items for live PID session, got %d", len(items))
	}

	// .recording.json must still exist
	if _, statErr := os.Stat(recPath); statErr != nil {
		t.Error(".recording.json should still exist for live PID session")
	}
}

// TestDetect_LivePID_OldSession verifies that a live PID is NEVER considered
// stale, regardless of session age. The daemon waits for the PID to die before
// finalizing — false positives (PID reuse) are acceptable, premature
// finalization of an active session is not.
func TestDetect_LivePID_OldSession(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	// live PID but started_at > 24h ago
	recState := map[string]any{
		"started_at": time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
		"agent_id":   "OxLVOL",
		"parent_pid": os.Getpid(),
	}
	ledgerPath, _, recPath := setupRecordingSession(t, "2026-03-13T10-00-testuser-OxLVOL", recState)

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	// live PID → not stale, even if age exceeds threshold
	if len(items) != 0 {
		t.Fatalf("expected 0 items (live PID should never be stale), got %d", len(items))
	}

	if _, statErr := os.Stat(recPath); os.IsNotExist(statErr) {
		t.Error(".recording.json should still exist (live PID = not stale)")
	}
}

// TestDetect_NoPID_Old verifies that a recording without a parent PID
// falls back to the time-based threshold: old sessions are stale.
func TestDetect_NoPID_Old(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	recState := map[string]any{
		"started_at": time.Now().Add(-25 * time.Hour).Format(time.RFC3339),
		"agent_id":   "OxNOPO",
	}
	ledgerPath, _, recPath := setupRecordingSession(t, "2026-03-13T10-00-testuser-OxNOPO", recState)

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 stale session (no PID, old), got %d", len(items))
	}

	if _, statErr := os.Stat(recPath); !os.IsNotExist(statErr) {
		t.Error(".recording.json should have been removed for stale no-PID session")
	}
}

// TestDetect_NoPID_Recent verifies that a recording without a parent PID
// but within the time threshold is NOT considered stale.
func TestDetect_NoPID_Recent(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	recState := map[string]any{
		"started_at": time.Now().Add(-30 * time.Minute).Format(time.RFC3339),
		"agent_id":   "OxNOPR",
	}
	ledgerPath, _, recPath := setupRecordingSession(t, "2026-03-13T10-00-testuser-OxNOPR", recState)

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(items) != 0 {
		t.Errorf("expected 0 items for recent no-PID session, got %d", len(items))
	}

	if _, statErr := os.Stat(recPath); statErr != nil {
		t.Error(".recording.json should still exist for recent no-PID session")
	}
}

// TestDetect_CorruptRecording verifies that a corrupt .recording.json (invalid
// JSON) falls back to mod time for staleness. We set the mod time to be old
// enough to trigger the threshold.
func TestDetect_CorruptRecording(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	ledgerPath := t.TempDir()
	sessionName := "2026-03-13T10-00-testuser-OxCRPT"
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code"}}
{"type":"user","content":"hello","seq":1}
`
	if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}

	// write corrupt JSON
	recPath := filepath.Join(sessionDir, recordingMarker)
	if err := os.WriteFile(recPath, []byte("{not valid json!!!"), 0644); err != nil {
		t.Fatal(err)
	}

	// backdate mod time to trigger threshold
	oldTime := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(recPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 stale session (corrupt JSON, old mod time), got %d", len(items))
	}

	if _, statErr := os.Stat(recPath); !os.IsNotExist(statErr) {
		t.Error(".recording.json should have been removed for stale corrupt recording")
	}
}

// TestDetect_PIDLookupFallback verifies that when .recording.json has no
// parent_pid but the pidLookup function returns a PID for the agent ID,
// that PID is used for liveness detection.
func TestDetect_PIDLookupFallback(t *testing.T) {
	t.Run("lookup returns dead PID", func(t *testing.T) {
		handler := NewSessionFinalizeHandler(slog.Default())

		pid := deadPID(t)
		handler.SetPIDLookup(func(agentID string) int {
			if agentID == "OxLKDY" {
				return pid
			}
			return 0
		})

		// no parent_pid in recording; must be past ghost grace period (10min)
		recState := map[string]any{
			"started_at": time.Now().Add(-15 * time.Minute).Format(time.RFC3339),
			"agent_id":   "OxLKDY",
		}
		ledgerPath, _, recPath := setupRecordingSession(t, "2026-03-13T10-00-testuser-OxLKDY", recState)

		items, err := handler.Detect(ledgerPath)
		if err != nil {
			t.Fatalf("Detect failed: %v", err)
		}

		if len(items) != 1 {
			t.Fatalf("expected 1 stale session (pidLookup dead PID), got %d", len(items))
		}

		if _, statErr := os.Stat(recPath); !os.IsNotExist(statErr) {
			t.Error(".recording.json should have been removed")
		}
	})

	t.Run("lookup returns live PID recent session", func(t *testing.T) {
		handler := NewSessionFinalizeHandler(slog.Default())

		handler.SetPIDLookup(func(agentID string) int {
			if agentID == "OxLKLV" {
				return os.Getpid()
			}
			return 0
		})

		// no parent_pid, recent timestamp + live PID from lookup = not stale
		recState := map[string]any{
			"started_at": time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
			"agent_id":   "OxLKLV",
		}
		ledgerPath, _, recPath := setupRecordingSession(t, "2026-03-13T10-00-testuser-OxLKLV", recState)

		items, err := handler.Detect(ledgerPath)
		if err != nil {
			t.Fatalf("Detect failed: %v", err)
		}

		if len(items) != 0 {
			t.Errorf("expected 0 items for live PID via lookup, got %d", len(items))
		}

		if _, statErr := os.Stat(recPath); statErr != nil {
			t.Error(".recording.json should still exist")
		}
	})

	t.Run("lookup returns zero falls back to time", func(t *testing.T) {
		handler := NewSessionFinalizeHandler(slog.Default())

		handler.SetPIDLookup(func(agentID string) int {
			return 0 // unknown agent
		})

		// no parent_pid, lookup returns 0, old timestamp → stale via time
		recState := map[string]any{
			"started_at": time.Now().Add(-25 * time.Hour).Format(time.RFC3339),
			"agent_id":   "OxLKZR",
		}
		ledgerPath, _, _ := setupRecordingSession(t, "2026-03-13T10-00-testuser-OxLKZR", recState)

		items, err := handler.Detect(ledgerPath)
		if err != nil {
			t.Fatalf("Detect failed: %v", err)
		}

		if len(items) != 1 {
			t.Fatalf("expected 1 stale session (lookup zero, old), got %d", len(items))
		}
	})
}

// TestHasSubstantiveEntries verifies the guard that prevents header-only
// raw.jsonl files from being finalized. This is the regression test for the
// zero-entry upload bug: sessions with only a metadata header were being
// uploaded to the ledger, creating ghost stubs.
func TestHasSubstantiveEntries(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "header plus entries",
			content: "{\"metadata\":{\"agent_id\":\"Ox1\"}}\n{\"type\":\"user\",\"content\":\"hello\"}\n",
			want:    true,
		},
		{
			name:    "header only",
			content: "{\"metadata\":{\"agent_id\":\"Ox1\"}}\n",
			want:    false,
		},
		{
			name:    "empty file",
			content: "",
			want:    false,
		},
		{
			name:    "nonexistent file",
			content: "", // won't be written
			want:    false,
		},
		{
			name:    "multi-turn session",
			content: "{\"metadata\":{}}\n{\"type\":\"user\"}\n{\"type\":\"assistant\"}\n{\"type\":\"tool\"}\n",
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "nonexistent file" {
				if session.HasSubstantiveEntries("/nonexistent/raw.jsonl") {
					t.Error("nonexistent file should return false")
				}
				return
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "raw.jsonl")
			if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}
			got := session.HasSubstantiveEntries(path)
			if got != tt.want {
				t.Errorf("session.HasSubstantiveEntries() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDetect_SkipsHeaderOnlySessions verifies that Detect skips sessions where
// raw.jsonl exists but contains only the metadata header (zero substantive entries).
// These are ghost sessions created by ox session start without any actual work.
func TestDetect_SkipsHeaderOnlySessions(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	ledgerPath := t.TempDir()
	sessionName := "2026-01-10T09-00-testuser-OxHDR0"
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	// raw.jsonl with ONLY the metadata header — no real session content
	headerOnly := `{"metadata":{"agent_id":"OxHDR0","agent_type":"claude","version":"1.0"},"type":"header"}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(headerOnly), 0644); err != nil {
		t.Fatal(err)
	}

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(items) != 0 {
		t.Errorf("expected 0 items for header-only session, got %d — "+
			"header-only sessions should be skipped to prevent ghost stubs in the ledger", len(items))
	}
}

// TestDetect_ConcurrentRemoval verifies that Detect handles gracefully the
// race condition where .recording.json disappears mid-scan (e.g., concurrent
// `ox session stop`). No panics, no corrupt results.
func TestDetect_ConcurrentRemoval(t *testing.T) {
	const iterations = 50

	for i := 0; i < iterations; i++ {
		ledgerPath := t.TempDir()
		sessionName := "2026-03-13T10-00-testuser-OxRACE"
		sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
		if err := os.MkdirAll(sessionDir, 0755); err != nil {
			t.Fatal(err)
		}

		rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code"}}
{"type":"user","content":"hello","seq":1}
`
		if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(rawContent), 0644); err != nil {
			t.Fatal(err)
		}

		// write a stale recording that Detect will try to process
		recState := map[string]any{
			"started_at": time.Now().Add(-25 * time.Hour).Format(time.RFC3339),
			"agent_id":   "OxRACE",
		}
		recData, _ := json.Marshal(recState)
		recPath := filepath.Join(sessionDir, recordingMarker)
		if err := os.WriteFile(recPath, recData, 0644); err != nil {
			t.Fatal(err)
		}

		handler := NewSessionFinalizeHandler(slog.Default())

		var wg sync.WaitGroup
		wg.Add(2)

		var detectErr error
		var detectItems []*WorkItem

		// goroutine 1: run Detect
		go func() {
			defer wg.Done()
			detectItems, detectErr = handler.Detect(ledgerPath)
		}()

		// goroutine 2: remove .recording.json concurrently
		go func() {
			defer wg.Done()
			// small jitter to increase chance of hitting the race window
			os.Remove(recPath)
		}()

		wg.Wait()

		if detectErr != nil {
			t.Fatalf("iteration %d: Detect returned error: %v", i, detectErr)
		}

		// valid outcomes: 0 items (marker gone before Detect read it, so session
		// looks like a normal incomplete session OR Detect read it but Remove
		// failed because it was already gone) or 1 item (Detect processed it
		// before removal). Both are fine — the key is no panic or error.
		if len(detectItems) > 1 {
			t.Fatalf("iteration %d: expected 0 or 1 items, got %d", i, len(detectItems))
		}
	}
}

func TestSetQualityThresholds(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	// defaults
	if handler.qualityUploadThreshold != 0.3 {
		t.Errorf("expected default upload threshold 0.3, got %f", handler.qualityUploadThreshold)
	}
	if handler.qualityDiscardThreshold != 0.1 {
		t.Errorf("expected default discard threshold 0.1, got %f", handler.qualityDiscardThreshold)
	}

	// custom
	handler.SetQualityThresholds(0.6, 0.2)
	if handler.qualityUploadThreshold != 0.6 {
		t.Errorf("expected upload threshold 0.6, got %f", handler.qualityUploadThreshold)
	}
	if handler.qualityDiscardThreshold != 0.2 {
		t.Errorf("expected discard threshold 0.2, got %f", handler.qualityDiscardThreshold)
	}
}

// deadPID runs a short-lived process and returns its PID after it has exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start process for dead PID: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("process wait failed: %v", err)
	}
	return pid
}

// setupRecordingSession creates a session dir with raw.jsonl and .recording.json.
// Returns (ledgerPath, sessionDir, recPath).
func setupRecordingSession(t *testing.T, sessionName string, recState map[string]any) (string, string, string) {
	t.Helper()
	ledgerPath := t.TempDir()
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code"}}
{"type":"user","content":"hello","seq":1}
{"type":"assistant","content":"hi there","seq":2}
`
	if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}

	recData, _ := json.Marshal(recState)
	recPath := filepath.Join(sessionDir, recordingMarker)
	if err := os.WriteFile(recPath, recData, 0644); err != nil {
		t.Fatal(err)
	}

	return ledgerPath, sessionDir, recPath
}

// TestDetect_NeedsSummaryMarkerTriggersRefinalization verifies that sessions with
// all artifact files present but a .needs-summary marker are enqueued for LLM
// summarization. Without this, stub artifacts written by "ox session stop" would
// be treated as final and the daemon would never regenerate them with the LLM.
func TestDetect_NeedsSummaryMarkerTriggersRefinalization(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	ledgerPath := t.TempDir()
	sessionsDir := filepath.Join(ledgerPath, "sessions")

	// session with all artifacts AND a .needs-summary marker (stub data from stop)
	stubSession := "2026-04-01T10-00-testuser-OxSTUB"
	stubDir := filepath.Join(sessionsDir, stubSession)
	if err := os.MkdirAll(stubDir, 0755); err != nil {
		t.Fatal(err)
	}
	rawContent := "{\"_meta\":{\"schema_version\":\"1\",\"agent_type\":\"claude-code\"}}\n{\"type\":\"user\",\"content\":\"hello\",\"seq\":1}\n{\"type\":\"assistant\",\"content\":\"hi\",\"seq\":2}\n"
	if err := os.WriteFile(filepath.Join(stubDir, "raw.jsonl"), []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}
	// write all required artifacts (stubs)
	for _, name := range requiredArtifacts {
		if err := os.WriteFile(filepath.Join(stubDir, name), []byte("stub"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// write the .needs-summary marker
	if err := session.WriteNeedsSummaryMarker(stubDir, filepath.Join(stubDir, "raw.jsonl"), ""); err != nil {
		t.Fatal(err)
	}

	// fully finalized session (all artifacts, NO marker)
	doneSession := "2026-04-01T11-00-testuser-OxDONE"
	doneDir := filepath.Join(sessionsDir, doneSession)
	if err := os.MkdirAll(doneDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(doneDir, "raw.jsonl"), []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range requiredArtifacts {
		if err := os.WriteFile(filepath.Join(doneDir, name), []byte("done"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	// only the stub session (with marker) should be enqueued
	if len(items) != 1 {
		t.Fatalf("expected 1 item (stub session needing LLM), got %d", len(items))
	}

	payload := items[0].Payload.(*SessionFinalizePayload)
	if filepath.Base(payload.SessionDir) != stubSession {
		t.Errorf("expected stub session, got %s", filepath.Base(payload.SessionDir))
	}
	if payload.UploadOnly {
		t.Error("stub session should NOT be upload-only, it needs LLM summarization")
	}
	if len(payload.Missing) != len(requiredArtifacts) {
		t.Errorf("expected all artifacts in Missing (for regeneration), got %d: %v", len(payload.Missing), payload.Missing)
	}
}
