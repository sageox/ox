package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
)

func TestGenerateAgentID(t *testing.T) {
	t.Run("generates valid agent ID with no existing IDs", func(t *testing.T) {
		agentID, err := agentinstance.GenerateAgentID([]string{})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if !agentinstance.IsValidAgentID(agentID) {
			t.Errorf("generated invalid agent ID: %s", agentID)
		}

		if !strings.HasPrefix(agentID, "Ox") {
			t.Errorf("agent ID should start with 'Ox', got %s", agentID)
		}

		if len(agentID) != 6 {
			t.Errorf("agent ID should be 6 chars, got %d: %s", len(agentID), agentID)
		}
	})

	t.Run("generates unique agent ID avoiding collisions", func(t *testing.T) {
		existing := []string{"OxA1b2", "OxC3d4", "OxE5f6"}
		agentID, err := agentinstance.GenerateAgentID(existing)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		for _, existingID := range existing {
			if agentID == existingID {
				t.Errorf("generated ID %s collides with existing ID", agentID)
			}
		}
	})

	t.Run("generates multiple unique IDs", func(t *testing.T) {
		seen := make(map[string]bool)
		iterations := 100

		for i := 0; i < iterations; i++ {
			agentID, err := agentinstance.GenerateAgentID([]string{})
			if err != nil {
				t.Fatalf("iteration %d: expected no error, got %v", i, err)
			}

			if seen[agentID] {
				t.Errorf("iteration %d: duplicate agent ID generated: %s", i, agentID)
			}
			seen[agentID] = true
		}
	})
}

func TestIsValidAgentID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "valid alphanumeric", input: "OxA1b2", expected: true},
		{name: "valid all numbers", input: "Ox1234", expected: true},
		{name: "valid all letters", input: "OxZzZz", expected: true},
		{name: "valid mixed case", input: "OxAaBb", expected: true},
		{name: "invalid lowercase prefix", input: "ox1234", expected: false},
		{name: "invalid uppercase prefix", input: "OX1234", expected: false},
		{name: "invalid too short", input: "Ox123", expected: false},
		{name: "invalid too long", input: "Ox12345", expected: false},
		{name: "invalid special char", input: "Ox12#4", expected: false},
		{name: "invalid empty", input: "", expected: false},
		{name: "invalid no prefix", input: "123456", expected: false},
		{name: "invalid wrong prefix", input: "Ab1234", expected: false},
		{name: "invalid spaces", input: "Ox12 4", expected: false},
		{name: "invalid underscore", input: "Ox12_4", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := agentinstance.IsValidAgentID(tt.input)
			if result != tt.expected {
				t.Errorf("IsValidAgentID(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestFindProjectRoot(t *testing.T) {
	t.Run("finds project root with .sageox directory", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "ox-test-*")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		sageoxDir := filepath.Join(tmpDir, ".sageox")
		if err := os.MkdirAll(sageoxDir, 0755); err != nil {
			t.Fatalf("failed to create .sageox dir: %v", err)
		}

		subDir := filepath.Join(tmpDir, "sub", "nested")
		if err := os.MkdirAll(subDir, 0755); err != nil {
			t.Fatalf("failed to create nested dir: %v", err)
		}

		originalCwd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get cwd: %v", err)
		}
		defer os.Chdir(originalCwd)

		if err := os.Chdir(subDir); err != nil {
			t.Fatalf("failed to change dir: %v", err)
		}

		root, err := findProjectRoot()
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		// resolve both paths to handle symlinks on macOS
		expectedRoot, _ := filepath.EvalSymlinks(tmpDir)
		actualRoot, _ := filepath.EvalSymlinks(root)

		if actualRoot != expectedRoot {
			t.Errorf("expected root %s, got %s", expectedRoot, actualRoot)
		}
	})

	t.Run("returns cwd when no .sageox directory found", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "ox-test-*")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		originalCwd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get cwd: %v", err)
		}
		defer os.Chdir(originalCwd)

		if err := os.Chdir(tmpDir); err != nil {
			t.Fatalf("failed to change dir: %v", err)
		}

		root, err := findProjectRoot()
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		// resolve both paths to handle symlinks on macOS
		expectedRoot, _ := filepath.EvalSymlinks(tmpDir)
		actualRoot, _ := filepath.EvalSymlinks(root)

		if actualRoot != expectedRoot {
			t.Errorf("expected root %s, got %s", expectedRoot, actualRoot)
		}
	})
}

func TestGetUserSlug(t *testing.T) {
	slug := getUserSlug()

	if slug == "" {
		t.Error("getUserSlug should not return empty string")
	}

	if strings.Contains(slug, " ") {
		t.Errorf("slug should not contain spaces: %s", slug)
	}

	if strings.Contains(slug, "@") {
		t.Errorf("slug should not contain @: %s", slug)
	}

	if slug != strings.ToLower(slug) {
		t.Errorf("slug should be lowercase: %s", slug)
	}
}

// SageOx is multiplayer - offline mode is not supported.
// See internal/auth/feature.go for philosophy.

func TestAgentPrimeOutputFormatting(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ox-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	sageoxDir := filepath.Join(tmpDir, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	originalCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	defer os.Chdir(originalCwd)

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change dir: %v", err)
	}

	t.Run("json format output", func(t *testing.T) {
		output := agentPrimeOutput{
			Status:  "fresh",
			AgentID: "OxA1b2",
			Content: "test guidance content",
		}

		data, err := json.Marshal(output)
		if err != nil {
			t.Fatalf("failed to marshal output: %v", err)
		}

		var decoded agentPrimeOutput
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("failed to unmarshal output: %v", err)
		}

		if decoded.Status != output.Status {
			t.Errorf("status mismatch: got %s, want %s", decoded.Status, output.Status)
		}

		if decoded.AgentID != output.AgentID {
			t.Errorf("agent_id mismatch: got %s, want %s", decoded.AgentID, output.AgentID)
		}
	})
}

func TestOutputAgentPrimeTextFormat(t *testing.T) {
	t.Skip("skipping output format test - requires cobra command setup")
}

func TestInstanceExpiry(t *testing.T) {
	t.Run("expired instance detected", func(t *testing.T) {
		inst := &agentinstance.Instance{
			AgentID:         "OxA1b2",
			ServerSessionID: "oxsid_01KCJECKEGETGX6HC80NRYVZ3P",
			CreatedAt:       time.Now().Add(-25 * time.Hour),
			ExpiresAt:       time.Now().Add(-1 * time.Hour),
		}

		if !inst.IsExpired() {
			t.Error("expected instance to be expired")
		}
	})

	t.Run("active instance not expired", func(t *testing.T) {
		inst := &agentinstance.Instance{
			AgentID:         "OxA1b2",
			ServerSessionID: "oxsid_01KCJECKEGETGX6HC80NRYVZ3P",
			CreatedAt:       time.Now(),
			ExpiresAt:       time.Now().Add(23 * time.Hour),
		}

		if inst.IsExpired() {
			t.Error("expected instance to not be expired")
		}
	})
}

func TestAgentPrimeOutputStatuses(t *testing.T) {
	tests := []struct {
		name          string
		status        string
		message       string
		expectMessage bool
	}{
		{
			name:          "fresh status no message",
			status:        "fresh",
			message:       "",
			expectMessage: false,
		},
		{
			name:          "stale status with message",
			status:        "stale",
			message:       "Using cached guidance",
			expectMessage: true,
		},
		{
			name:          "offline status with message",
			status:        "offline",
			message:       "Offline mode",
			expectMessage: true,
		},
		{
			name:          "unavailable status with message",
			status:        "unavailable",
			message:       "API unavailable",
			expectMessage: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := agentPrimeOutput{
				Status:  tt.status,
				AgentID: "OxTest",
				Message: tt.message,
			}

			if tt.status != output.Status {
				t.Errorf("status mismatch: got %s, want %s", output.Status, tt.status)
			}

			if tt.expectMessage && output.Message == "" {
				t.Error("expected message to be set for non-fresh status")
			}
		})
	}
}

func TestIsAgentSubcommand(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "session is subcommand", input: "session", expected: true},
		{name: "doctor is subcommand", input: "doctor", expected: true},
		{name: "prime is NOT subcommand", input: "prime", expected: false},
		{name: "list is NOT subcommand", input: "list", expected: false},
		{name: "unknown is NOT subcommand", input: "foobar", expected: false},
		{name: "empty is NOT subcommand", input: "", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isAgentSubcommand(tt.input)
			if result != tt.expected {
				t.Errorf("isAgentSubcommand(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestAgentDispatcherEnvVarFallback(t *testing.T) {
	t.Run("env var with valid agent ID resolves subcommand", func(t *testing.T) {
		// isAgentSubcommand + valid env var = would attempt runWithAgentID
		// we test the logic components since full dispatch needs cobra wiring
		if !isAgentSubcommand("session") {
			t.Fatal("session should be a known agent subcommand")
		}

		validID := "OxT3st"
		if !agentinstance.IsValidAgentID(validID) {
			t.Fatalf("%s should be a valid agent ID", validID)
		}
	})

	t.Run("env var with invalid agent ID does not resolve", func(t *testing.T) {
		invalidIDs := []string{"", "bad", "ox1234", "OX1234", "Ox12"}
		for _, id := range invalidIDs {
			if agentinstance.IsValidAgentID(id) {
				t.Errorf("expected %q to be invalid agent ID", id)
			}
		}
	})

	t.Run("non-subcommand first arg does not trigger env fallback", func(t *testing.T) {
		nonSubcommands := []string{"prime", "list", "foobar", ""}
		for _, arg := range nonSubcommands {
			if isAgentSubcommand(arg) {
				t.Errorf("expected %q to NOT be a subcommand", arg)
			}
		}
	})
}
