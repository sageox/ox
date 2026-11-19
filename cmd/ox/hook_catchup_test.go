package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
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
type tailingClaudeAdapter struct{ testClaudeCodeAdapter }

func (a *tailingClaudeAdapter) ReadFromOffset(path string, offset int64) ([]adapters.RawEntry, int64, error) {
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
	require.NoError(t, session.UpdateRecordingStateForAgent(f.projectRoot, f.agentID, func(s *session.RecordingState) {
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

// A hook and the daemon's catch-up capturing one transcript at the same time
// must leave every line in raw.jsonl, and in the live spool, exactly once.
func TestHookAndCatchUpRacingCaptureEveryLineOnce(t *testing.T) {
	f := newCatchUpFixture(t)
	m := f.catchUpManager()
	hook := &HookContext{Phase: phaseAfterTool, ProjectRoot: f.projectRoot, Marker: &SessionMarker{AgentID: f.agentID}}

	const lines = 60
	ctx, cancel := context.WithCancel(context.Background())
	var caughtUp atomic.Int64
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			assert.NoError(t, handleAfterTool(hook))
			time.Sleep(time.Millisecond)
		}
	}()
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			caughtUp.Add(int64(m.CatchUpHookRecordings(ctx, f.projectRoot)))
			time.Sleep(500 * time.Microsecond)
		}
	}()
	for i := range lines {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		f.say(role, fmt.Sprintf("line-%03d", i))
		f.settle() // settled at once, so the catch-up competes for every write
		time.Sleep(3 * time.Millisecond)
	}
	cancel()
	wg.Wait()
	require.NoError(t, handleAfterTool(hook)) // whatever neither took yet

	raw := string(f.rawBytes())
	for i := range lines {
		assert.Equal(t, 1, strings.Count(raw, fmt.Sprintf("line-%03d", i)), "line-%03d must be in raw.jsonl exactly once", i)
	}
	assert.Equal(t, raw, string(f.spoolBytes()), "the live spool must hold exactly what raw.jsonl holds")
	assert.Positive(t, caughtUp.Load(), "the catch-up never won a capture, so the race was not exercised")
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
