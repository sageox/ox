package agentwork

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/cursorpaths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: daemon recovery finalizes an inactive Cursor recording
// through FinalizeInactiveCursorCapture, which threw away the batch its final
// drain committed. raw.jsonl kept those turns, but the live spool never got
// them and no gap line said so, so the stream on SageOx closed short (C8).
// The batch is committed even when the final drain must wait for Cursor's
// export, so it is forwarded then too.
func TestRecoverRawFromSessionFile_CursorFinalBatchReachesTheLiveSpool(t *testing.T) {
	for _, tc := range []struct {
		name string
		// source is the native Cursor transcript recovery drains
		source        string
		streamSession string // the live stream's ses_ id; the recording's is liveTestSessionID
		wantErr       error
		wantSpooled   []string
	}{
		{
			name: "finalized",
			source: `{"role":"user","content":"late prompt"}` + "\n" +
				`{"role":"assistant","content":"late response"}` + "\n" +
				`{"type":"turn_ended","status":"success"}` + "\n",
			streamSession: liveTestSessionID,
			wantSpooled:   []string{"late prompt", "late response"},
		},
		{
			name:          "final drain still pending",
			source:        `{"role":"user","content":"unfinished"}` + "\n",
			streamSession: liveTestSessionID,
			wantErr:       session.ErrCursorFinalDrainPending,
			wantSpooled:   []string{"unfinished"},
		},
		{
			// C4: a stream another recording left in this folder never gets them
			name: "stream belongs to another recording",
			source: `{"role":"user","content":"not yours"}` + "\n" +
				`{"type":"turn_ended","status":"success"}` + "\n",
			streamSession: otherLiveSessionID,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installCursorWatcherAdapter(t)
			home, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			t.Setenv("HOME", home)
			projectRoot := t.TempDir()
			sessionDir := t.TempDir()
			source, err := cursorpaths.SessionPath(home, projectRoot, cursorFinalizeConversationID)
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Dir(source), 0o755))
			require.NoError(t, os.WriteFile(source, []byte(tc.source), 0o600))
			_, err = livesession.Prepare(context.Background(),
				livesession.Config{Endpoint: "https://live.test.sageox.ai", DataDir: t.TempDir(), KeyDir: t.TempDir()},
				sessionDir, livesession.OpenParams{SessionID: tc.streamSession, SessionName: filepath.Base(sessionDir)})
			require.NoError(t, err)
			recPath := filepath.Join(sessionDir, recordingMarker)
			stoppedAt := time.Now().UTC()
			writeRecordingState(t, recPath, session.RecordingState{
				SessionID: liveTestSessionID, StoppedAt: &stoppedAt,
				AgentID: "OxCursorLive", AgentSessionID: cursorFinalizeConversationID,
				AdapterName: "cursor", WatchMode: "tail", WorkspacePath: projectRoot, SessionPath: sessionDir,
				SessionFile: source, StartOffsetKnown: true, SourcePrefixSHA256: watcherSHA256(nil),
			})

			_, err = recoverRawFromSessionFile(slog.New(slog.DiscardHandler), recPath, sessionDir, filepath.Join(sessionDir, artifactRaw))
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}

			spool, readErr := os.ReadFile(filepath.Join(sessionDir, "spool.jsonl"))
			if len(tc.wantSpooled) == 0 {
				assert.NotContains(t, string(spool), "not yours", "another recording's stream must not get this recording's turns")
				return
			}
			require.NoError(t, readErr, "recovery committed turns to raw.jsonl but never spooled them")
			for _, want := range tc.wantSpooled {
				assert.Contains(t, string(spool), want, "a turn recovery committed to raw.jsonl is missing from the live spool")
			}
		})
	}
}
