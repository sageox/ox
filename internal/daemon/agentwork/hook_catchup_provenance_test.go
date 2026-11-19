package agentwork

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const plantedTurn = "a planted turn"

// plantBeside writes a recording marker into the coworker's own sessions
// folder, where a Ledger pull can put one, under a name that sorts ahead of
// the live recording's. It names a Claude Code transcript inside Claude
// Code's own root, so the source checks alone would accept it, and carries
// its own raw.jsonl and live stream state. claims overrides marker fields.
func (f *catchUpFixture) plantBeside(claims map[string]any) (dir string, marker []byte) {
	f.t.Helper()
	t := f.t
	dir = filepath.Join(filepath.Dir(f.state.SessionPath), "0000-planted")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	source := filepath.Join(f.home, ".claude", "projects", "other", "planted.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(source), 0o700))
	require.NoError(t, os.WriteFile(source, fmt.Appendf(nil,
		"{\"type\":\"assistant\",\"role\":\"assistant\",\"content\":%q,\"sessionId\":\"planted\",\"cwd\":%q,\"timestamp\":%q}\n",
		plantedTurn, f.projectRoot, time.Now().Add(-time.Hour).Format(time.RFC3339Nano)), 0o600))
	settle(t, source)
	fields := map[string]any{
		"agent_id": f.agentID, "session_id": "ses_planted", "adapter_name": "claude-code", "watch_mode": "hook",
		"session_path": dir, "workspace_path": f.projectRoot, "session_file": source,
	}
	for k, v := range claims {
		fields[k] = v
	}
	marker, err := json.Marshal(fields)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), marker, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, artifactRaw), []byte(`{"type":"header","metadata":{}}`+"\n"), 0o600))
	streamAs(t, dir, fields["session_id"].(string))
	return dir, marker
}

// streamAs makes dir a live-streaming session folder for sessionID, so
// whatever the catch-up commits there is handed to the live spool.
func streamAs(t *testing.T, dir, sessionID string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stream-state.json"), fmt.Appendf(nil, `{"session_id":%q,"status":"pending"}`, sessionID), 0o600))
}

// spoolLog reads back the live spool of every session the watcher asked a
// drain for: the step asks for one whenever it spooled a batch.
type spoolLog struct {
	t      *testing.T
	drains *drainLog
}

// watchSpools streams m's captures (wireLiveStreaming) and logs what they spool.
func watchSpools(t *testing.T, m *SessionWatcherManager) *spoolLog {
	return &spoolLog{t: t, drains: wireLiveStreaming(m)}
}

func (s *spoolLog) all() string {
	seen := map[string]bool{}
	var spools []string
	for _, dir := range s.drains.all() {
		if !seen[dir] {
			seen[dir] = true
			spools = append(spools, filepath.Base(dir)+": "+spoolOf(s.t, dir))
		}
	}
	return strings.Join(spools, "\n")
}

// A heartbeat is not a hook: every ox command sends one with the agent's ID.
// A marker planted under a live agent's ID, beside that agent's recording,
// is armed by the agent's next heartbeat and swept with the rest. Even when
// it names the live recording's folder and session ID, and a transcript in
// Claude Code's own root, it must never be read, appended or published, and
// the recording ox actually started is caught up as before.
func TestHookCatchUpNeverReadsAPlantedMarkerReusingALiveAgentID(t *testing.T) {
	plants := map[string]func(f *catchUpFixture) map[string]any{
		"its own folder and session": func(*catchUpFixture) map[string]any { return nil },
		"the live recording's folder and session": func(f *catchUpFixture) map[string]any {
			return map[string]any{"session_id": f.state.SessionID, "session_path": f.state.SessionPath}
		},
	}
	for name, claims := range plants {
		t.Run(name, func(t *testing.T) {
			f := newCatchUpFixture(t)
			streamAs(t, f.state.SessionPath, f.state.SessionID)
			plantedDir, marker := f.plantBeside(claims(f))
			plantedRaw, err := os.ReadFile(filepath.Join(plantedDir, artifactRaw))
			require.NoError(t, err)
			f.say("assistant", "the coworker's late answer")
			f.settle()

			m := f.watcher(slog.Default()) // the agent's heartbeat
			spooled := watchSpools(t, m)
			for range 2 {
				m.CatchUpHookRecordings(context.Background(), f.projectRoot)
			}

			assert.NotContains(t, f.raw(), plantedTurn, "the planted transcript was appended to the live recording")
			assert.NotContains(t, spooled.all(), plantedTurn, "the planted transcript was published")
			got, err := os.ReadFile(filepath.Join(plantedDir, artifactRaw))
			require.NoError(t, err)
			assert.Equal(t, string(plantedRaw), string(got), "the planted recording was appended to")
			got, err = os.ReadFile(filepath.Join(plantedDir, ".recording.json"))
			require.NoError(t, err)
			assert.JSONEq(t, string(marker), string(got), "the planted marker was written to")

			// the recording ox started is caught up exactly as before
			assert.Equal(t, 1, strings.Count(f.raw(), "the coworker's late answer"))
			assert.Contains(t, spooled.all(), "the coworker's late answer")
			assert.Equal(t, int64(1), f.adapter.reads.Load(), "want one read: the live transcript's")
			quarantined, err := session.LoadQuarantinedRecordingsForAgent(f.projectRoot, f.agentID)
			require.NoError(t, err)
			assert.Empty(t, quarantined, "the planted marker got the live recording quarantined")
		})
	}
}

// A marker the catch-up refuses for provenance warns once, from the heartbeat
// or the sweep that meets it first, not once per heartbeat or per sweep. That
// covers a recording an older ox started, which is in no registry and is left
// to its hooks.
func TestHookCatchUpWarnsOnceForAMarkerOxDidNotStartHere(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(f *catchUpFixture)
		// wantLive is whether the live recording is still caught up
		wantLive bool
	}{
		{name: "planted under a live agent's ID", arrange: func(f *catchUpFixture) { f.plantBeside(nil) }, wantLive: true},
		{name: "planted under an agent that never heartbeats", arrange: func(f *catchUpFixture) {
			// only the sweep ever meets it
			f.plantBeside(map[string]any{"agent_id": "OxPlant"})
		}, wantLive: true},
		{name: "started by an older ox", arrange: func(f *catchUpFixture) {
			f.t.Setenv("XDG_DATA_HOME", f.t.TempDir()) // a registry that never heard of it
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCatchUpFixture(t)
			tt.arrange(f)
			f.say("assistant", "an answer")
			f.settle()
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			prev := slog.Default()
			slog.SetDefault(logger)
			t.Cleanup(func() { slog.SetDefault(prev) })

			m := f.watcher(logger)
			for range 2 {
				m.CatchUpHookRecordings(context.Background(), f.projectRoot)
				m.NoteHook(context.Background(), f.projectRoot, f.agentID)
			}

			assert.Equal(t, 1, strings.Count(logs.String(), "level=WARN"), "want one warning per marker, not per heartbeat or sweep:\n%s", logs.String())
			assert.Contains(t, logs.String(), "reason=not_started_here")
			if tt.wantLive {
				assert.Equal(t, 1, strings.Count(f.raw(), "an answer"))
				return
			}
			assert.Zero(t, f.adapter.reads.Load(), "a recording no registry names was read")
			assert.NotContains(t, f.raw(), "an answer")
		})
	}
}
