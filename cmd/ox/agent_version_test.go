package main

import "testing"

// --- A. Flexible version extraction ---

// TestExtractFlexibleVersion_Formats validates that the flexible regex matches
// version patterns that agentx's strict X.Y.Z regex misses.
// Failure prevented: agents reporting "unknown" when their CLI outputs non-standard versions.
func TestExtractFlexibleVersion_Formats(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"three-part semver", "1.2.3", "1.2.3"},
		{"two-part version", "0.14", "0.14"},
		{"v-prefixed", "v1.2.3", "1.2.3"},
		{"v-prefixed two-part", "v0.14", "0.14"},
		{"with tool name", "pi-coding-agent 1.2.3", "1.2.3"},
		{"with tool name two-part", "amp 0.5", "0.5"},
		{"with newline", "gemini 2.1.0\n", "2.1.0"},
		{"embedded in text", "Version: v3.14.1 (stable)", "3.14.1"},
		{"empty", "", ""},
		{"no version", "hello world", ""},
		{"whitespace only", "   ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFlexibleVersion(tt.input)
			if got != tt.want {
				t.Errorf("extractFlexibleVersion(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// --- B. Agent type to binary mapping ---

// TestAgentTypeToBinary_Mapping validates that canonical agent types map to correct CLI binaries.
// Failure prevented: version fallback running wrong binary for an agent type.
func TestAgentTypeToBinary_Mapping(t *testing.T) {
	tests := []struct {
		agentType string
		want      string
	}{
		{"claude", "claude"},
		{"pi", "pi"},
		{"amp", "amp"},
		{"gemini", "gemini"},
		{"droid", "droid"},
		{"codex", "codex"},
		{"aider", "aider"},
		{"opencode", "opencode"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			got := agentTypeToBinary(tt.agentType)
			if got != tt.want {
				t.Errorf("agentTypeToBinary(%q) = %q, want %q", tt.agentType, got, tt.want)
			}
		})
	}
}
