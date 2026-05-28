package adapterruntime

import (
	"testing"
)

// TestAdapterDisabledByEnv verifies the kill-switch env var honors
// case-insensitive comma-separated adapter names and trims whitespace.
// Failure prevented: a misconfigured env value (e.g. mixed case from
// shell quoting) silently fails to disable detection, leaving the
// detector running when an operator believed it was off.
func TestAdapterDisabledByEnv(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		adapter string
		want    bool
	}{
		{name: "unset", env: "", adapter: "claude-code", want: false},
		{name: "single match", env: "claude-code", adapter: "claude-code", want: true},
		{name: "single non-match", env: "claude-code", adapter: "codex", want: false},
		{name: "multi both", env: "claude-code,codex", adapter: "codex", want: true},
		{name: "multi other", env: "claude-code,codex", adapter: "claude-code", want: true},
		{name: "multi non-match", env: "claude-code,codex", adapter: "cursor", want: false},
		{name: "case insensitive + trim", env: "  Claude-Code  ,  codex", adapter: "claude-code", want: true},
		{name: "case insensitive target", env: "claude-code", adapter: "Claude-Code", want: true},
		{name: "empty target", env: "claude-code", adapter: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OX_DISABLE_TERMINAL_DETECTION", tt.env)
			got := AdapterDisabledByEnv(tt.adapter)
			if got != tt.want {
				t.Fatalf("AdapterDisabledByEnv(%q) with env=%q = %v, want %v",
					tt.adapter, tt.env, got, tt.want)
			}
		})
	}
}
