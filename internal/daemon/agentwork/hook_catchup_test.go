package agentwork

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingClaudeAdapter reads {"role","content","timestamp"} records through
// adapterruntime.TailJSONL, as the real adapter tails, and counts its reads:
// in production every read is an adapter process.
type countingClaudeAdapter struct {
	mockReadAdapter
	reads atomic.Int64
}

func (*countingClaudeAdapter) Name() string { return "claude-code" }

func (a *countingClaudeAdapter) ReadFromOffset(path string, offset int64) ([]adapters.RawEntry, int64, error) {
	a.reads.Add(1)
	var entries []adapters.RawEntry
	_, next, err := adapterruntime.TailJSONL(path, offset, func(line []byte) ([]adapterprotocol.RawEntry, error) {
		var rec struct{ Role, Content, Timestamp string }
		if json.Unmarshal(line, &rec) != nil || rec.Role == "" {
			return nil, nil // a record the adapter does not model
		}
		ts, _ := time.Parse(time.RFC3339Nano, rec.Timestamp)
		entries = append(entries, adapters.RawEntry{Role: rec.Role, Content: rec.Content, Timestamp: ts})
		return nil, nil
	})
	return entries, next, err
}

type catchUpFixture struct {
	t           *testing.T
	adapter     *countingClaudeAdapter
	projectRoot string
	agentID     string
	source      string
	state       *session.RecordingState
	turns       int
}

// newCatchUpFixture is an active hook-mode Claude Code recording whose hooks
// have captured everything up to the transcript's end.
func newCatchUpFixture(t *testing.T) *catchUpFixture {
	t.Helper()
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	adapter := &countingClaudeAdapter{}
	adapters.Register(adapter)
	t.Cleanup(func() { adapters.Unregister("claude-code") })

	projectRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".sageox", "config.json"), []byte(`{"config_version":"2","repo_id":"repo_catchup_test"}`), 0o644))
	f := &catchUpFixture{t: t, adapter: adapter, projectRoot: projectRoot, agentID: "OxCatch"}
	state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID: f.agentID, AdapterName: "claude-code", Username: "person-a", WatchMode: "hook", WorkspacePath: projectRoot,
	})
	require.NoError(t, err)
	f.source = filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(f.source, fmt.Appendf(nil, "{\"type\":\"session\",\"sessionId\":\"session\",\"cwd\":%q}\n", projectRoot), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(state.SessionPath, artifactRaw), []byte(`{"type":"header","metadata":{}}`+"\n"), 0o600))
	info, err := os.Stat(f.source)
	require.NoError(t, err)
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, f.agentID, func(s *session.RecordingState) {
		s.SessionFile = f.source
		s.SourceOffset = info.Size() // the hooks have read everything so far
	}))
	f.state = state
	return f
}

func (f *catchUpFixture) say(role, text string) {
	f.t.Helper()
	f.turns++
	rec, err := json.Marshal(map[string]string{
		"type": role, "role": role, "content": text, "sessionId": "session", "cwd": f.projectRoot,
		"timestamp": f.state.StartedAt.Add(time.Duration(f.turns) * time.Second).Format(time.RFC3339Nano),
	})
	require.NoError(f.t, err)
	file, err := os.OpenFile(f.source, os.O_WRONLY|os.O_APPEND, 0o600)
	require.NoError(f.t, err)
	_, err = file.Write(append(rec, '\n'))
	require.NoError(f.t, err)
	require.NoError(f.t, file.Close())
}

func (f *catchUpFixture) settle() {
	f.t.Helper()
	old := time.Now().Add(-time.Minute)
	require.NoError(f.t, os.Chtimes(f.source, old, old))
}

func (f *catchUpFixture) raw() string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.state.SessionPath, artifactRaw))
	require.NoError(f.t, err)
	return string(b)
}

// Bytes a hook is about to read belong to the hook: only it has the tool error
// from its stdin. The catch-up takes bytes only once they sat unread.
func TestHookCatchUpLeavesFreshBytesToTheHook(t *testing.T) {
	f := newCatchUpFixture(t)
	m := NewSessionWatcherManager(slog.Default())
	f.say("assistant", "just written")

	assert.Equal(t, 0, m.CatchUpHookRecordings(context.Background(), f.projectRoot))
	assert.Zero(t, f.adapter.reads.Load(), "fresh bytes must not even be read")

	f.settle()
	assert.Equal(t, 1, m.CatchUpHookRecordings(context.Background(), f.projectRoot))
	assert.Equal(t, 1, strings.Count(f.raw(), "just written"))
}

// The catch-up must never wait on a hook (or the stop) holding the capture
// lock: it skips the cycle at once and captures on a later one.
func TestHookCatchUpSkipsACycleWhileTheCaptureLockIsHeld(t *testing.T) {
	f := newCatchUpFixture(t)
	m := NewSessionWatcherManager(slog.Default())
	f.say("assistant", "late answer")
	f.settle()

	err := fileutil.WithFileLock(context.Background(), filepath.Join(f.state.SessionPath, artifactRaw), func() error {
		start := time.Now()
		assert.Equal(t, 0, m.CatchUpHookRecordings(context.Background(), f.projectRoot))
		assert.Less(t, time.Since(start), time.Second, "the catch-up waited on the lock")
		return nil
	})
	require.NoError(t, err)
	assert.NotContains(t, f.raw(), "late answer")

	assert.Equal(t, 1, m.CatchUpHookRecordings(context.Background(), f.projectRoot))
	assert.Contains(t, f.raw(), "late answer")
}

// Claude Code writes records its adapter turns into no entries, and the cursor
// stays short of them while the session idles. Each idle sweep must not run
// the adapter again over the same bytes.
func TestHookCatchUpReadsAnUnchangedTranscriptOnce(t *testing.T) {
	f := newCatchUpFixture(t)
	m := NewSessionWatcherManager(slog.Default())
	file, err := os.OpenFile(f.source, os.O_WRONLY|os.O_APPEND, 0o600)
	require.NoError(t, err)
	_, err = file.WriteString(`{"type":"file-history-snapshot"}` + "\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())
	f.settle()

	for range 3 {
		assert.Equal(t, 0, m.CatchUpHookRecordings(context.Background(), f.projectRoot))
	}
	assert.Equal(t, int64(1), f.adapter.reads.Load(), "an unchanged transcript was read on every sweep")

	f.say("assistant", "a real answer")
	f.settle()
	assert.Equal(t, 1, m.CatchUpHookRecordings(context.Background(), f.projectRoot), "a transcript that changed is read again")
	assert.Contains(t, f.raw(), "a real answer")
}

// Only hook-mode recordings are the catch-up's: a tail-mode recording has its
// own watcher, and two writers would race its cursor.
func TestHookCatchUpLeavesTailRecordingsToTheirWatcher(t *testing.T) {
	f := newCatchUpFixture(t)
	require.NoError(t, session.UpdateRecordingStateForAgent(f.projectRoot, f.agentID, func(s *session.RecordingState) { s.WatchMode = "tail" }))
	f.say("assistant", "tailed answer")
	f.settle()

	assert.Equal(t, 0, NewSessionWatcherManager(slog.Default()).CatchUpHookRecordings(context.Background(), f.projectRoot))
	assert.NotContains(t, f.raw(), "tailed answer")
}
