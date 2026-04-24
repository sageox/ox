package main

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeSessionSources(t *testing.T) {
	now := time.Now()

	mkSession := func(name string, age time.Duration) session.SessionInfo {
		return session.SessionInfo{
			SessionName: name,
			CreatedAt:   now.Add(-age),
		}
	}

	t.Run("both empty", func(t *testing.T) {
		result := mergeSessionSources(nil, nil)
		assert.Empty(t, result)
	})

	t.Run("primary only", func(t *testing.T) {
		primary := []session.SessionInfo{mkSession("a", 1*time.Hour), mkSession("b", 2*time.Hour)}
		result := mergeSessionSources(primary, nil)
		require.Len(t, result, 2)
		assert.Equal(t, "a", result[0].SessionName, "newest first")
	})

	t.Run("additional only", func(t *testing.T) {
		additional := []session.SessionInfo{mkSession("x", 3*time.Hour)}
		result := mergeSessionSources(nil, additional)
		require.Len(t, result, 1)
		assert.Equal(t, "x", result[0].SessionName)
	})

	t.Run("dedup primary wins", func(t *testing.T) {
		primary := []session.SessionInfo{mkSession("dup", 1*time.Hour)}
		additional := []session.SessionInfo{mkSession("dup", 2*time.Hour)}
		result := mergeSessionSources(primary, additional)
		require.Len(t, result, 1, "duplicate should be deduped")
		// primary's timestamp wins
		assert.Equal(t, now.Add(-1*time.Hour).Unix(), result[0].CreatedAt.Unix())
	})

	t.Run("legacy empty-name dedup by filepath", func(t *testing.T) {
		primary := []session.SessionInfo{
			{SessionName: "", FilePath: "/a/raw.jsonl", CreatedAt: now.Add(-1 * time.Hour)},
			{SessionName: "", FilePath: "/b/raw.jsonl", CreatedAt: now.Add(-2 * time.Hour)},
		}
		additional := []session.SessionInfo{
			{SessionName: "", FilePath: "/a/raw.jsonl", CreatedAt: now.Add(-3 * time.Hour)},
		}
		result := mergeSessionSources(primary, additional)
		require.Len(t, result, 2, "distinct legacy sessions should not be collapsed")
	})

	t.Run("disjoint merge sorted", func(t *testing.T) {
		primary := []session.SessionInfo{mkSession("old", 5*time.Hour)}
		additional := []session.SessionInfo{mkSession("new", 1*time.Hour), mkSession("mid", 3*time.Hour)}
		result := mergeSessionSources(primary, additional)
		require.Len(t, result, 3)
		assert.Equal(t, "new", result[0].SessionName)
		assert.Equal(t, "mid", result[1].SessionName)
		assert.Equal(t, "old", result[2].SessionName)
	})
}

// TestSanitizeSessionText guards the ANSI/control-byte sanitizer applied to
// meta.json Title/Summary from the shared ledger. Failure prevented: a hostile
// or malformed escape sequence rendered verbatim in the session list, spoofing
// the terminal or leaking control bytes into agent-consumed JSON.
func TestSanitizeSessionText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain ascii", "hello world", "hello world"},
		{"preserves tab and newline", "a\tb\nc", "a\tb\nc"},
		{"strips CSI color payload", "\x1b[31mred\x1b[0m text", "red text"},
		{"strips CSI with params", "before\x1b[1;2;3Hafter", "beforeafter"},
		{"strips OSC 8 hyperlink (BEL terminator)", "\x1b]8;;https://evil.example\x07visible\x1b]8;;\x07", "visible"},
		{"strips OSC with ST terminator", "\x1b]0;title\x1b\\after", "after"},
		{"strips DEL", "a\x7fbc", "abc"},
		{"strips C1 codepoint U+009C", "x\u009cy", "xy"},
		{"strips C0 except tab/newline", "x\x00y\x08z", "xyz"},
		{"two-byte ESC (ESC =)", "hi\x1b=bye", "hibye"},
		{"lone ESC at end", "trailing\x1b", "trailing"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, sanitizeSessionText(tc.in))
		})
	}
}
