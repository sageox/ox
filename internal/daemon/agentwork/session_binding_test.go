package agentwork

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/pkg/adapterruntime"
	"github.com/stretchr/testify/require"
)

func TestUnboundSourceCannotRestartOrRecover(t *testing.T) {
	for _, adapter := range []string{"gemini", "amp"} {
		for _, phase := range []string{"restart", "recovery", "publication"} {
			t.Run(adapter+"/"+phase, func(t *testing.T) {
				ledger := t.TempDir()
				cache := filepath.Join(ledger, ".sageox", "cache", "sessions", "unbound")
				require.NoError(t, os.MkdirAll(cache, 0o700))
				foreign := filepath.Join(t.TempDir(), "T-foreign.jsonl")
				require.NoError(t, os.WriteFile(foreign, []byte("{\"type\":\"user\",\"content\":\"foreign project secret\"}\n"), 0o600))
				recPath := filepath.Join(cache, recordingMarker)
				rawPath := filepath.Join(cache, artifactRaw)
				header := []byte("{\"type\":\"header\",\"metadata\":{}}\n")
				require.NoError(t, os.WriteFile(rawPath, header, 0o600))
				writeRecordingState(t, recPath, session.RecordingState{
					AgentID: "OxUnbound", AdapterName: adapter, WatchMode: "tail",
					SessionFile: foreign, SessionPath: cache, WorkspacePath: t.TempDir(),
					ParentPID: os.Getpid(), StartedAt: time.Now().Add(-time.Minute),
				})
				switch phase {
				case "restart":
					m := NewSessionWatcherManager(slog.Default())
					t.Cleanup(m.StopAll)
					require.Zero(t, m.DetectAndRestart(ledger))
				case "recovery":
					recovered, err := recoverRawFromSessionFile(slog.Default(), recPath, cache, rawPath)
					require.False(t, recovered)
					require.ErrorIs(t, err, adapterruntime.ErrUnboundSource)
				case "publication":
					require.True(t, sessionQuarantined(cache))
				}
				persisted, err := session.ReadRecordingStateFile(cache)
				require.NoError(t, err)
				require.True(t, persisted.SourceRejected)
				require.Zero(t, persisted.SourceOffset)
				after, err := os.ReadFile(rawPath)
				require.NoError(t, err)
				require.Equal(t, header, after)
			})
		}
	}
}