package agentwork

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/livesession"
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
	// beforeRead, when set, runs as a read starts: the agent writing between
	// the daemon's settle check and its read.
	beforeRead func()
	// found is the transcript FindSessionFile rediscovers, if any.
	found string
}

func (*countingClaudeAdapter) Name() string { return "claude-code" }

func (a *countingClaudeAdapter) FindSessionFile(adapters.SessionLookup) (string, error) {
	if a.found == "" {
		return "", adapters.ErrSessionNotFound
	}
	return a.found, nil
}

func (a *countingClaudeAdapter) ReadFromOffset(path string, offset int64) ([]adapters.RawEntry, int64, error) {
	a.reads.Add(1)
	if a.beforeRead != nil {
		a.beforeRead()
	}
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
	home        string
	projectRoot string
	agentID     string
	source      string
	state       *session.RecordingState
	turns       int
}

// newCatchUpFixture is an active hook-mode Claude Code recording whose hooks
// have captured everything up to the transcript's end. The transcript sits
// under Claude Code's allowed root in $HOME, which the manager confines to.
func newCatchUpFixture(t *testing.T) *catchUpFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", home)
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
	f := &catchUpFixture{t: t, adapter: adapter, home: home, projectRoot: projectRoot, agentID: "OxCatch"}
	state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID: f.agentID, AdapterName: "claude-code", Username: "person-a", WatchMode: "hook", WorkspacePath: projectRoot,
	})
	require.NoError(t, err)
	f.source = filepath.Join(home, ".claude", "projects", "repo", "session.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(f.source), 0o700))
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
	f.sayAt(role, text, f.state.StartedAt.Add(time.Duration(f.turns)*time.Second))
}

func (f *catchUpFixture) sayAt(role, text string, at time.Time) {
	f.t.Helper()
	rec, err := json.Marshal(map[string]string{
		"type": role, "role": role, "content": text, "sessionId": "session", "cwd": f.projectRoot,
		"timestamp": at.Format(time.RFC3339Nano),
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
	settle(f.t, f.source)
}

// settle backdates path past the catch-up's settle window.
func settle(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-time.Minute)
	require.NoError(t, os.Chtimes(path, old, old))
}

func (f *catchUpFixture) rawPath() string { return filepath.Join(f.state.SessionPath, artifactRaw) }

func (f *catchUpFixture) raw() string {
	f.t.Helper()
	b, err := os.ReadFile(f.rawPath())
	require.NoError(f.t, err)
	return string(b)
}

func (f *catchUpFixture) recording() *session.RecordingState {
	f.t.Helper()
	st, err := session.LoadRecordingStateForAgent(f.projectRoot, f.agentID)
	require.NoError(f.t, err)
	require.NotNil(f.t, st)
	return st
}

// capture runs the capture transaction under the raw.jsonl lock, as a hook does.
func (f *catchUpFixture) capture(opts session.HookCaptureOptions) (session.HookCapture, error) {
	f.t.Helper()
	var capture session.HookCapture
	err := fileutil.WithFileLock(context.Background(), f.rawPath(), func() error {
		var err error
		capture, err = session.CaptureHookEntries(context.Background(), f.projectRoot, f.agentID, f.state.SessionPath, f.state.SessionID, opts)
		return err
	})
	return capture, err
}

func skipAsRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("reason: root ignores file permissions, so the I/O failure cannot be simulated")
	}
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

// The catch-up sweeps markers it did not write (the Ledger checkout's, legacy
// sessions/ folders'), so it reads only what the tail watcher would: a
// transcript under its adapter's allowed roots, symlinks resolved. A refusal
// is logged once, not every sweep.
func TestHookCatchUpRefusesATranscriptOutsideTheAdapterRoots(t *testing.T) {
	plants := map[string]func(t *testing.T, f *catchUpFixture, outside string) string{
		"outside the roots": func(_ *testing.T, _ *catchUpFixture, outside string) string { return outside },
		"symlink out of the roots": func(t *testing.T, f *catchUpFixture, outside string) string {
			link := filepath.Join(filepath.Dir(f.source), "linked.jsonl")
			require.NoError(t, os.Symlink(outside, link))
			return link
		},
	}
	for name, plant := range plants {
		t.Run(name, func(t *testing.T) {
			f := newCatchUpFixture(t)
			var logs bytes.Buffer
			m := NewSessionWatcherManager(slog.New(slog.NewTextHandler(&logs, nil)))
			f.say("assistant", "planted answer")
			b, err := os.ReadFile(f.source)
			require.NoError(t, err)
			outside := filepath.Join(t.TempDir(), "session.jsonl")
			require.NoError(t, os.WriteFile(outside, b, 0o600))
			settle(t, outside)
			planted := plant(t, f, outside)
			require.NoError(t, session.UpdateRecordingStateForAgent(f.projectRoot, f.agentID, func(s *session.RecordingState) {
				s.SessionFile = planted
			}))

			for range 2 {
				assert.Equal(t, 0, m.CatchUpHookRecordings(context.Background(), f.projectRoot))
			}
			assert.Zero(t, f.adapter.reads.Load(), "a transcript outside the roots was read")
			assert.NotContains(t, f.raw(), "planted answer")
			assert.Equal(t, 1, strings.Count(logs.String(), "session_capture_catchup_refused"), "want one warning per recording")
		})
	}
}

// The settle check is a stat; the adapter then reads to the end of the file.
// A record the agent wrote in between is not the daemon's: its PostToolUse hook
// has the error to attach. Nothing commits until the whole read has settled,
// and a settled transcript no hook follows is still captured.
func TestHookCatchUpCommitsOnlyBytesThatSettled(t *testing.T) {
	f := newCatchUpFixture(t)
	m := NewSessionWatcherManager(slog.Default())
	f.say("assistant", "settled answer")
	f.settle()
	f.adapter.beforeRead = func() {
		f.adapter.beforeRead = nil
		f.say("tool", "fresh tool result")
	}

	assert.Equal(t, 0, m.CatchUpHookRecordings(context.Background(), f.projectRoot), "bytes written after the settle check were committed")
	assert.NotContains(t, f.raw(), "fresh tool result")
	assert.NotContains(t, f.raw(), "settled answer", "a read past the settled size committed part of itself")

	f.settle()
	assert.Equal(t, 1, m.CatchUpHookRecordings(context.Background(), f.projectRoot))
	assert.Equal(t, 1, strings.Count(f.raw(), "settled answer"))
	assert.Equal(t, 1, strings.Count(f.raw(), "fresh tool result"))
}

// Every entry filtered out still moves the cursor, so that write is the
// capture: a cursor that did not persist is a failure, backed off like one,
// never progress.
func TestHookCatchUpCountsAnUnpersistedCursorAsAFailure(t *testing.T) {
	skipAsRoot(t)
	f := newCatchUpFixture(t)
	var logs bytes.Buffer
	m := NewSessionWatcherManager(slog.New(slog.NewTextHandler(&logs, nil)))
	f.sayAt("assistant", "said before the recording", f.state.StartedAt.Add(-time.Hour))
	f.settle()
	cursor := f.recording().SourceOffset
	info, err := os.Stat(f.state.SessionPath)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(f.state.SessionPath, 0o500)) // the state file can no longer be replaced
	t.Cleanup(func() { _ = os.Chmod(f.state.SessionPath, info.Mode().Perm()) })

	assert.Equal(t, 0, m.CatchUpHookRecordings(context.Background(), f.projectRoot), "a cursor that did not persist was counted as progress")
	assert.Contains(t, logs.String(), "session_capture_catchup_failed")
	assert.Equal(t, 0, m.CatchUpHookRecordings(context.Background(), f.projectRoot))
	assert.Equal(t, int64(1), f.adapter.reads.Load(), "a failed checkpoint was read again without the failure backoff")

	require.NoError(t, os.Chmod(f.state.SessionPath, info.Mode().Perm()))
	assert.Equal(t, cursor, f.recording().SourceOffset)
}

// The daemon cannot write raw.jsonl's header, so it never appends to a
// raw.jsonl without one: an entry ahead of the header leaves it headerless for
// good. A zero-byte file has no header either.
func TestHookCatchUpNeverAppendsToAHeaderlessRawFile(t *testing.T) {
	f := newCatchUpFixture(t)
	m := NewSessionWatcherManager(slog.Default())
	require.NoError(t, os.Truncate(f.rawPath(), 0))
	f.say("assistant", "answer before the header")
	f.settle()

	assert.Equal(t, 0, m.CatchUpHookRecordings(context.Background(), f.projectRoot))
	assert.Empty(t, f.raw(), "entries were appended to a raw.jsonl with no header")
	assert.Zero(t, f.adapter.reads.Load(), "the adapter ran for a capture that could not append")
}

// A recording whose raw.jsonl a hook has not started yet is not idle: once a
// hook writes the header, the next sweep captures the unread bytes even though
// the transcript did not change.
func TestHookCatchUpRetriesARecordingWaitingForItsRawFile(t *testing.T) {
	f := newCatchUpFixture(t)
	m := NewSessionWatcherManager(slog.Default())
	require.NoError(t, os.Remove(f.rawPath()))
	f.say("assistant", "answer before raw.jsonl")
	f.settle()

	assert.Equal(t, 0, m.CatchUpHookRecordings(context.Background(), f.projectRoot))
	require.NoError(t, os.WriteFile(f.rawPath(), []byte(`{"type":"header","metadata":{}}`+"\n"), 0o600)) // a hook starts it

	assert.Equal(t, 1, m.CatchUpHookRecordings(context.Background(), f.projectRoot), "a recording waiting for raw.jsonl was remembered as idle")
	assert.Contains(t, f.raw(), "answer before raw.jsonl")
}

// A hook writes the header itself: raw.jsonl starts with it whether the file
// is missing or was left empty.
func TestHookCaptureWritesTheHeaderBeforeTheFirstEntry(t *testing.T) {
	prepare := map[string]func(raw string) error{
		"missing": os.Remove,
		"empty":   func(raw string) error { return os.Truncate(raw, 0) },
	}
	for name, prep := range prepare {
		t.Run(name, func(t *testing.T) {
			f := newCatchUpFixture(t)
			require.NoError(t, prep(f.rawPath()))
			f.say("assistant", "first answer")

			_, err := f.capture(session.HookCaptureOptions{WriteHeader: func(*session.RecordingState) error {
				return os.WriteFile(f.rawPath(), []byte(`{"type":"header","metadata":{}}`+"\n"), 0o600)
			}})

			require.NoError(t, err)
			first, rest, _ := strings.Cut(f.raw(), "\n")
			var line struct{ Type string }
			require.NoError(t, json.Unmarshal([]byte(first), &line))
			assert.Equal(t, "header", line.Type, "raw.jsonl does not start with its header")
			assert.Contains(t, rest, "first answer")
		})
	}
}

// A header that cannot be written stops the capture: nothing is appended and
// the cursor stays, so a later capture starts raw.jsonl properly.
func TestHookCaptureAppendsNothingWhenTheHeaderFails(t *testing.T) {
	f := newCatchUpFixture(t)
	require.NoError(t, os.Remove(f.rawPath()))
	f.say("assistant", "answer with no header")
	cursor := f.recording().SourceOffset

	_, err := f.capture(session.HookCaptureOptions{WriteHeader: func(*session.RecordingState) error {
		return errors.New("disk full")
	}})

	require.Error(t, err, "a header failure was swallowed")
	assert.NoFileExists(t, f.rawPath(), "entries were appended without the header")
	assert.Equal(t, cursor, f.recording().SourceOffset)
}

// A batch committed to raw.jsonl that cannot be read back never reaches the
// live stream, and the cursor is past it, so nothing would ever send it. It is
// declared: the stream carries a gap of its size in its place.
func TestHookCatchUpDeclaresABatchItCouldNotSpool(t *testing.T) {
	skipAsRoot(t)
	f := newCatchUpFixture(t)
	m := NewSessionWatcherManager(slog.Default())
	var spooled atomic.Int64
	m.SetLiveSpooler(func(string, session.AppendedBatch) { spooled.Add(1) })
	_, err := livesession.Prepare(context.Background(), livesession.Config{Endpoint: "https://live.test.sageox.ai", DataDir: t.TempDir()},
		f.state.SessionPath, livesession.OpenParams{SessionID: f.state.SessionID, SessionName: filepath.Base(f.state.SessionPath)})
	require.NoError(t, err)
	// past the first batch, the append never reads raw.jsonl to restore redaction state
	require.NoError(t, session.UpdateRecordingStateForAgent(f.projectRoot, f.agentID, func(s *session.RecordingState) {
		s.CommandRedactionVersion = 1
	}))
	f.say("assistant", "answer the stream lost")
	f.settle()
	before, err := os.Stat(f.rawPath())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(f.rawPath(), 0o200)) // appendable, but the batch cannot be read back
	t.Cleanup(func() { _ = os.Chmod(f.rawPath(), 0o600) })

	assert.Equal(t, 1, m.CatchUpHookRecordings(context.Background(), f.projectRoot))
	require.NoError(t, os.Chmod(f.rawPath(), 0o600))
	assert.Contains(t, f.raw(), "answer the stream lost", "the batch must stay committed to raw.jsonl")
	assert.Zero(t, spooled.Load())
	after, err := os.Stat(f.rawPath())
	require.NoError(t, err)

	res, err := livesession.Spool(context.Background(), f.state.SessionPath, []byte(`{"type":"msg"}`+"\n"), livesession.SpoolOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, res.Gaps, "the next write declared no gap for the lost batch")
	spool, err := os.ReadFile(filepath.Join(f.state.SessionPath, "spool.jsonl"))
	require.NoError(t, err)
	gapLine, _, _ := bytes.Cut(spool, []byte("\n"))
	dec := json.NewDecoder(bytes.NewReader(gapLine))
	dec.DisallowUnknownFields()
	var gap struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
		Bytes  int64  `json:"bytes"`
	}
	require.NoError(t, dec.Decode(&gap))
	assert.Equal(t, "gap", gap.Type)
	assert.Equal(t, "spool_write_failed", gap.Reason)
	assert.Equal(t, after.Size()-before.Size(), gap.Bytes, "the gap must cover exactly the committed batch")
}
