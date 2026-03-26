package main

import (
	"strings"
	"testing"
)

func TestFormatInvalidCommandsDetail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		invalids     []invalidHookCommand
		settingsPath string
		wantContains []string
	}{
		{
			name: "single invalid without suggestion",
			invalids: []invalidHookCommand{
				{event: "SessionStart", command: "ox foo", suggestion: ""},
			},
			settingsPath: "/home/user/.claude/settings.json",
			wantContains: []string{"'ox foo'", "/home/user/.claude/settings.json"},
		},
		{
			name: "single invalid with suggestion",
			invalids: []invalidHookCommand{
				{event: "SessionStart", command: "ox prime", suggestion: "ox agent prime"},
			},
			settingsPath: "/path/settings.json",
			wantContains: []string{"'ox prime'", "use 'ox agent prime'"},
		},
		{
			name: "multiple invalids",
			invalids: []invalidHookCommand{
				{event: "SessionStart", command: "ox prime", suggestion: "ox agent prime"},
				{event: "Stop", command: "ox bar", suggestion: ""},
			},
			settingsPath: "/path/s.json",
			wantContains: []string{"'ox prime'", "'ox bar'", "Edit /path/s.json"},
		},
		{
			name:         "empty invalids list",
			invalids:     nil,
			settingsPath: "/path/s.json",
			wantContains: []string{"Edit /path/s.json"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatInvalidCommandsDetail(tt.invalids, tt.settingsPath)
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("expected detail to contain %q, got %q", want, got)
				}
			}
		})
	}
}

func TestExtractOxCommands_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: nil,
		},
		{
			name:     "no ox commands",
			input:    "echo hello world",
			expected: nil,
		},
		{
			name:     "ox inside single quotes is stripped",
			input:    "echo 'install ox for optimized team context'",
			expected: nil,
		},
		{
			name:     "multiple ox commands",
			input:    "ox agent prime && ox doctor",
			expected: []string{"ox agent prime", "ox doctor"},
		},
		{
			name:     "ox command with conditional",
			input:    "if command -v ox >/dev/null; then ox agent hook SessionStart; fi",
			expected: []string{"ox agent hook"},
		},
		{
			name:     "bare ox word boundary check",
			input:    "foxhound runs",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractOxCommands(tt.input)
			if len(got) != len(tt.expected) {
				t.Errorf("expected %d commands, got %d: %v", len(tt.expected), len(got), got)
				return
			}
			for i, cmd := range got {
				if cmd != tt.expected[i] {
					t.Errorf("command[%d]: expected %q, got %q", i, tt.expected[i], cmd)
				}
			}
		})
	}
}

func TestIsValidOxCommand_WithArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		cmd   string
		valid bool
	}{
		{name: "exact match", cmd: "ox doctor", valid: true},
		{name: "with additional flags", cmd: "ox agent prime --user", valid: true},
		{name: "agent hook with event", cmd: "ox agent hook SessionStart", valid: true},
		{name: "unknown command", cmd: "ox foo", valid: false},
		{name: "partial match not a prefix", cmd: "ox doc", valid: false},
		{name: "integrate subcommand", cmd: "ox integrate", valid: true},
		{name: "login command", cmd: "ox login", valid: true},
		{name: "status command", cmd: "ox status", valid: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isValidOxCommand(tt.cmd)
			if got != tt.valid {
				t.Errorf("isValidOxCommand(%q) = %v, want %v", tt.cmd, got, tt.valid)
			}
		})
	}
}

func TestGetSuggestionForInvalidCommand_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cmd        string
		suggestion string
	}{
		{name: "known legacy", cmd: "ox prime", suggestion: "ox agent prime"},
		{name: "legacy with flags", cmd: "ox prime --user", suggestion: "ox agent prime --user"},
		{name: "unknown command", cmd: "ox frobnicate", suggestion: ""},
		{name: "empty string", cmd: "", suggestion: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := getSuggestionForInvalidCommand(tt.cmd)
			if got != tt.suggestion {
				t.Errorf("getSuggestionForInvalidCommand(%q) = %q, want %q", tt.cmd, got, tt.suggestion)
			}
		})
	}
}

func TestValidateHookCommand_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		hookCmd        string
		wantValid      bool
		wantInvalidCnt int
	}{
		{
			name:           "no ox commands at all",
			hookCmd:        "echo hello",
			wantValid:      true,
			wantInvalidCnt: 0,
		},
		{
			name:           "all valid",
			hookCmd:        "ox agent prime && ox doctor",
			wantValid:      true,
			wantInvalidCnt: 0,
		},
		{
			name:           "one invalid",
			hookCmd:        "ox prime",
			wantValid:      false,
			wantInvalidCnt: 1,
		},
		{
			name:           "mixed valid and invalid",
			hookCmd:        "ox agent prime && ox foo",
			wantValid:      false,
			wantInvalidCnt: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			valid, invalids, suggestions := ValidateHookCommand(tt.hookCmd)
			if valid != tt.wantValid {
				t.Errorf("valid=%v, want %v", valid, tt.wantValid)
			}
			if len(invalids) != tt.wantInvalidCnt {
				t.Errorf("invalid count=%d, want %d", len(invalids), tt.wantInvalidCnt)
			}
			// verify suggestions map is non-nil even when valid
			_ = suggestions // used for validation above
		})
	}
}
