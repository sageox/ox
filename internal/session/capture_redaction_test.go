package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/stretchr/testify/require"
)

func TestCaptureCommandRedactionSurvivesRestartAndCrashReplay(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "checkpoint", true: "legacy_reconstruction"}[legacy], func(t *testing.T) {
			dir := t.TempDir()
			raw := filepath.Join(dir, "raw.jsonl")
			statePath := filepath.Join(dir, ".recording.json")
			require.NoError(t, fileutil.AtomicWriteJSON(statePath, &RecordingState{SessionPath: dir}, 0600))
			appendBatch := func(entries []Entry, offset int64) {
				require.NoError(t, fileutil.WithFileLock(context.Background(), raw, func() error {
					w, e := NewRawWriter(raw, "")
					if e != nil {
						return e
					}
					defer w.Close()
					return w.AppendRecordingBatch(statePath, entries, offset)
				}))
			}
			appendBatch([]Entry{{Type: EntryTypeTool, CallID: "secret-call", ToolInput: `{"command":["/bin/bash","-lc","aws configure export-credentials"]}`}}, 100)
			if legacy {
				require.NoError(t, MutateRecordingStateFile(statePath, func(s *RecordingState) error {
					s.CommandRedactionVersion = 0
					s.PendingCommandRedactions = nil
					return nil
				}))
			}
			// Simulate failed output append before the cursor transaction commits.
			w, err := NewRawWriter(raw, "")
			require.NoError(t, err)
			require.NoError(t, w.BeginAppend(100, 200))
			require.NoError(t, w.WriteEntry(&Entry{Type: EntryTypeTool, Content: "incomplete output"}))
			require.NoError(t, w.CloseAndSync())
			require.NoError(t, RecoverRawAppend(raw, 100))
			appendBatch([]Entry{{Type: EntryTypeTool, CallID: "secret-call", ToolOutput: "opaque credential not matched by regex", Content: "opaque credential not matched by regex"}}, 200)
			b, err := os.ReadFile(raw)
			require.NoError(t, err)
			require.NotContains(t, string(b), "opaque credential")
			require.NotContains(t, string(b), "incomplete output")
			require.NoError(t, MutateRecordingStateFile(statePath, func(s *RecordingState) error {
				require.Equal(t, 1, s.CommandRedactionVersion)
				require.Empty(t, s.PendingCommandRedactions)
				require.Equal(t, int64(200), s.SourceOffset)
				return nil
			}))
		})
	}
}
