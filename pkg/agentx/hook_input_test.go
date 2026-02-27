package agentx

import (
	"strings"
	"testing"
)

func TestReadHookInput(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantNil bool
		check   func(t *testing.T, hi *HookInput)
	}{
		{
			name:    "empty input",
			input:   "",
			wantNil: true,
		},
		{
			name:    "invalid JSON",
			input:   "not json",
			wantNil: true,
		},
		{
			name:  "session start event",
			input: `{"session_id":"abc123","hook_event_name":"SessionStart","source":"startup"}`,
			check: func(t *testing.T, hi *HookInput) {
				if hi.SessionID != "abc123" {
					t.Errorf("session_id = %q, want %q", hi.SessionID, "abc123")
				}
				if hi.HookEventName != "SessionStart" {
					t.Errorf("hook_event_name = %q, want %q", hi.HookEventName, "SessionStart")
				}
				if hi.Source != "startup" {
					t.Errorf("source = %q, want %q", hi.Source, "startup")
				}
			},
		},
		{
			name:  "tool event with payloads",
			input: `{"session_id":"xyz","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"ls"},"tool_response":"file.txt"}`,
			check: func(t *testing.T, hi *HookInput) {
				if hi.ToolName != "Bash" {
					t.Errorf("tool_name = %q, want %q", hi.ToolName, "Bash")
				}
				if string(hi.ToolInput) != `{"command":"ls"}` {
					t.Errorf("tool_input = %q, want %q", string(hi.ToolInput), `{"command":"ls"}`)
				}
			},
		},
		{
			name:  "compact event with trigger",
			input: `{"session_id":"s1","hook_event_name":"PreCompact","trigger":"auto"}`,
			check: func(t *testing.T, hi *HookInput) {
				if hi.Trigger != "auto" {
					t.Errorf("trigger = %q, want %q", hi.Trigger, "auto")
				}
			},
		},
		{
			name:  "minimal valid JSON object",
			input: `{}`,
			check: func(t *testing.T, hi *HookInput) {
				if hi.SessionID != "" {
					t.Errorf("session_id = %q, want empty", hi.SessionID)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := strings.NewReader(tt.input)
			got := ReadHookInput(r)

			if tt.wantNil {
				if got != nil {
					t.Errorf("ReadHookInput() = %+v, want nil", got)
				}
				return
			}

			if got == nil {
				t.Fatal("ReadHookInput() = nil, want non-nil")
			}

			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestReadHookInput_RawBytesPreserved(t *testing.T) {
	// unknown fields should be preserved in RawBytes for subprocess passthrough
	input := `{"session_id":"s1","hook_event_name":"SessionStart","custom_agent_field":"secret","nested":{"deep":true}}`
	got := ReadHookInput(strings.NewReader(input))
	if got == nil {
		t.Fatal("ReadHookInput() = nil, want non-nil")
	}

	if got.SessionID != "s1" {
		t.Errorf("session_id = %q, want %q", got.SessionID, "s1")
	}

	// RawBytes must contain the original payload including unknown fields
	raw := string(got.RawBytes)
	if !strings.Contains(raw, "custom_agent_field") {
		t.Errorf("RawBytes should preserve unknown fields, got: %s", raw)
	}
	if !strings.Contains(raw, `"deep":true`) {
		t.Errorf("RawBytes should preserve nested unknown fields, got: %s", raw)
	}
}

func TestReadHookInput_LargePayload(t *testing.T) {
	// simulate a large tool response that would span multiple pipe reads
	largeOutput := strings.Repeat("x", 100000) // 100KB
	input := `{"session_id":"s1","tool_response":"` + largeOutput + `"}`
	got := ReadHookInput(strings.NewReader(input))
	if got == nil {
		t.Fatal("ReadHookInput() = nil for large payload, want non-nil")
	}
	if got.SessionID != "s1" {
		t.Errorf("session_id = %q, want %q", got.SessionID, "s1")
	}
	if len(got.RawBytes) < 100000 {
		t.Errorf("RawBytes length = %d, want >= 100000", len(got.RawBytes))
	}
}
