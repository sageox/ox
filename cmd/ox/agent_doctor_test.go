package main

import (
	"strings"
	"testing"
)

func TestBuildFinalizeCommands(t *testing.T) {
	t.Parallel()

	sessionPath := "/path/to/sessions/2026-01-15-user-abc123"
	agentID := "OxABCD"

	t.Run("generates commands for missing artifacts", func(t *testing.T) {
		t.Parallel()
		missing := []string{"summary"}
		commands := buildFinalizeCommands("2026-01-15-user-abc123", agentID, missing, sessionPath)

		if len(commands) != 1 {
			t.Errorf("expected 1 command, got %d", len(commands))
		}

		foundSummary := false
		for _, cmd := range commands {
			if strings.Contains(cmd, "ox agent OxABCD session summarize --file") {
				foundSummary = true
			}
		}
		if !foundSummary {
			t.Error("expected summarize command")
		}
	})
}

func TestBuildNextSteps(t *testing.T) {
	t.Parallel()

	t.Run("empty when all good", func(t *testing.T) {
		t.Parallel()
		output := &AgentDoctorOutput{
			IncompleteSessions: nil,
			CommitNeeded:       false,
			PushNeeded:         false,
		}

		steps := buildNextSteps(output)
		if len(steps) != 0 {
			t.Errorf("expected no steps, got %v", steps)
		}
	})

	t.Run("includes incomplete session steps", func(t *testing.T) {
		t.Parallel()
		output := &AgentDoctorOutput{
			IncompleteSessions: []IncompleteSessionInfo{
				{
					SessionID: "test-session",
					Missing:   []string{"summary"},
				},
			},
		}

		steps := buildNextSteps(output)
		if len(steps) != 1 {
			t.Errorf("expected 1 step, got %d: %v", len(steps), steps)
		}
	})

	t.Run("includes commit step when needed", func(t *testing.T) {
		t.Parallel()
		output := &AgentDoctorOutput{
			CommitNeeded: true,
		}

		steps := buildNextSteps(output)
		if len(steps) != 1 {
			t.Errorf("expected 1 step, got %d", len(steps))
		}
		if steps[0] != "ox session commit" {
			t.Errorf("expected commit step, got %s", steps[0])
		}
	})

	t.Run("includes push step when needed", func(t *testing.T) {
		t.Parallel()
		output := &AgentDoctorOutput{
			PushNeeded: true,
		}

		steps := buildNextSteps(output)
		if len(steps) != 1 {
			t.Errorf("expected 1 step, got %d", len(steps))
		}
		if !strings.Contains(steps[0], "push") {
			t.Errorf("expected push step, got %s", steps[0])
		}
	})
}
