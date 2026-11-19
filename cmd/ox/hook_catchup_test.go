package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tailingClaudeAdapter reads the transcript through adapterruntime.TailJSONL,
// as the real Claude Code adapter does: an unterminated last record stays
// unread and the cursor stops before it. testClaudeCodeAdapter jumps its
// cursor to EOF, which would hide the failures these tests are about.
type tailingClaudeAdapter struct {
	testClaudeCodeAdapter
	// onRead, if set, runs as each read starts: inside the raw.jsonl lock of
	// the capture (hook or catch-up) that is reading.
	onRead func()
}

func (a *tailingClaudeAdapter) ReadFromOffset(path string, offset int64) ([]adapters.RawEntry, int64, error) {
	if a.onRead != nil {
		a.onRead()
	}
	var entries []adapters.RawEntry
	_, next, err := adapterruntime.TailJSONL(path, offset, func(line []byte) ([]adapterprotocol.RawEntry, error) {
		entries = append(entries, a.parseLine(line)...)
		return nil, nil
	})
	return entries, next, err
}

// newCatchUpFixture is a streaming Claude Code recording in hook watch mode,
// as prime starts one.
func newCatchUpFixture(t *testing.T) *liveFixture {
	t.Helper()
	f := newLiveFixture(t, liveOpts{streaming: true})
	adapters.Unregister("claude-code") // the fixture's adapter; its cleanup unregisters this one
	adapters.Register(&tailingClaudeAdapter{})
	// the daemon reads only a transcript inside Claude Code's own session
	// root, so this one sits where Claude Code writes it
	contained := filepath.Join(os.Getenv("HOME"), ".claude", "projects", "catch-up", "session.jsonl")
	transcript, err := os.ReadFile(f.sourceFile)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(contained), 0o755))
	require.NoError(t, os.WriteFile(contained, transcript, 0o644))
	f.sourceFile = contained
	require.NoError(t, session.UpdateRecordingStateForAgent(f.projectRoot, f.agentID, func(s *session.RecordingState) {
		s.SessionFile = contained
		s.WatchMode = "hook"
		s.WorkspacePath = f.projectRoot
	}))
	return f
}

// record is one native transcript line, stamped after the recording started.
func (f *liveFixture) record(role, text string) string {
	f.turns++
	at := f.state.StartedAt.Add(time.Duration(f.turns) * time.Second).Format(time.RFC3339Nano)
	if role == "user" {
		return `{"type":"user","timestamp":"` + at + `","message":{"role":"user","content":"` + text + `"}}`
	}
	return `{"type":"assistant","timestamp":"` + at + `","message":{"role":"assistant","content":[{"type":"text","text":"` + text + `"}]}}`
}

func (f *liveFixture) say(role, text string) {
	f.t.Helper()
	appendClaudeEntries(f.t, f.sourceFile, time.Time{}, f.record(role, text))
}

// stopHook runs Claude Code's Stop hook (end of a response turn).
func (f *liveFixture) stopHook() {
	f.t.Helper()
	require.NoError(f.t, handleStop(&HookContext{Phase: phaseStop, ProjectRoot: f.projectRoot, Marker: &SessionMarker{AgentID: f.agentID}}))
}

// settle ages the transcript past the catch-up's settle window, as the
// seconds after an agent's last write would.
func (f *liveFixture) settle() {
	f.t.Helper()
	old := time.Now().Add(-time.Minute)
	require.NoError(f.t, os.Chtimes(f.sourceFile, old, old))
}

// catchUpManager is the daemon's session watcher, spooling committed batches
// into the live spool as the daemon's live sessions do.
func (f *liveFixture) catchUpManager() *agentwork.SessionWatcherManager {
	m := agentwork.NewSessionWatcherManager(slog.Default())
	m.SetLiveSpooler(func(dir string, b session.AppendedBatch) {
		_, err := livesession.Spool(context.Background(), dir, b.Bytes, livesession.SpoolOptions{Paused: b.Suspended})
		assert.NoError(f.t, err)
	})
	return m
}

func (f *liveFixture) catchUp() int {
	return f.catchUpManager().CatchUpHookRecordings(context.Background(), f.projectRoot)
}

// partialAnswer is an assistant record split where a writer still flushing it
// would leave it: head is on disk, tail (with the newline) is not yet.
func (f *liveFixture) partialAnswer(text string) (head, tail []byte) {
	f.t.Helper()
	var rec map[string]any
	require.NoError(f.t, json.Unmarshal([]byte(f.record("assistant", text)), &rec))
	rec["sessionId"], rec["cwd"] = "session", f.projectRoot
	line, err := json.Marshal(rec)
	require.NoError(f.t, err)
	line = append(line, '\n')
	return line[:len(line)/2], line[len(line)/2:]
}

// appendSource writes b to the transcript; it may run off the test goroutine.
func (f *liveFixture) appendSource(b []byte) {
	file, err := os.OpenFile(f.sourceFile, os.O_WRONLY|os.O_APPEND, 0o644)
	if !assert.NoError(f.t, err) {
		return
	}
	_, err = file.Write(b)
	assert.NoError(f.t, err)
	assert.NoError(f.t, file.Close())
}

// Failure prevented (BDD run_mwnarlbj-a7f5): Claude Code fired a turn's Stop
// hook before its final answer reached the transcript, and no hook followed.
// A hook-mode transcript was read only by hooks, so the answer never reached
// raw.jsonl or the live stream. One daemon catch-up cycle must bring it to
// both, exactly once.
func TestCatchUpCapturesTheAnswerWrittenAfterTheLastStopHook(t *testing.T) {
	f := newCatchUpFixture(t)
	f.say("user", "question one")
	f.say("assistant", "answer one")
	f.stopHook()
	f.say("user", "question three")
	f.stopHook() // Stop fires; the answer is not on disk yet
	f.say("assistant", "answer three")
	f.settle()

	assert.Equal(t, 1, f.catchUp(), "one catch-up cycle must capture the answer written after the last hook")

	raw := string(f.rawBytes())
	assert.Equal(t, 1, strings.Count(raw, "answer three"), "raw.jsonl must hold the late answer exactly once")
	assert.Equal(t, raw, string(f.spoolBytes()), "the live spool must hold exactly what raw.jsonl holds")
	info, err := os.Stat(f.sourceFile)
	require.NoError(t, err)
	state, err := session.LoadRecordingStateForAgent(f.projectRoot, f.agentID)
	require.NoError(t, err)
	assert.Equal(t, info.Size(), state.SourceOffset, "the cursor must move past the captured answer")

	assert.Equal(t, 0, f.catchUp(), "a second cycle has nothing left to capture")
	assert.Equal(t, raw, string(f.rawBytes()))
}

// duringFirstRead runs fn as the first transcript read starts: inside the
// raw.jsonl lock of whichever capture reads first. Every later read goes
// straight on, even one fn itself is waiting for (sync.Once would block it).
func (f *liveFixture) duringFirstRead(fn func()) {
	f.t.Helper()
	a, err := adapters.GetAdapter("claude-code")
	require.NoError(f.t, err)
	tailing, ok := a.(*tailingClaudeAdapter)
	require.True(f.t, ok, "duringFirstRead needs newCatchUpFixture's adapter")
	var fired atomic.Bool
	tailing.onRead = func() {
		if fired.CompareAndSwap(false, true) {
			fn()
		}
	}
}

// capturedOnce asserts every text is in raw.jsonl exactly once, and that the
// live spool holds exactly what raw.jsonl holds.
func (f *liveFixture) capturedOnce(texts ...string) {
	f.t.Helper()
	raw := string(f.rawBytes())
	for _, text := range texts {
		assert.Equal(f.t, 1, strings.Count(raw, text), "%q must be in raw.jsonl exactly once", text)
	}
	assert.Equal(f.t, raw, string(f.spoolBytes()), "the live spool must hold exactly what raw.jsonl holds")
}

// A hook and the daemon's catch-up capturing one transcript contend for its
// raw.jsonl lock. Whichever takes it first, every line must reach raw.jsonl,
// and the live spool, exactly once. Each order is forced from inside the
// first capture's read, while it holds the lock, so scheduling decides
// nothing.
func TestHookAndCatchUpRacingCaptureEveryLineOnce(t *testing.T) {
	t.Run("hook holds the lock, the catch-up arrives", func(t *testing.T) {
		f := newCatchUpFixture(t)
		m := f.catchUpManager()
		f.say("user", "a question")
		f.say("assistant", "an answer")
		f.settle() // the catch-up would take these, but for the hook's lock

		caughtWhileHeld := -1
		f.duringFirstRead(func() {
			caught := make(chan int, 1)
			go func() { caught <- m.CatchUpHookRecordings(context.Background(), f.projectRoot) }()
			select {
			case caughtWhileHeld = <-caught:
			case <-time.After(10 * time.Second):
				t.Error("the catch-up waited on the hook's lock; it must skip a recording a hook is capturing")
			}
		})
		require.NoError(t, handleAfterTool(&HookContext{Phase: phaseAfterTool, ProjectRoot: f.projectRoot, Marker: &SessionMarker{AgentID: f.agentID}}))

		assert.Zero(t, caughtWhileHeld, "the catch-up must leave a recording to the hook holding its lock")
		assert.Zero(t, m.CatchUpHookRecordings(context.Background(), f.projectRoot), "the hook captured everything")
		f.capturedOnce("a question", "an answer")
	})

	t.Run("catch-up holds the lock, the hook arrives", func(t *testing.T) {
		f := newCatchUpFixture(t)
		m := f.catchUpManager()
		f.say("user", "a question")
		f.say("assistant", "an answer")
		f.settle()

		hookStarted := false
		hookDone := make(chan error, 1)
		f.duringFirstRead(func() {
			hookStarted = true // on this goroutine: the catch-up below runs on it
			go func() {
				hookDone <- handleAfterTool(&HookContext{Phase: phaseAfterTool, ProjectRoot: f.projectRoot, Marker: &SessionMarker{AgentID: f.agentID}})
			}()
		})
		assert.Equal(t, 1, m.CatchUpHookRecordings(context.Background(), f.projectRoot), "the catch-up took the lock first, so it captures")
		require.True(t, hookStarted, "the catch-up never read the transcript, so the hook never contended")
		require.NoError(t, <-hookDone)

		f.capturedOnce("a question", "an answer")
	})
}

// A recording that is stopping, stopped, held, gone or quarantined is finished
// as it stands: the catch-up must not add to it, however far its transcript
// ran past the cursor.
func TestCatchUpNeverTouchesAStoppedOrHeldRecording(t *testing.T) {
	cases := []struct {
		name  string
		apply func(t *testing.T, f *liveFixture)
	}{
		{"control: active recording", nil},
		{"stop requested", func(t *testing.T, f *liveFixture) {
			require.NoError(t, session.MarkExplicitStop(f.projectRoot, f.agentID))
		}},
		{"stopped", func(t *testing.T, f *liveFixture) {
			require.NoError(t, session.UpdateRecordingStateForAgent(f.projectRoot, f.agentID, func(s *session.RecordingState) {
				now := time.Now()
				s.StoppedAt = &now
			}))
		}},
		{"held on this machine", func(t *testing.T, f *liveFixture) {
			require.NoError(t, session.WriteHoldMarker(f.dir(), session.HoldManualPublishing, "test"))
		}},
		{"recording state removed, as abort leaves it", func(t *testing.T, f *liveFixture) {
			require.NoError(t, session.ClearRecordingStateAt(f.dir(), f.state.SessionID))
		}},
		{"quarantined", func(t *testing.T, f *liveFixture) {
			require.NoError(t, session.SetSourceRejectedAt(f.dir(), f.state.SessionID, true))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newCatchUpFixture(t)
			f.say("user", "last question")
			f.stopHook()
			f.say("assistant", "late answer")
			f.settle()
			before := string(f.rawBytes())
			if tc.apply == nil {
				// the same recording left active is caught up, so a zero below
				// is the condition's doing, not the fixture's
				assert.Equal(t, 1, f.catchUp())
				assert.Contains(t, string(f.rawBytes()), "late answer")
				return
			}
			tc.apply(t, f)

			assert.Equal(t, 0, f.catchUp())
			assert.Equal(t, before, string(f.rawBytes()), "the catch-up added to a finished recording")
		})
	}
}

// Failure prevented: a stop that lands while Claude Code is still writing its
// final record reads only up to the record before it, and the stop's drain is
// the last read, so the record is lost. It must wait briefly for the record.
func TestStopsFinalDrainWaitsForTheRecordBeingWritten(t *testing.T) {
	if testing.Short() {
		t.Skip("the writer finishes its record 300 ms into the stop's wait")
	}
	f := newCatchUpFixture(t)
	f.say("user", "last question")
	f.stopHook()
	head, tail := f.partialAnswer("the final answer")
	f.appendSource(head)
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(300 * time.Millisecond)
		f.appendSource(tail)
	}()

	f.stop()
	<-done

	assert.Contains(t, string(f.rawBytes()), "the final answer", "the stop must wait for the record being written")
}

// The wait is bounded: a record that never completes (the agent died
// mid-write) must not hold the stop open.
func TestStopsFinalDrainGivesUpOnARecordThatNeverCompletes(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the stop's full final-record wait (finalRecordWait)")
	}
	f := newCatchUpFixture(t)
	f.say("user", "last question")
	f.stopHook()
	head, _ := f.partialAnswer("an answer cut off")
	f.appendSource(head)

	start := time.Now()
	f.stop()

	assert.Less(t, time.Since(start), 5*time.Second, "the stop must not wait unbounded for a record that never completes")
	assert.NotContains(t, string(f.rawBytes()), "an answer cut off")
}
