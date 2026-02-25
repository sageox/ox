//go:build !short

package main

import (
	"testing"
)

func TestCheckLedgerURLAPIMatch_Skip_NoLedger(t *testing.T) {
	// run in a temp dir with no ledger configured
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	result := checkLedgerURLAPIMatch(false)

	if !result.skipped {
		t.Errorf("expected skipped=true when no ledger found, got: %+v", result)
	}
}

func TestStripURLCredentials(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "URL with oauth2 credentials",
			input:    "https://oauth2:some-token@gitlab.example.com/group/repo.git",
			expected: "https://gitlab.example.com/group/repo.git",
		},
		{
			name:     "URL without credentials",
			input:    "https://gitlab.example.com/group/repo.git",
			expected: "https://gitlab.example.com/group/repo.git",
		},
		{
			name:     "URL with username only",
			input:    "https://user@gitlab.example.com/group/repo.git",
			expected: "https://gitlab.example.com/group/repo.git",
		},
		{
			name:     "URL with port and credentials",
			input:    "https://oauth2:token@gitlab.example.com:8443/group/repo.git",
			expected: "https://gitlab.example.com:8443/group/repo.git",
		},
		{
			name:     "invalid URL returns as-is",
			input:    "://not-a-valid-url",
			expected: "://not-a-valid-url",
		},
		{
			name:     "empty string returns empty",
			input:    "",
			expected: "",
		},
		{
			name:     "SSH-style URL returns as-is (not a parseable URL)",
			input:    "git@github.com:org/repo.git",
			expected: "git@github.com:org/repo.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripURLCredentials(tt.input)
			if got != tt.expected {
				t.Errorf("stripURLCredentials(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
