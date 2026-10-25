package agentwork

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
)

// ampSidecarAdapter reads ox-bridge sidecar lines with their own timestamps;
// testAdapter stamps every entry with the time it was read.
type ampSidecarAdapter struct{ testAdapter }

func (a *ampSidecarAdapter) ReadFromOffset(path string, offset int64) ([]adapters.RawEntry, int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, offset, err
	}
	var entries []adapters.RawEntry
	for _, line := range strings.SplitAfter(string(data[offset:]), "\n") {
		var l struct {
			Type      string    `json:"type"`
			Content   string    `json:"content"`
			Timestamp time.Time `json:"timestamp"`
		}
		if json.Unmarshal([]byte(line), &l) == nil {
			entries = append(entries, adapters.RawEntry{Role: l.Type, Content: l.Content, Timestamp: l.Timestamp})
		}
	}
	return entries, int64(len(data)), nil
}

// Customer promise (sageox/ox#1318): an Amp session the daemon finishes after
// Amp is gone, with no stop, still holds the prompt that started it. The
// ox-bridge plugin writes that prompt before the model runs `ox agent prime`.
func TestNativeRecovery_AmpKeepsThePromptThatStartedTheRecording(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	ledgerPath := t.TempDir()
	sessionDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", "recovery-OxAmp")
	require.NoError(t, os.MkdirAll(sessionDir, 0o700))

	startedAt := time.Now().Add(-time.Hour).UTC()
	line := func(kind, content string, at time.Time) string {
		b, err := json.Marshal(map[string]any{"type": kind, "content": content, "timestamp": at})
		require.NoError(t, err)
		return string(b) + "\n"
	}
	earlier := line("user", "What does the billing service retry on?", startedAt.Add(-30*time.Minute))
	sidecar := filepath.Join(home, ".cache", "amp", "ox-sessions", "T-1.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(sidecar), 0o700))
	require.NoError(t, os.WriteFile(sidecar, []byte(earlier+
		line("user", "Rename the retry helper", startedAt.Add(-20*time.Second))+
		line("assistant", "Renamed it.", startedAt.Add(time.Minute))), 0o600))

	// what prime records for an Amp thread: the sidecar, starting at the
	// prompt's line, in hook mode (nothing captured until the stop)
	state := session.RecordingState{
		AgentID: "OxAmp", AdapterName: "amp", WatchMode: "hook", WorkspacePath: t.TempDir(),
		SessionFile: sidecar, StartOffset: int64(len(earlier)), StartOffsetKnown: true, SourceOffset: int64(len(earlier)),
		SessionPath: sessionDir, ParentPID: 99999999, StartedAt: startedAt,
	}
	writeRecordingState(t, filepath.Join(sessionDir, recordingMarker), state)
	rawPath := filepath.Join(sessionDir, artifactRaw)
	require.NoError(t, os.WriteFile(rawPath, []byte("{\"_meta\":{\"agent_type\":\"amp\"}}\n"), 0o600))
	adapters.Register(&ampSidecarAdapter{testAdapter{name: "amp"}})
	t.Cleanup(func() { adapters.Unregister("amp") })

	items := NewSessionFinalizeHandlerForTest(nil).DetectOrphanedForAgent(ledgerPath, state.AgentID, state.ParentPID)

	require.Len(t, items, 1)
	stored, err := session.ReadSessionFromPath(rawPath)
	require.NoError(t, err)
	var got []any
	for _, e := range stored.Entries {
		got = append(got, e["content"])
	}
	assert.Equal(t, []any{"Rename the retry helper", "Renamed it."}, got,
		"the session holds the prompt that started it, and no turn from before")
}
