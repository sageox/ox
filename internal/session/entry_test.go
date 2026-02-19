package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestMapEntryType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"user", "user"},
		{"human", "user"},
		{"assistant", "assistant"},
		{"message", "assistant"},
		{"ai", "assistant"},
		{"model", "assistant"},
		{"tool", "tool"},
		{"tool_use", "tool"},
		{"tool_call", "tool"},
		{"tool_result", "tool_result"},
		{"tool_output", "tool_result"},
		{"system", "system"},
		{"unknown", "info"},
		{"", "info"},
	}

	for _, tt := range tests {
		got := MapEntryType(tt.input)
		assert.Equal(t, tt.want, got, "MapEntryType(%q)", tt.input)
	}
}

func TestMapRoleToType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"user", "user"},
		{"assistant", "assistant"},
		{"system", "system"},
		{"unknown", "info"},
		{"", "info"},
	}

	for _, tt := range tests {
		got := MapRoleToType(tt.input)
		assert.Equal(t, tt.want, got, "MapRoleToType(%q)", tt.input)
	}
}

func TestParseTimestamp(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantOK  bool
		wantUTC bool
	}{
		{"RFC3339", "2026-01-20T14:00:00Z", true, true},
		{"RFC3339Nano", "2026-01-20T14:00:00.123456789Z", true, true},
		{"RFC3339 with offset", "2026-01-20T14:00:00+05:30", true, false},
		{"empty", "", false, false},
		{"invalid", "not-a-date", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, ok := ParseTimestamp(tt.input)
			assert.Equal(t, tt.wantOK, ok, "ParseTimestamp(%q) ok", tt.input)
			if tt.wantOK {
				assert.False(t, ts.IsZero())
			}
		})
	}
}

func TestExtractEntryTimestamp(t *testing.T) {
	tests := []struct {
		name  string
		entry map[string]any
		want  bool
	}{
		{"timestamp field", map[string]any{"timestamp": "2026-01-20T14:00:00Z"}, true},
		{"ts field", map[string]any{"ts": "2026-01-20T14:00:00Z"}, true},
		{"both fields prefers timestamp", map[string]any{"timestamp": "2026-01-20T14:00:00Z", "ts": "2026-01-20T15:00:00Z"}, true},
		{"no timestamp", map[string]any{"type": "user"}, false},
		{"empty entry", map[string]any{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, ok := ExtractEntryTimestamp(tt.entry)
			assert.Equal(t, tt.want, ok)
			if tt.want {
				assert.False(t, ts.IsZero())
			}
		})
	}

	// verify timestamp field takes precedence over ts
	entry := map[string]any{
		"timestamp": "2026-01-20T14:00:00Z",
		"ts":        "2026-01-20T15:00:00Z",
	}
	ts, ok := ExtractEntryTimestamp(entry)
	assert.True(t, ok)
	assert.Equal(t, time.Date(2026, 1, 20, 14, 0, 0, 0, time.UTC), ts)
}

func TestExtractEntryType(t *testing.T) {
	assert.Equal(t, "user", ExtractEntryType(map[string]any{"type": "user"}))
	assert.Equal(t, "unknown", ExtractEntryType(map[string]any{}))
	assert.Equal(t, "unknown", ExtractEntryType(map[string]any{"type": 123}))
}

func TestExtractContent(t *testing.T) {
	tests := []struct {
		name  string
		entry map[string]any
		want  string
	}{
		{"direct content", map[string]any{"content": "hello"}, "hello"},
		{"nested data.content", map[string]any{"data": map[string]any{"content": "nested"}}, "nested"},
		{"nested data.message", map[string]any{"data": map[string]any{"message": "msg"}}, "msg"},
		{"message field", map[string]any{"message": "top-msg"}, "top-msg"},
		{"text field", map[string]any{"text": "some text"}, "some text"},
		{"result field", map[string]any{"result": "tool output"}, "tool output"},
		{"empty entry", map[string]any{}, ""},
		{"content takes precedence", map[string]any{"content": "first", "message": "second"}, "first"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractContent(tt.entry)
			assert.Equal(t, tt.want, got)
		})
	}
}
