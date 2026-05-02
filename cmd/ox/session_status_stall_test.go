package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
)

// TestCountRawEntries verifies the raw.jsonl line counter used to cross-check
// RecordingState.EntryCount against on-disk reality.
// Failure prevented: the #519 symptom where state.EntryCount=0 masked the
// fact that raw.jsonl had only the header line — we need a direct count.
func TestCountRawEntries(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{"missing file", "", -1},
		{"empty file", "<EMPTY>", 0},
		{"header only", `{"type":"header"}` + "\n", 0},
		{"header + 2 entries", `{"type":"header"}
{"type":"user","content":"hi"}
{"type":"assistant","content":"hello"}
`, 2},
		{"header + 5 entries, no trailing newline", `{"type":"header"}
{"type":"user"}
{"type":"assistant"}
{"type":"tool"}
{"type":"assistant"}
{"type":"user"}`, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, tt.name+".jsonl")
			switch tt.content {
			case "":
				// leave missing
			case "<EMPTY>":
				if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got := countRawEntries(path)
			if got != tt.want {
				t.Errorf("countRawEntries(%q) = %d, want %d", tt.name, got, tt.want)
			}
		})
	}
}

// TestSessionStallStatus verifies the stall heuristic used by `ox session status`
// to surface the #519 silent-failure mode: hooks firing but no entries captured.
// Failure prevented: regressing to the pre-#519 UX where a broken recording
// chain looked identical to a healthy idle session.
func TestSessionStallStatus(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name      string
		state     *session.RecordingState
		wantStall bool
	}{
		{
			name:  "nil state is never stalled",
			state: nil,
		},
		{
			name: "fresh session with no hooks is not stalled",
			state: &session.RecordingState{
				StartedAt: now.Add(-10 * time.Second),
			},
		},
		{
			name: "session with entries is not stalled",
			state: &session.RecordingState{
				StartedAt:       now.Add(-10 * time.Minute),
				EntryCount:      5,
				HookInvocations: 20,
			},
		},
		{
			name: "few hook invocations not enough to stall",
			state: &session.RecordingState{
				StartedAt:       now.Add(-10 * time.Minute),
				HookInvocations: 2,
				LastHookStatus:  "session-file-not-found",
			},
		},
		{
			name: "within grace period not stalled",
			state: &session.RecordingState{
				StartedAt:       now.Add(-30 * time.Second),
				HookInvocations: 20,
				LastHookStatus:  "session-file-not-found",
			},
		},
		{
			name: "hooks firing past threshold and grace period with zero entries IS stalled",
			state: &session.RecordingState{
				StartedAt:       now.Add(-10 * time.Minute),
				HookInvocations: 20,
				LastHookStatus:  "session-file-not-found",
			},
			wantStall: true,
		},
		{
			name: "stall with no last status still reports",
			state: &session.RecordingState{
				StartedAt:       now.Add(-10 * time.Minute),
				HookInvocations: 20,
			},
			wantStall: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStall, gotReason := sessionStallStatus(tt.state)
			if gotStall != tt.wantStall {
				t.Errorf("sessionStallStatus stalled = %v, want %v (reason=%q)", gotStall, tt.wantStall, gotReason)
			}
			if tt.wantStall && gotReason == "" {
				t.Errorf("sessionStallStatus returned stalled=true but empty reason")
			}
		})
	}
}
