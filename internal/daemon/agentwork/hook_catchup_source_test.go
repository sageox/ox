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

const foreignTurn = "a turn from another transcript"

// foreignTranscript is another Claude Code transcript of this repository
// under the coworker's own ~/.claude/projects: the source checks alone accept it.
func (f *catchUpFixture) foreignTranscript(name string) string {
	f.t.Helper()
	path := filepath.Join(f.home, ".claude", "projects", "other", name)
	require.NoError(f.t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(f.t, os.WriteFile(path, fmt.Appendf(nil,
		"{\"type\":\"assistant\",\"role\":\"assistant\",\"content\":%q,\"sessionId\":\"other\",\"cwd\":%q,\"timestamp\":%q}\n",
		foreignTurn, f.projectRoot, f.state.StartedAt.Add(time.Second).Format(time.RFC3339Nano)), 0o600))
	settle(f.t, path)
	return path
}

// rewriteMarker edits the recording's own marker in place, as anyone who can
// write its folder can: the Ledger checkout's cache, for one.
func (f *catchUpFixture) rewriteMarker(fields map[string]any) {
	f.t.Helper()
	path := filepath.Join(f.state.SessionPath, ".recording.json")
	data, err := os.ReadFile(path)
	require.NoError(f.t, err)
	var marker map[string]any
	require.NoError(f.t, json.Unmarshal(data, &marker))
	for k, v := range fields {
		marker[k] = v
	}
	data, err = json.Marshal(marker)
	require.NoError(f.t, err)
	require.NoError(f.t, os.WriteFile(path, data, 0o600))
}

// Failure prevented: the catch-up trusted the transcript a recording's marker
// names once the marker was shown to be one ox started here. Rewritten in
// place to name another transcript under ~/.claude/projects (the source checks
// accept any), the marker had the daemon import that conversation into this
// recording's raw.jsonl and live stream. Only the transcript a local hook
// found for the recording is read; any other is refused with one warning.
func TestHookCatchUpNeverReadsATranscriptTheMarkerWasRewrittenToName(t *testing.T) {
	f := newCatchUpFixture(t)
	streamAs(t, f.state.SessionPath, f.state.SessionID)
	other := f.foreignTranscript("other.jsonl")
	before := f.raw()
	f.rewriteMarker(map[string]any{"session_file": other, "source_offset": 0, "start_offset": 0})
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(prev) })

	m := f.watcher(logger)
	spooled := watchSpools(t, m)
	for range 2 {
		assert.Equal(t, 0, m.CatchUpHookRecordings(context.Background(), f.projectRoot))
	}

	assert.Zero(t, f.adapter.reads.Load(), "the rewritten transcript was read")
	assert.Equal(t, before, f.raw(), "the rewritten transcript was appended")
	assert.NotContains(t, spooled.all(), foreignTurn, "the rewritten transcript was published")
	assert.Equal(t, 1, strings.Count(logs.String(), "level=WARN"), "want one warning, not one per sweep:\n%s", logs.String())
	assert.Contains(t, logs.String(), "not the one a local hook found")
}

// Claude Code can replace a recording's transcript (it shrank or went away).
// The next hook's stdin names the replacement and moves the binding, and the
// catch-up follows it. The catch-up itself never rediscovers: a capture
// bounded to the transcript it stat'd leaves a replaced one, and the marker,
// to the next hook.
func TestHookCatchUpFollowsTheHookToAReplacedTranscript(t *testing.T) {
	f := newCatchUpFixture(t)
	m := f.watcher(slog.Default())
	settledSize := f.recording().SourceOffset
	// the same native session (Claude Code names a transcript by its session)
	replacement := filepath.Join(f.home, ".claude", "projects", "repo-moved", "session.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(replacement), 0o700))
	require.NoError(t, os.WriteFile(replacement, fmt.Appendf(nil, "{\"type\":\"session\",\"sessionId\":\"session\",\"cwd\":%q}\n", f.projectRoot), 0o600))
	require.NoError(t, os.Truncate(f.source, 0)) // Claude Code started over in a new file
	f.adapter.found = replacement

	// the daemon's capture, bounded to what it stat'd before the replacement
	capture, err := f.capture(session.HookCaptureOptions{
		SourceHome: f.home, Settled: session.SettledSource{Path: f.source, Size: settledSize},
	})
	require.NoError(t, err)
	assert.Equal(t, session.HookStatusUnsettled, capture.Status)
	assert.Equal(t, f.source, f.recording().SessionFile, "the catch-up's capture rediscovered a transcript")

	// the next hook rediscovers the replacement, and its stdin names it
	_, err = f.capture(hookStdin(replacement, "session"))
	require.NoError(t, err)
	require.Equal(t, replacement, f.recording().SessionFile)
	f.adapter.found = ""
	f.source = replacement
	f.say("assistant", "an answer in the new transcript")
	f.settle()

	assert.Equal(t, 1, m.CatchUpHookRecordings(context.Background(), f.projectRoot))
	assert.Equal(t, 1, strings.Count(f.raw(), "an answer in the new transcript"))
}

// A transcript no local hook has found (here, one set on the marker by
// anything but a hook's discovery) is not read: the recording waits for the
// hook that discovers it, quietly, since that is a normal state.
func TestHookCatchUpWaitsForAHookToFindTheTranscript(t *testing.T) {
	f := newCatchUpFixture(t)
	other := f.foreignTranscript("unbound.jsonl")
	// a second recording of this agent's teammate, its transcript set directly
	unbound, err := session.StartRecording(f.projectRoot, session.StartRecordingOptions{
		AgentID: "OxUnbound", AdapterName: "claude-code", Username: "person-a", WatchMode: "hook", WorkspacePath: f.projectRoot,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(unbound.SessionPath, artifactRaw), []byte(`{"type":"header","metadata":{}}`+"\n"), 0o600))
	require.NoError(t, session.UpdateRecordingStateForAgent(f.projectRoot, "OxUnbound", func(s *session.RecordingState) { s.SessionFile = other }))
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	m := NewSessionWatcherManager(logger)
	m.NoteHook(context.Background(), f.projectRoot, "OxUnbound")

	for range 2 {
		assert.Equal(t, 0, m.CatchUpHookRecordings(context.Background(), f.projectRoot))
	}
	assert.Zero(t, f.adapter.reads.Load(), "a transcript no hook found was read")
	assert.NotContains(t, logs.String(), "level=WARN", "waiting for a hook is normal, not a warning")
}
