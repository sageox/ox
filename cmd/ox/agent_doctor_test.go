package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckSessionCompleteness(t *testing.T) {
	t.Parallel()

	t.Run("missing raw returns nil", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		sessionDir := filepath.Join(tmpDir, "test-session")
		if err := os.MkdirAll(sessionDir, 0755); err != nil {
			t.Fatal(err)
		}

		// no raw.jsonl - should return nil (invalid session)
		missing := checkSessionCompleteness(sessionDir)
		if missing != nil {
			t.Errorf("expected nil for missing raw, got %v", missing)
		}
	})

	t.Run("complete session returns empty", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		sessionDir := filepath.Join(tmpDir, "test-session")
		if err := os.MkdirAll(sessionDir, 0755); err != nil {
			t.Fatal(err)
		}

		// create all expected files (including summary.json)
		files := []string{ledgerFileRaw, ledgerFileEvents, ledgerFileHTML, ledgerFileSummaryMD, ledgerFileSessionMD, "summary.json"}
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(sessionDir, f), []byte("test"), 0644); err != nil {
				t.Fatal(err)
			}
		}

		missing := checkSessionCompleteness(sessionDir)
		if len(missing) != 0 {
			t.Errorf("expected empty missing list, got %v", missing)
		}
	})

	t.Run("partial session returns missing artifacts", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		sessionDir := filepath.Join(tmpDir, "test-session")
		if err := os.MkdirAll(sessionDir, 0755); err != nil {
			t.Fatal(err)
		}

		// create only raw.jsonl
		if err := os.WriteFile(filepath.Join(sessionDir, ledgerFileRaw), []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}

		missing := checkSessionCompleteness(sessionDir)
		if len(missing) != 5 {
			t.Errorf("expected 5 missing artifacts, got %d: %v", len(missing), missing)
		}

		// verify expected items are in missing list
		expectedMissing := map[string]bool{
			"events":       true,
			"html":         true,
			"summary":      true,
			"session_md":   true,
			"summary_json": true,
		}
		for _, m := range missing {
			if !expectedMissing[m] {
				t.Errorf("unexpected missing artifact: %s", m)
			}
		}
	})
}

func TestBuildFinalizeCommands(t *testing.T) {
	t.Parallel()

	sessionPath := "/path/to/sessions/2026-01-15-user-abc123"
	agentID := "OxABCD"

	t.Run("generates commands for missing artifacts", func(t *testing.T) {
		t.Parallel()
		missing := []string{"html", "summary"}
		commands := buildFinalizeCommands("2026-01-15-user-abc123", agentID, missing, sessionPath)

		if len(commands) != 2 {
			t.Errorf("expected 2 commands, got %d", len(commands))
		}

		// verify html command
		foundHTML := false
		foundSummary := false
		for _, cmd := range commands {
			if strings.Contains(cmd, "ox session export --input") {
				foundHTML = true
			}
			if strings.Contains(cmd, "ox agent OxABCD session summarize --file") {
				foundSummary = true
			}
		}
		if !foundHTML {
			t.Error("expected html generation command")
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
					Missing:   []string{"summary", "html"},
				},
			},
		}

		steps := buildNextSteps(output)
		if len(steps) != 2 {
			t.Errorf("expected 2 steps, got %d: %v", len(steps), steps)
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
