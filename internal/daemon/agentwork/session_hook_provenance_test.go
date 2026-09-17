package agentwork

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/require"
)

func TestHookRecoveryPersistsNativeCursorBeforeRetiringMarker(t *testing.T) {
	for _, mode := range []string{"hook-prefix", "stopped-prefix", "pending-drain", "missing-cursor"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			rawPath := filepath.Join(dir, artifactRaw)
			recPath := filepath.Join(dir, recordingMarker)
			id := "019c6d2e-27b0-798d-aaed-b036114dc63a"
			stamp := time.Now().UTC().Format(time.RFC3339Nano)
			header := fmt.Sprintf("{\"timestamp\":%q,\"type\":\"session_meta\",\"payload\":{\"id\":%q,\"cwd\":%q}}\n", stamp, id, dir)
			line := fmt.Sprintf("{\"timestamp\":%q,\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"native turn\"}]}}\n", stamp)
			source := filepath.Join(t.TempDir(), "native.jsonl")
			require.NoError(t, os.WriteFile(source, []byte(header+line+line), 0600))
			now := time.Now()
			state := session.RecordingState{AgentID: "OxHook", AdapterName: "codex", AgentSessionID: id, SessionFile: source, SessionPath: dir, WatchMode: "hook", SourceOffset: int64(len(header + line)), EntryCount: 1}
			if mode == "stopped-prefix" || mode == "pending-drain" {
				state.StoppedAt = &now
			}
			state.CaptureDrainPending = mode == "pending-drain"
			if mode == "missing-cursor" {
				state.SourceOffset = 0
			}
			writeRecordingState(t, recPath, state)
			original := []byte("{\"_meta\":{\"agent_type\":\"codex\"}}\n{\"type\":\"user\",\"content\":\"captured prefix\"}\n")
			require.NoError(t, os.WriteFile(rawPath, original, 0600))
			h := NewSessionFinalizeHandlerForTest(nil)
			recovered, err := recoverRawFromSessionFile(h.logger, recPath, dir, rawPath)
			if mode == "missing-cursor" {
				require.ErrorContains(t, err, "no reliable native recovery cursor")
				require.NoFileExists(t, filepath.Join(dir, ".capture-source.json"))
				return
			}
			require.NoError(t, err)
			require.True(t, recovered)
			proof, err := session.ReadCaptureSource(dir)
			require.NoError(t, err)
			require.NotNil(t, proof)
			require.Equal(t, id, proof.NativeSessionID)
			require.Len(t, proof.Ranges, 1)
			if mode == "pending-drain" || mode == "hook-prefix" {
				require.Equal(t, int64(len(header+line+line)), proof.Ranges[0].End)
				require.Equal(t, 2, countRawJSONLEntries(t, rawPath))
			} else {
				require.Equal(t, state.SourceOffset, proof.Ranges[0].End)
				after, err := os.ReadFile(rawPath)
				require.NoError(t, err)
				require.Equal(t, original, after)
			}
			// Direct SessionEnd IPC cannot bypass the detector retiring this cursor.
			require.ErrorContains(t, checkNativeCaptureReady(dir), "recovery pending")
		})
	}
}
