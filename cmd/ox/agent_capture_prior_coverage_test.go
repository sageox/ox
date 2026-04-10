package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
)

func TestParseCapturePriorFile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "no file flag",
			args: []string{"capture-prior"},
			want: "",
		},
		{
			name: "file flag separate",
			args: []string{"--file", "/tmp/history.jsonl"},
			want: "/tmp/history.jsonl",
		},
		{
			name: "file flag equals",
			args: []string{"--file=/tmp/history.jsonl"},
			want: "/tmp/history.jsonl",
		},
		{
			name: "file flag at end without value",
			args: []string{"--file"},
			want: "",
		},
		{
			name: "empty args",
			args: []string{},
			want: "",
		},
		{
			name: "file flag between other args",
			args: []string{"--title", "test", "--file", "/path/data.jsonl", "--verbose"},
			want: "/path/data.jsonl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseCapturePriorFile(tt.args)
			if got != tt.want {
				t.Errorf("parseCapturePriorFile(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestFormatCapturePriorOutput(t *testing.T) {
	t.Parallel()

	t.Run("basic result", func(t *testing.T) {
		t.Parallel()
		result := &session.CaptureResult{
			AgentID:         "agent-001",
			Path:            "/tmp/sessions/session-001",
			SessionName:     "session-001",
			EntryCount:      5,
			SecretsRedacted: 2,
			Title:           "Planning discussion",
		}

		data, err := formatCapturePriorOutput(result)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var output capturePriorOutput
		if err := json.Unmarshal(data, &output); err != nil {
			t.Fatalf("failed to parse output: %v", err)
		}

		if !output.Success {
			t.Error("expected Success = true")
		}
		if output.Type != "session_capture_prior" {
			t.Errorf("Type = %q, want %q", output.Type, "session_capture_prior")
		}
		if output.AgentID != "agent-001" {
			t.Errorf("AgentID = %q, want %q", output.AgentID, "agent-001")
		}
		if output.EntryCount != 5 {
			t.Errorf("EntryCount = %d, want 5", output.EntryCount)
		}
		if output.SecretsRedacted != 2 {
			t.Errorf("SecretsRedacted = %d, want 2", output.SecretsRedacted)
		}
		if output.Title != "Planning discussion" {
			t.Errorf("Title = %q, want %q", output.Title, "Planning discussion")
		}
	})

	t.Run("minimal result", func(t *testing.T) {
		t.Parallel()
		result := &session.CaptureResult{
			AgentID:    "agent-002",
			Path:       "/tmp/s",
			EntryCount: 0,
		}

		data, err := formatCapturePriorOutput(result)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var output capturePriorOutput
		if err := json.Unmarshal(data, &output); err != nil {
			t.Fatalf("failed to parse output: %v", err)
		}

		if output.EntryCount != 0 {
			t.Errorf("EntryCount = %d, want 0", output.EntryCount)
		}
		if output.SessionName != "" {
			t.Errorf("SessionName = %q, want empty", output.SessionName)
		}
	})
}

func TestParseSessionID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no flag", []string{"capture-prior"}, ""},
		{"separate value", []string{"--session-id", "abc-123"}, "abc-123"},
		{"equals value", []string{"--session-id=abc-123"}, "abc-123"},
		{"no value", []string{"--session-id"}, ""},
		{"empty args", []string{}, ""},
		{"between other args", []string{"--verbose", "--session-id", "sess-xyz", "--title", "test"}, "sess-xyz"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseSessionID(tt.args)
			if got != tt.want {
				t.Errorf("parseSessionID(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

// TestFindCapturePriorAdapterByName_NotFound verifies that an explicit
// --adapter <name> for an unknown adapter returns a clear "not found" error
// rather than silently falling back to auto-detection.
//
// Failure prevented: a typo or stale adapter reference would otherwise be
// masked by auto-detect picking up whatever adapter happens to detect first,
// reintroducing the original "wrong adapter chosen" bug.
func TestFindCapturePriorAdapterByName_NotFound(t *testing.T) {
	// not parallel: mutates global adapter registry
	saveAdapterRegistry(t)

	_, err := findCapturePriorAdapterByName("nonexistent-adapter")
	if err == nil {
		t.Fatal("expected error for unknown adapter, got nil")
	}
	if !errors.Is(err, adapters.ErrAdapterNotFound) {
		t.Errorf("expected ErrAdapterNotFound in chain, got %v", err)
	}
	if !strings.Contains(err.Error(), "nonexistent-adapter") {
		t.Errorf("error should name the adapter, got: %v", err)
	}
}

// TestFindCapturePriorAdapterByName_NotExternal verifies that an explicit
// adapter name pointing at a built-in (non-ExternalAdapter) returns a clear
// "does not support capture-prior" error.
//
// Failure prevented: capture-prior is implemented exclusively by external
// adapter binaries via the protocol; built-in adapters lack the
// CapturePrior() method. Without this guard, the type assertion would panic
// at runtime.
func TestFindCapturePriorAdapterByName_NotExternal(t *testing.T) {
	// not parallel: mutates global adapter registry
	saveAdapterRegistry(t)

	// GenericJSONLAdapter is a built-in (registered in init() of generic_jsonl.go),
	// not an *ExternalAdapter — exercises the type-assertion failure branch.
	_, err := findCapturePriorAdapterByName("generic")
	if err == nil {
		t.Fatal("expected error for built-in adapter, got nil")
	}
	if !strings.Contains(err.Error(), "does not support capture-prior") {
		t.Errorf("error should explain capability gap, got: %v", err)
	}
}

// TestFindCapturePriorAdapter_NoneCapable verifies the auto-detect path
// returns a clean error when no installed adapter advertises capture-prior.
//
// Failure prevented: the previous bug surfaced as a confusing "adapter X
// does not support capture-prior" pointing at the wrong (random) adapter.
// The capability filter must run BEFORE detect, and an empty capable set
// must produce a stable diagnostic message.
func TestFindCapturePriorAdapter_NoneCapable(t *testing.T) {
	// not parallel: mutates global adapter registry
	saveAdapterRegistry(t)
	adapters.ResetRegistry() // strip GenericJSONLAdapter and any externals

	_, err := findCapturePriorAdapter("")
	if err == nil {
		t.Fatal("expected error for empty registry, got nil")
	}
	if !strings.Contains(err.Error(), "no installed adapter supports capture-prior") {
		t.Errorf("error should mention missing capability, got: %v", err)
	}
}

// saveAdapterRegistry snapshots the global adapter registry and restores it
// at test cleanup. Used by tests that need to mutate the registry.
func saveAdapterRegistry(t *testing.T) {
	t.Helper()
	// snapshot current adapters by name; we restore by re-importing the
	// generic builtin via package init on next test (if needed). Since
	// ResetRegistry/Unregister mutate global state and Register would
	// panic on duplicate, we restore by capturing names then unregistering
	// any names not in the snapshot afterward.
	originalNames := adapters.ListAdapters()
	originalSet := make(map[string]bool, len(originalNames))
	for _, n := range originalNames {
		originalSet[n] = true
	}
	t.Cleanup(func() {
		// remove any adapters added during the test
		for _, n := range adapters.ListAdapters() {
			if !originalSet[n] {
				adapters.Unregister(n)
			}
		}
		// re-register any builtin that the test removed (only "generic" is
		// registered via package init; external adapters are discovered
		// lazily, so they don't need restoring here).
		stillThere := make(map[string]bool)
		for _, n := range adapters.ListAdapters() {
			stillThere[n] = true
		}
		if originalSet["generic"] && !stillThere["generic"] {
			adapters.Register(&adapters.GenericJSONLAdapter{})
		}
	})
}

// TestParseAdapter exercises the manual --adapter parser used by
// capture-prior to bypass auto-detection.
//
// Failure prevented: a regression that breaks --adapter parsing would
// silently fall back to auto-detect, reintroducing the non-deterministic
// adapter race that this flag exists to defeat.
func TestParseAdapter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no flag", []string{"capture-prior"}, ""},
		{"separate value", []string{"--adapter", "claude-code"}, "claude-code"},
		{"equals value", []string{"--adapter=claude-code"}, "claude-code"},
		{"alias separate", []string{"--adapter", "claude"}, "claude"},
		{"alias equals", []string{"--adapter=claude"}, "claude"},
		{"no value", []string{"--adapter"}, ""},
		{"empty args", []string{}, ""},
		{"between other args", []string{"--verbose", "--adapter", "codex", "--title", "test"}, "codex"},
		{"after session-id", []string{"--session-id", "abc", "--adapter", "claude"}, "claude"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseAdapter(tt.args)
			if got != tt.want {
				t.Errorf("parseAdapter(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}
