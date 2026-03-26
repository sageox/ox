package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/constants"
)

func TestOxHookCommandForEvent_AllEvents(t *testing.T) {
	t.Parallel()

	events := []string{"SessionStart", "PreCompact", "PostToolUse", "Stop", "SessionEnd", "UserPromptSubmit"}
	for _, event := range events {
		t.Run(event, func(t *testing.T) {
			t.Parallel()
			got := oxHookCommandForEvent(event)
			expected := fmt.Sprintf(constants.OxHookCommandClaudeCodeTemplate, event)
			if got != expected {
				t.Errorf("oxHookCommandForEvent(%q) = %q, want %q", event, got, expected)
			}
			// should contain the event name
			if !strings.Contains(got, event) {
				t.Errorf("expected command to contain event name %q", event)
			}
			// should contain ox agent hook
			if !strings.Contains(got, "ox agent hook") {
				t.Errorf("expected command to contain 'ox agent hook'")
			}
		})
	}
}

func TestGetSharedClaudeSettingsPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		gitRoot  string
		expected string
	}{
		{
			name:     "standard git root",
			gitRoot:  "/home/user/project",
			expected: filepath.Join("/home/user/project", ".claude", "settings.json"),
		},
		{
			name:     "root directory",
			gitRoot:  "/",
			expected: filepath.Join("/", ".claude", "settings.json"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := getSharedClaudeSettingsPath(tt.gitRoot)
			if got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestGetLocalClaudeSettingsPath(t *testing.T) {
	t.Parallel()

	gitRoot := "/home/user/project"
	expected := filepath.Join(gitRoot, ".claude", "settings.local.json")
	got := getLocalClaudeSettingsPath(gitRoot)
	if got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}
}

func TestClaudeLifecycleEvents_ContainsExpected(t *testing.T) {
	t.Parallel()

	requiredEvents := []string{"SessionStart", "PreCompact", "PostToolUse", "Stop", "SessionEnd", "UserPromptSubmit"}
	for _, event := range requiredEvents {
		found := false
		for _, e := range claudeLifecycleEvents {
			if e == event {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("claudeLifecycleEvents missing required event %q", event)
		}
	}
}

func TestReadSettingsFileRaw_NonexistentFile_Coverage(t *testing.T) {
	t.Parallel()

	settings, rawMap, err := readSettingsFileRaw("/nonexistent/path/settings.json")
	if err != nil {
		t.Fatalf("expected no error for nonexistent file, got %v", err)
	}
	if settings == nil {
		t.Fatal("expected non-nil settings")
	}
	if settings.Hooks == nil {
		t.Error("expected initialized Hooks map")
	}
	if rawMap == nil {
		t.Error("expected non-nil rawMap")
	}
}

func TestListProjectClaudeHooks_NoSettings(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	status := listProjectClaudeHooks(tmpDir)
	if status == nil {
		t.Fatal("expected non-nil status map")
	}
	// should have no true entries since there are no settings
	for event, installed := range status {
		if installed {
			t.Errorf("expected event %q to be false, got true", event)
		}
	}
}
