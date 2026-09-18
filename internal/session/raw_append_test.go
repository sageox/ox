package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRawAppendRecoveryAtPublicationBoundaries(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_cursor", true: "after_cursor"}[committed], func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "raw.jsonl")
			w, err := NewRawWriter(p, "")
			require.NoError(t, err)
			first := SessionEntry{Timestamp: time.Now(), Type: SessionEntryTypeUser, Content: "first"}
			require.NoError(t, w.WriteEntry(&first))
			require.NoError(t, w.Sync())
			before, err := os.ReadFile(p)
			require.NoError(t, err)
			require.NoError(t, w.BeginAppend(100, 200))
			last := first
			last.Content = "last"
			require.NoError(t, w.WriteEntry(&last))
			require.NoError(t, w.SealAppend())
			require.NoError(t, w.CloseAndSync())
			offset := int64(100)
			if committed {
				offset = 200
			}
			require.NoError(t, RecoverRawAppend(p, offset))
			require.NoError(t, RecoverRawAppend(p, offset))
			after, err := os.ReadFile(p)
			require.NoError(t, err)
			if committed {
				require.Contains(t, string(after), "last")
			} else {
				require.Equal(t, before, after)
			}
		})
	}
}
func TestRawAppendRejectsConflictingCursor(t *testing.T) {
	p := filepath.Join(t.TempDir(), "raw.jsonl")
	w, err := NewRawWriter(p, "")
	require.NoError(t, err)
	defer w.Close()
	require.NoError(t, w.BeginAppend(100, 200))
	require.ErrorContains(t, RecoverRawAppend(p, 300), "conflicts")
	_, err = os.Stat(p + ".append.json")
	require.NoError(t, err)
}
func TestRawStreamWriterCloseIsSafe(t *testing.T) {
	w, err := NewRawStreamWriter(os.Stdout, "")
	require.NoError(t, err)
	require.NoError(t, w.Sync())
	require.NoError(t, w.Close())
}

func TestRawWriterRedactsStreamedCredentialResults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "raw.jsonl")
	w, err := NewRawWriter(p, "")
	require.NoError(t, err)
	call := SessionEntry{Type: SessionEntryTypeTool, CallID: "secret-call", ToolName: "exec_command", ToolInput: `{"cmd":"gh auth token"}`}
	result := SessionEntry{Type: SessionEntryTypeTool, CallID: "secret-call", ToolOutput: "opaque-value-not-recognized-by-patterns"}
	require.NoError(t, w.WriteEntry(&call))
	require.NoError(t, w.WriteEntry(&result))
	require.NoError(t, w.CloseAndSync())
	body, err := os.ReadFile(p)
	require.NoError(t, err)
	require.NotContains(t, string(body), "opaque-value")
	require.Contains(t, string(body), "REDACTED:credential-output:gh-auth-token")
}

func TestRawAppendRejectsTruncationBehindCommittedCursor(t *testing.T) {
	p := filepath.Join(t.TempDir(), "raw.jsonl")
	w, err := NewRawWriter(p, "")
	require.NoError(t, err)
	require.NoError(t, w.BeginAppend(100, 200))
	entry := SessionEntry{Type: SessionEntryTypeUser, Content: "must survive"}
	require.NoError(t, w.WriteEntry(&entry))
	require.NoError(t, w.SealAppend())
	require.NoError(t, w.CloseAndSync())
	require.NoError(t, os.Truncate(p, 0))
	require.ErrorContains(t, RecoverRawAppend(p, 200), "committed capture size")
	require.FileExists(t, p+".append.json", "do not erase evidence of failed capture")
}
