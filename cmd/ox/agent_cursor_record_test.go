package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorSessionRecordRejectsManualEntriesWithoutBlockingNativeCapture(t *testing.T) {
	if testing.Short() {
		t.Skip("short: exercises native capture, batch refusal, and finalization")
	}
	for _, captured := range []bool{false, true} {
		name := "before first native capture"
		if captured {
			name = "after native capture"
		}
		t.Run(name, func(t *testing.T) {
			f := newCursorHostFixture(t)
			first, second := cursorReviewTurn("FIRST"), cursorReviewTurn("SECOND")
			f.write(t, first)
			state := f.record(t)
			if captured {
				capture, err := session.DrainCursorSource(context.Background(), f.root, state.SessionPath, f.home, f.reader, false)
				require.NoError(t, err)
				state = capture.State
				require.Equal(t, 2, state.EntryCount)
			} else {
				require.Zero(t, state.EntryCount)
				require.Equal(t, state.StartOffset, state.SourceOffset)
			}
			rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
			rawBefore, err := os.ReadFile(rawPath)
			require.NoError(t, err)
			markerPath := filepath.Join(state.SessionPath, ".recording.json")
			markerBefore, err := os.ReadFile(markerPath)
			require.NoError(t, err)

			// The durable adapter identity governs eligibility even if the caller's
			// instance was detected differently.
			inst := &agentinstance.Instance{AgentID: state.AgentID, AgentType: "generic"}
			err = runAgentSessionRecord(inst, []string{"--entries", `[{"type":"user","content":"MANUAL_ENTRY"}]`})
			assert.ErrorContains(t, err, "unsupported-scope: session record cannot add entries to a native Cursor recording")
			rawAfter, err := os.ReadFile(rawPath)
			require.NoError(t, err)
			assert.Equal(t, string(rawBefore), string(rawAfter), "refusal must not append bytes to the native recording")
			markerAfter, err := os.ReadFile(markerPath)
			require.NoError(t, err)
			assert.Equal(t, string(markerBefore), string(markerAfter), "entry count and source checkpoint must remain aligned")

			// A batch before the first native capture used to leave acknowledged
			// entries without source progress, so the real watcher refused startup.
			mgr := agentwork.NewSessionWatcherManager(slog.New(slog.NewTextHandler(io.Discard, nil)))
			t.Cleanup(mgr.StopAll)
			assert.NoError(t, mgr.StartWatch(filepath.Base(state.SessionPath), f.source, "cursor", "", state.SessionPath))
			mgr.StopAll()

			f.write(t, first+second)
			err = finalizeCursorHostRecording(t, f, state)
			require.NoError(t, err, "a refused manual batch must not poison later native reconciliation")
			stored, err := session.ReadSessionFromPath(rawPath)
			require.NoError(t, err)
			require.Len(t, stored.Entries, 4)
			for _, entry := range stored.Entries {
				assert.NotContains(t, entry["content"], "MANUAL_ENTRY")
			}
			// Direct helper callers must not be able to bypass the CLI refusal.
			rawBefore, err = os.ReadFile(rawPath)
			require.NoError(t, err)
			recorded, err := recordEntriesToSession(f.root, state, []sessionRecordInput{{Type: "user", Content: "DIRECT_MANUAL_ENTRY"}})
			assert.ErrorContains(t, err, "unsupported-scope: session record cannot add entries to a native Cursor recording")
			assert.Zero(t, recorded)
			rawAfter, err = os.ReadFile(rawPath)
			require.NoError(t, err)
			assert.Equal(t, string(rawBefore), string(rawAfter))
		})
	}
}

func TestSessionRecordStillSupportsOtherAdapters(t *testing.T) {
	for _, adapter := range []string{"generic", "claude-code", "codex"} {
		t.Run(adapter, func(t *testing.T) {
			f := newCursorHostFixture(t)
			state, err := session.StartRecording(f.root, session.StartRecordingOptions{
				AgentID: "Oxbatch", AdapterName: adapter, AgentType: adapter, WorkspacePath: f.root,
			})
			require.NoError(t, err)
			require.NoError(t, writeRawHeader(f.root, state))
			inst := &agentinstance.Instance{AgentID: state.AgentID, AgentType: adapter}
			require.NoError(t, runAgentSessionRecord(inst, []string{"--entries", `[{"type":"user","content":"batch prompt"},{"type":"assistant","content":"batch response"}]`}))
			current, err := session.LoadRecordingStateForAgent(f.root, state.AgentID)
			require.NoError(t, err)
			require.Equal(t, 2, current.EntryCount)
			stored, err := session.ReadSessionFromPath(filepath.Join(state.SessionPath, "raw.jsonl"))
			require.NoError(t, err)
			require.Len(t, stored.Entries, 2)
			assert.Equal(t, "batch prompt", stored.Entries[0]["content"])
			assert.Equal(t, "batch response", stored.Entries[1]["content"])
		})
	}
}
