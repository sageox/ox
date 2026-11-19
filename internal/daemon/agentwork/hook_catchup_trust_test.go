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
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// victimFile is an unrelated file the coworker can write, which a planted
// raw.jsonl symlink names. Its bytes must survive every capture.
func victimFile(t *testing.T) (path string, content []byte) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "notes.txt")
	content = []byte("the coworker's own notes, not session data\n")
	require.NoError(t, os.WriteFile(path, content, 0o600))
	return path, content
}

// plantRollback makes dir/raw.jsonl a symlink to target and leaves the batch
// journal a rollback at cursor reads as uncommitted: recovery would truncate
// raw.jsonl to raw_size 0, through the link.
func plantRollback(t *testing.T, dir, target string, cursor int64) (journal string, journalBytes []byte) {
	t.Helper()
	raw := filepath.Join(dir, artifactRaw)
	require.NoError(t, os.RemoveAll(raw))
	require.NoError(t, os.Symlink(target, raw))
	journal = raw + ".append.json"
	journalBytes = fmt.Appendf(nil, `{"raw_size":0,"old_offset":%d,"new_offset":%d}`, cursor, cursor+1)
	require.NoError(t, os.WriteFile(journal, journalBytes, 0o600))
	return journal, journalBytes
}

// assertUntouched asserts the victim and the planted journal kept their bytes:
// nothing was recovered, truncated or appended through the link.
func assertUntouched(t *testing.T, victim string, want []byte, journal string, journalWant []byte) {
	t.Helper()
	got, err := os.ReadFile(victim)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got), "the file the raw.jsonl symlink names was modified")
	j, err := os.ReadFile(journal)
	require.NoError(t, err, "the planted journal was consumed by a recovery")
	assert.Equal(t, string(journalWant), string(j))
}

// A repository writer can plant a recording marker in the legacy sessions/
// folder of the checkout, naming a Claude Code transcript of another
// workspace. With a raw.jsonl symlinked at another file the coworker can
// write, and a rollback journal that says the last batch never committed,
// recovery would truncate that file; with a plain raw.jsonl, the catch-up
// would import the other workspace's transcript. No local hook ever reports a
// planted marker, so the catch-up must not read, recover or write anything
// for it, while the coworker's real recording, whose hooks ran, is still
// caught up.
func TestHookCatchUpNeverTouchesAMarkerNoLocalHookReported(t *testing.T) {
	plants := map[string]func(t *testing.T, dir string) (check func(t *testing.T)){
		"raw.jsonl symlinked, with a rollback journal": func(t *testing.T, dir string) func(t *testing.T) {
			victim, victimBytes := victimFile(t)
			journal, journalBytes := plantRollback(t, dir, victim, 0)
			return func(t *testing.T) { assertUntouched(t, victim, victimBytes, journal, journalBytes) }
		},
		"plain raw.jsonl": func(t *testing.T, dir string) func(t *testing.T) {
			raw := filepath.Join(dir, artifactRaw)
			header := []byte(`{"type":"header","metadata":{}}` + "\n")
			require.NoError(t, os.WriteFile(raw, header, 0o600))
			return func(t *testing.T) {
				got, err := os.ReadFile(raw)
				require.NoError(t, err)
				assert.Equal(t, string(header), string(got), "the planted recording was appended to")
			}
		},
	}
	for name, plant := range plants {
		t.Run(name, func(t *testing.T) {
			f := newCatchUpFixture(t)
			m := f.watcher(slog.Default())

			plantedDir := filepath.Join(f.projectRoot, "sessions", "2026-11-19T10-00-planted")
			require.NoError(t, os.MkdirAll(plantedDir, 0o755))
			plantedSource := filepath.Join(f.home, ".claude", "projects", "other", "planted.jsonl")
			require.NoError(t, os.MkdirAll(filepath.Dir(plantedSource), 0o700))
			require.NoError(t, os.WriteFile(plantedSource, fmt.Appendf(nil,
				"{\"type\":\"assistant\",\"role\":\"assistant\",\"content\":\"another workspace's turn\",\"sessionId\":\"planted\",\"cwd\":%q,\"timestamp\":%q}\n",
				f.projectRoot, time.Now().Add(-time.Hour).Format(time.RFC3339Nano)), 0o600))
			settle(t, plantedSource)
			marker, err := json.Marshal(map[string]any{
				"agent_id": "OxPlant", "session_id": "ses_planted", "adapter_name": "claude-code", "watch_mode": "hook",
				"session_path": plantedDir, "workspace_path": f.projectRoot, "session_file": plantedSource,
			})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(plantedDir, ".recording.json"), marker, 0o600))
			untouched := plant(t, plantedDir)

			f.say("assistant", "the coworker's late answer")
			f.settle()

			assert.Equal(t, 1, m.CatchUpHookRecordings(context.Background(), f.projectRoot), "only the recording a hook reported is caught up")
			assert.Equal(t, 0, m.CatchUpHookRecordings(context.Background(), f.projectRoot))

			untouched(t)
			assert.Equal(t, int64(1), f.adapter.reads.Load(), "the planted transcript was read")
			assert.Equal(t, 1, strings.Count(f.raw(), "the coworker's late answer"))
			assert.NotContains(t, f.raw(), "another workspace's turn")
			planted, err := os.ReadFile(filepath.Join(plantedDir, ".recording.json"))
			require.NoError(t, err)
			assert.JSONEq(t, string(marker), string(planted), "the planted marker was written to")
		})
	}
}

// The set of recordings hooks reported lives in the daemon's memory. After a
// daemon restart nothing is armed: a recording is caught up again once its
// next hook arrives, never before.
func TestHookCatchUpRearmsAtTheNextHookAfterARestart(t *testing.T) {
	f := newCatchUpFixture(t)
	restarted := NewSessionWatcherManager(slog.Default())
	f.say("assistant", "answer after the last hook")
	f.settle()

	assert.Equal(t, 0, restarted.CatchUpHookRecordings(context.Background(), f.projectRoot), "a recording no hook reported since the restart was caught up")
	assert.Zero(t, f.adapter.reads.Load())
	assert.NotContains(t, f.raw(), "answer after the last hook")

	restarted.NoteHook(context.Background(), f.projectRoot, f.agentID) // the next hook's heartbeat
	assert.Equal(t, 1, restarted.CatchUpHookRecordings(context.Background(), f.projectRoot))
	assert.Equal(t, 1, strings.Count(f.raw(), "answer after the last hook"))
}

// countingCursorAdapter is the counting reader under Cursor's name.
type countingCursorAdapter struct{ countingClaudeAdapter }

func (*countingCursorAdapter) Name() string { return "cursor" }

func installCountingCursorAdapter(t *testing.T) *countingCursorAdapter {
	t.Helper()
	prior, priorErr := adapters.GetAdapter("cursor")
	if priorErr == nil {
		adapters.Unregister("cursor")
	}
	a := &countingCursorAdapter{}
	adapters.Register(a)
	t.Cleanup(func() {
		adapters.Unregister("cursor")
		if priorErr == nil {
			adapters.Register(prior)
		}
	})
	return a
}

// Cursor has its own drain (session.DrainCursorSource), which applies Cursor's
// conversation and worktree checks. The catch-up runs only Claude Code's, so
// even a Cursor recording whose hooks ran (they heartbeat too) is never
// caught up: its transcript is neither read nor appended.
func TestHookCatchUpLeavesCursorRecordingsToTheirOwnDrain(t *testing.T) {
	f := newCatchUpFixture(t)
	cursor := installCountingCursorAdapter(t)
	source := filepath.Join(f.home, ".cursor", "projects", "workspace", "agent-transcripts", "conversation.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(source), 0o700))
	require.NoError(t, os.WriteFile(source, nil, 0o600))
	require.NoError(t, session.UpdateRecordingStateForAgent(f.projectRoot, f.agentID, func(s *session.RecordingState) {
		s.AdapterName = "cursor"
		s.SessionFile = source
		s.SourceOffset, s.StartOffset = 0, 0
	}))
	f.source = source
	m := f.watcher(slog.Default())
	f.say("assistant", "a Cursor answer")
	f.settle()
	before := f.raw()

	assert.Equal(t, 0, m.CatchUpHookRecordings(context.Background(), f.projectRoot))
	assert.Zero(t, cursor.reads.Load(), "the catch-up read a Cursor transcript")
	assert.Equal(t, before, f.raw())
	assert.Zero(t, f.recording().SourceOffset, "the catch-up moved a Cursor recording's cursor")
}

// sayFrom appends a turn Claude Code recorded in another working directory.
func (f *catchUpFixture) sayFrom(cwd, role, text string) {
	f.t.Helper()
	f.turns++
	rec, err := json.Marshal(map[string]string{
		"type": role, "role": role, "content": text, "sessionId": "session", "cwd": cwd,
		"timestamp": f.state.StartedAt.Add(time.Duration(f.turns) * time.Second).Format(time.RFC3339Nano),
	})
	require.NoError(f.t, err)
	file, err := os.OpenFile(f.source, os.O_WRONLY|os.O_APPEND, 0o600)
	require.NoError(f.t, err)
	_, err = file.Write(append(rec, '\n'))
	require.NoError(f.t, err)
	require.NoError(f.t, file.Close())
}

// spellAdapter rewrites the marker's adapter_name as an older ox wrote it.
func (f *catchUpFixture) spellAdapter(name string) {
	f.t.Helper()
	path := filepath.Join(f.state.SessionPath, ".recording.json")
	data, err := os.ReadFile(path)
	require.NoError(f.t, err)
	var marker map[string]any
	require.NoError(f.t, json.Unmarshal(data, &marker))
	marker["adapter_name"] = name
	data, err = json.Marshal(marker)
	require.NoError(f.t, err)
	require.NoError(f.t, os.WriteFile(path, data, 0o600))
}

// `ox agent prime` once recorded Claude Code as "claude", an alias the adapter
// registry accepts. A recording spelled that way is Claude Code and gets
// Claude Code's ownership check: a turn from another repository is never
// appended, and the recording is quarantined for review.
func TestHookCatchUpChecksTheClaudeAliasAsClaudeCode(t *testing.T) {
	f := newCatchUpFixture(t)
	f.spellAdapter("claude")
	m := f.watcher(slog.Default())
	f.sayFrom(t.TempDir(), "assistant", "a turn from another repository")
	f.settle()

	assert.Equal(t, 0, m.CatchUpHookRecordings(context.Background(), f.projectRoot))
	assert.NotContains(t, f.raw(), "a turn from another repository", "an alias skipped Claude Code's ownership check")
	quarantined, err := session.LoadQuarantinedRecordingsForAgent(f.projectRoot, f.agentID)
	require.NoError(t, err)
	assert.Len(t, quarantined, 1, "the foreign turn did not quarantine the recording")
}

// Even a recording a hook reported may have a raw.jsonl someone replaced with
// a symlink. The capture refuses it before recovery: neither the file the link
// names nor the journal is touched. The catch-up warns once, not every sweep.
func TestHookCaptureRefusesASymlinkedRawFileBeforeRecovery(t *testing.T) {
	t.Run("hook", func(t *testing.T) {
		f := newCatchUpFixture(t)
		victim, victimBytes := victimFile(t)
		journal, journalBytes := plantRollback(t, f.state.SessionPath, victim, f.recording().SourceOffset)
		f.say("assistant", "an answer")
		headerWrites := 0

		capture, err := f.capture(session.HookCaptureOptions{WriteHeader: func(*session.RecordingState) error {
			headerWrites++
			return nil
		}})

		require.ErrorIs(t, err, session.ErrRawNotRegular)
		assert.Equal(t, session.HookStatusRawRefused, capture.Status)
		assert.True(t, capture.Failed())
		assertUntouched(t, victim, victimBytes, journal, journalBytes)
		assert.Zero(t, headerWrites)
		assert.Zero(t, f.adapter.reads.Load(), "the transcript was read for a refused raw.jsonl")
	})

	t.Run("catch-up", func(t *testing.T) {
		f := newCatchUpFixture(t)
		// the daemon's log carries both the watcher's lines and the capture's
		// (slog's default), so a warning per sweep from either one shows here
		var logs bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logs, nil))
		prev := slog.Default()
		slog.SetDefault(logger)
		t.Cleanup(func() { slog.SetDefault(prev) })
		m := f.watcher(logger)
		victim, victimBytes := victimFile(t)
		journal, journalBytes := plantRollback(t, f.state.SessionPath, victim, f.recording().SourceOffset)
		f.say("assistant", "an answer")
		f.settle()

		for range 2 {
			assert.Equal(t, 0, m.CatchUpHookRecordings(context.Background(), f.projectRoot))
		}
		assertUntouched(t, victim, victimBytes, journal, journalBytes)
		assert.Zero(t, f.adapter.reads.Load())
		assert.Equal(t, 1, strings.Count(logs.String(), "level=WARN"), "want one warning per recording, not one per sweep")
		assert.Contains(t, logs.String(), "session_capture_catchup_refused")
	})
}
