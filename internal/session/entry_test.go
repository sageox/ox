package session

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// --- Entry ID tests ---

var entryIDPattern = regexp.MustCompile(`^[0-9A-Za-z]{5}$`)

// TestGenerateEntryID_Format verifies length and charset.
// Failure prevented: malformed entry IDs that break downstream parsers expecting [A-Za-z0-9]{5}.
func TestGenerateEntryID_Format(t *testing.T) {
	for i := 0; i < 100; i++ {
		eid := GenerateEntryID()
		require.Len(t, eid, 5, "entry ID must be exactly 5 chars")
		assert.Regexp(t, entryIDPattern, eid, "entry ID must match [0-9A-Za-z]{5}")
	}
}

// TestGenerateEntryID_Uniqueness verifies the generator is drawing from the full
// 62^5 space rather than a broken or collapsed one.
//
// Failure prevented: broken randomness producing duplicate IDs within a session.
//
// It deliberately does NOT demand zero collisions. With 916M possible IDs, the
// birthday probability of at least one collision in 1000 draws is ~0.055% — so a
// zero-collision assertion fails roughly one CI run in 1800 for a generator that
// is working perfectly, which is what it did. Two or more collisions in the same
// batch is ~1 in 7 million, so that threshold still catches genuinely broken
// randomness (a stuck RNG or a collapsed alphabet collides constantly) without
// failing on correct behavior.
func TestGenerateEntryID_Uniqueness(t *testing.T) {
	const draws = 1000
	seen := make(map[string]bool, draws)
	collisions := 0
	for i := 0; i < draws; i++ {
		eid := GenerateEntryID()
		if seen[eid] {
			collisions++
		}
		seen[eid] = true
	}
	require.LessOrEqual(t, collisions, 1,
		"%d duplicate IDs in %d draws: randomness is broken, not merely unlucky", collisions, draws)
}

// TestGenerateEntryID_Unbiased catches the modulo bias the generator used to have.
//
// `rand byte % 62` over-represents the first 8 characters of the alphabet by
// ~1.6%. Failure prevented: a silently shrunken ID space and a raised collision
// rate. The bound is loose enough never to flake but far tighter than the ~1.29x
// ratio a biased generator produces at this sample size.
func TestGenerateEntryID_Unbiased(t *testing.T) {
	const draws = 20000
	biased, rest := 0, 0
	for i := 0; i < draws; i++ {
		for _, c := range GenerateEntryID() {
			if strings.IndexRune(entryIDCharset, c) < 8 {
				biased++
			} else {
				rest++
			}
		}
	}
	// 8 of 62 characters: expected share is 8/62 ~= 0.129.
	share := float64(biased) / float64(biased+rest)
	require.InDelta(t, 8.0/62.0, share, 0.015,
		"first 8 charset characters appear %.4f of the time, expected ~%.4f: modulo bias is back",
		share, 8.0/62.0)
}

// TestExtractEntryID verifies extraction from entry maps.
// Failure prevented: read-path can't find eid field.
func TestExtractEntryID(t *testing.T) {
	tests := []struct {
		name  string
		entry map[string]any
		want  string
	}{
		{"present", map[string]any{"eid": "aB3xZ"}, "aB3xZ"},
		{"missing", map[string]any{"type": "user"}, ""},
		{"wrong type", map[string]any{"eid": 12345}, ""},
		{"empty string", map[string]any{"eid": ""}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractEntryID(tt.entry)
			assert.Equal(t, tt.want, got)
		})
	}
}
