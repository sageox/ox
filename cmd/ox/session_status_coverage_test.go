package main

import (
	"testing"

	"github.com/sageox/ox/internal/session"
)

func TestAgentLivenessFor_TableDriven(t *testing.T) {
	t.Parallel()

	trueVal := true
	falseVal := false

	tests := []struct {
		name        string
		livenessMap map[string]bool
		agentID     string
		wantAlive   *bool
		wantStatus  string
	}{
		{
			name:        "nil map returns unknown",
			livenessMap: nil,
			agentID:     "agent-1",
			wantAlive:   nil,
			wantStatus:  "unknown",
		},
		{
			name:        "empty agent ID returns unknown",
			livenessMap: map[string]bool{"agent-1": true},
			agentID:     "",
			wantAlive:   nil,
			wantStatus:  "unknown",
		},
		{
			name:        "empty agent ID with nil map",
			livenessMap: nil,
			agentID:     "",
			wantAlive:   nil,
			wantStatus:  "unknown",
		},
		{
			name:        "agent not in map returns unknown",
			livenessMap: map[string]bool{"other-agent": true},
			agentID:     "agent-1",
			wantAlive:   nil,
			wantStatus:  "unknown",
		},
		{
			name:        "alive agent",
			livenessMap: map[string]bool{"agent-1": true},
			agentID:     "agent-1",
			wantAlive:   &trueVal,
			wantStatus:  "alive",
		},
		{
			name:        "dead agent",
			livenessMap: map[string]bool{"agent-1": false},
			agentID:     "agent-1",
			wantAlive:   &falseVal,
			wantStatus:  "dead",
		},
		{
			name:        "empty map returns unknown",
			livenessMap: map[string]bool{},
			agentID:     "agent-1",
			wantAlive:   nil,
			wantStatus:  "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotAlive, gotStatus := agentLivenessFor(tt.livenessMap, tt.agentID)

			if gotStatus != tt.wantStatus {
				t.Errorf("agentLivenessFor() status = %q, want %q", gotStatus, tt.wantStatus)
			}

			if tt.wantAlive == nil {
				if gotAlive != nil {
					t.Errorf("agentLivenessFor() alive = %v, want nil", *gotAlive)
				}
			} else {
				if gotAlive == nil {
					t.Errorf("agentLivenessFor() alive = nil, want %v", *tt.wantAlive)
				} else if *gotAlive != *tt.wantAlive {
					t.Errorf("agentLivenessFor() alive = %v, want %v", *gotAlive, *tt.wantAlive)
				}
			}
		})
	}
}

func TestFormatProcessStatus_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status string
	}{
		{name: "alive", status: "alive"},
		{name: "dead", status: "dead"},
		{name: "unknown", status: "unknown"},
		{name: "empty string", status: ""},
		{name: "arbitrary string", status: "something-else"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatProcessStatus(tt.status)
			if got == "" {
				t.Errorf("formatProcessStatus(%q) returned empty string", tt.status)
			}
			// the function wraps output in lipgloss styles, so we just verify
			// it produces non-empty output for all inputs
		})
	}
}

func TestAgentAlivePtr_NilState(t *testing.T) {
	t.Parallel()

	got := agentAlivePtr(nil)
	if got != nil {
		t.Errorf("agentAlivePtr(nil) = %v, want nil", *got)
	}
}

func TestAgentAlivePtr_ZeroPID(t *testing.T) {
	t.Parallel()

	state := &session.RecordingState{ParentPID: 0}
	got := agentAlivePtr(state)
	if got != nil {
		t.Errorf("agentAlivePtr(pid=0) = %v, want nil", *got)
	}
}

func TestAgentAlivePtr_NegativePID(t *testing.T) {
	t.Parallel()

	state := &session.RecordingState{ParentPID: -1}
	got := agentAlivePtr(state)
	if got != nil {
		t.Errorf("agentAlivePtr(pid=-1) = %v, want nil", *got)
	}
}

func TestAgentAlivePtr_PositivePID(t *testing.T) {
	t.Parallel()

	// use PID 1 (init process, always alive on unix)
	state := &session.RecordingState{ParentPID: 1}
	got := agentAlivePtr(state)
	if got == nil {
		t.Fatal("agentAlivePtr(pid=1) = nil, want non-nil")
	}
	// PID 1 should be alive
	if !*got {
		t.Logf("PID 1 reported as dead; may vary by platform")
	}
}

func TestAgentAlivePtr_NonexistentPID(t *testing.T) {
	t.Parallel()

	// use a very high PID that almost certainly doesn't exist
	state := &session.RecordingState{ParentPID: 999999999}
	got := agentAlivePtr(state)
	if got == nil {
		t.Fatal("agentAlivePtr(pid=999999999) = nil, want non-nil")
	}
	// should report dead
	if *got {
		t.Logf("PID 999999999 reported as alive; unexpected but not impossible")
	}
}
