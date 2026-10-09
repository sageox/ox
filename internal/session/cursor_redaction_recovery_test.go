package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorCaptureCheckpointRetryUsesUpdatedRedactionPolicy(t *testing.T) {
	for _, tc := range []struct {
		name       string
		oldPolicy  string
		newPolicy  string
		want       string
		tornAppend bool
	}{
		{"new rule", "", "[REDACTED_NEW]", "[REDACTED_NEW]", false},
		{"changed rule", "[REDACTED_OLD]", "[REDACTED_NEW]", "[REDACTED_NEW]", false},
		{"removed rule", "[REDACTED_OLD]", "", "internal.example.test", false},
		{"torn append and changed rule", "[REDACTED_OLD]", "[REDACTED_NEW]", "[REDACTED_NEW]", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"role":"user"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
			f := newCursorCaptureFixture(t, "", body, false)
			policyPath := filepath.Join(f.projectRoot, ".sageox", "REDACT.md")
			writePolicy := func(replacement string) {
				t.Helper()
				policy := ""
				if replacement != "" {
					policy = "```redact\nliteral \"internal.example.test\" -> " + replacement + "\n```\n"
				}
				require.NoError(t, os.WriteFile(policyPath, []byte(policy), 0o600))
			}
			writePolicy(tc.oldPolicy)
			reader := &fixedCursorIncrementalReader{
				next:    int64(len(body)),
				entries: []adapters.RawEntry{{Role: "user", Content: "internal.example.test"}},
			}
			original := writeRecordingStateAtomically
			t.Cleanup(func() { writeRecordingStateAtomically = original })
			writeRecordingStateAtomically = func(string, any, os.FileMode) error {
				return errors.New("injected checkpoint failure")
			}
			_, err := drainCursorFixture(t, f, reader, false)
			writeRecordingStateAtomically = original
			require.ErrorContains(t, err, "checkpoint Cursor capture")
			state := loadCursorCaptureState(t, f)
			require.Zero(t, state.EntryCount)
			require.Zero(t, state.SourceOffset)
			if tc.tornAppend {
				info, err := os.Stat(f.rawPath)
				require.NoError(t, err)
				require.NoError(t, os.Truncate(f.rawPath, info.Size()-1))
			}

			writePolicy(tc.newPolicy)
			result, err := drainCursorFixture(t, f, reader, false)
			require.NoError(t, err, "unchanged native bytes remain recoverable after REDACT.md changes")
			require.Equal(t, 1, result.Entries)
			state = loadCursorCaptureState(t, f)
			assert.Equal(t, int64(len(body)), state.SourceOffset)
			assert.Equal(t, 1, state.EntryCount)
			assert.NoFileExists(t, f.rawPath+rawAppendJournalSuffix)
			raw, err := os.ReadFile(f.rawPath)
			require.NoError(t, err)
			entries, completeBytes, err := completeRawRecords(raw)
			require.NoError(t, err)
			require.Len(t, entries, 1, "retry must not duplicate the uncheckpointed batch")
			assert.Equal(t, len(raw), completeBytes)
			assert.Equal(t, tc.want, entries[0]["content"])
			assert.NotContains(t, string(raw), "[REDACTED_OLD]")
		})
	}
}
