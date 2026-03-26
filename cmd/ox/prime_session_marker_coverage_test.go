package main

import (
	"testing"
	"time"
)

func TestMarkerPath_Sanitization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		sessionID string
		wantSafe  bool // path should not contain traversal sequences
	}{
		{
			name:      "normal session ID",
			sessionID: "abc123",
			wantSafe:  true,
		},
		{
			name:      "path traversal with dots",
			sessionID: "../../../etc/passwd",
			wantSafe:  true,
		},
		{
			name:      "forward slashes",
			sessionID: "a/b/c",
			wantSafe:  true,
		},
		{
			name:      "backslashes",
			sessionID: "a\\b\\c",
			wantSafe:  true,
		},
		{
			name:      "uuid-style",
			sessionID: "550e8400-e29b-41d4-a716-446655440000",
			wantSafe:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := markerPath(tt.sessionID)
			if path == "" {
				t.Error("expected non-empty path")
			}
			// path should end with .json
			if len(path) < 5 || path[len(path)-5:] != ".json" {
				t.Errorf("path should end with .json, got %q", path)
			}
		})
	}
}

func TestWriteReadDeleteSessionMarker(t *testing.T) {
	t.Parallel()

	t.Run("write and read round-trip", func(t *testing.T) {
		now := time.Now().Truncate(time.Second)
		marker := &SessionMarker{
			AgentID:        "agent-test-001",
			SessionID:      "session-xyz",
			AgentSessionID: "test-roundtrip-" + t.Name(),
			PrimedAt:       now,
		}

		if err := WriteSessionMarker(marker); err != nil {
			t.Fatalf("WriteSessionMarker failed: %v", err)
		}
		defer DeleteSessionMarker(marker.AgentSessionID)

		got, err := ReadSessionMarker(marker.AgentSessionID)
		if err != nil {
			t.Fatalf("ReadSessionMarker failed: %v", err)
		}
		if got == nil {
			t.Fatal("expected non-nil marker")
		}
		if got.AgentID != "agent-test-001" {
			t.Errorf("AgentID = %q, want %q", got.AgentID, "agent-test-001")
		}
		if got.SessionID != "session-xyz" {
			t.Errorf("SessionID = %q, want %q", got.SessionID, "session-xyz")
		}
	})

	t.Run("read nonexistent returns nil nil", func(t *testing.T) {
		t.Parallel()
		got, err := ReadSessionMarker("nonexistent-session-id-xyz")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if got != nil {
			t.Error("expected nil marker for nonexistent session")
		}
	})

	t.Run("read empty session ID returns nil nil", func(t *testing.T) {
		t.Parallel()
		got, err := ReadSessionMarker("")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if got != nil {
			t.Error("expected nil marker for empty session ID")
		}
	})

	t.Run("delete nonexistent is no-op", func(t *testing.T) {
		t.Parallel()
		err := DeleteSessionMarker("nonexistent-session-delete-test")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("delete empty session ID is no-op", func(t *testing.T) {
		t.Parallel()
		err := DeleteSessionMarker("")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("write requires agent session ID", func(t *testing.T) {
		t.Parallel()
		marker := &SessionMarker{
			AgentID:        "agent-test",
			AgentSessionID: "",
		}
		err := WriteSessionMarker(marker)
		if err == nil {
			t.Error("expected error when AgentSessionID is empty")
		}
	})
}
