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

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// claudeTranscript writes a transcript where Claude Code keeps one, named by
// its native session id, opening with a record of that session in cwd.
func (f *catchUpFixture) claudeTranscript(project, id, cwd string) string {
	f.t.Helper()
	path := filepath.Join(f.home, ".claude", "projects", project, id+".jsonl")
	require.NoError(f.t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(f.t, os.WriteFile(path, fmt.Appendf(nil, "{\"type\":\"session\",\"sessionId\":%q,\"cwd\":%q}\n", id, cwd), 0o600))
	return path
}

// appendTurn appends an assistant turn of native session id to transcript.
func (f *catchUpFixture) appendTurn(transcript, id, text string) {
	f.t.Helper()
	f.turns++
	rec, err := json.Marshal(map[string]string{
		"type": "assistant", "role": "assistant", "content": text, "sessionId": id, "cwd": f.projectRoot,
		"timestamp": f.state.StartedAt.Add(time.Duration(f.turns) * time.Second).Format(time.RFC3339Nano),
	})
	require.NoError(f.t, err)
	file, err := os.OpenFile(transcript, os.O_WRONLY|os.O_APPEND, 0o600)
	require.NoError(f.t, err)
	_, err = file.Write(append(rec, '\n'))
	require.NoError(f.t, err)
	require.NoError(f.t, file.Close())
}

// startUnbound starts another Claude Code recording in f's project whose
// marker already names its transcript and that no hook has bound: one active
// when the upgrade landed (an older ox's hook discovered its transcript), or
// one started by hand with `ox session start`, which passes the transcript in.
func (f *catchUpFixture) startUnbound(agentID string, byHand bool) (st *session.RecordingState, transcript, nativeID string) {
	f.t.Helper()
	t := f.t
	nativeID = "native-" + strings.ToLower(agentID)
	transcript = f.claudeTranscript(agentID, nativeID, f.projectRoot)
	opts := session.StartRecordingOptions{
		AgentID: agentID, AdapterName: "claude-code", Username: "person-a", WatchMode: "hook", WorkspacePath: f.projectRoot,
	}
	if byHand {
		opts.SessionFile, opts.AgentSessionID = transcript, nativeID
	}
	st, err := session.StartRecording(f.projectRoot, opts)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(st.SessionPath, artifactRaw), []byte(`{"type":"header","metadata":{}}`+"\n"), 0o600))
	info, err := os.Stat(transcript)
	require.NoError(t, err)
	require.NoError(t, session.UpdateRecordingStateForAgent(f.projectRoot, agentID, func(s *session.RecordingState) {
		s.SessionFile = transcript
		s.SourceOffset = info.Size() // its hooks have read everything so far
	}))
	return st, transcript, nativeID
}

// captureAs runs st's hook capture under its raw.jsonl lock, as its hook does.
func (f *catchUpFixture) captureAs(st *session.RecordingState, opts session.HookCaptureOptions) (session.HookCapture, error) {
	f.t.Helper()
	var capture session.HookCapture
	err := fileutil.WithFileLock(context.Background(), filepath.Join(st.SessionPath, artifactRaw), func() error {
		var err error
		capture, err = session.CaptureHookEntries(context.Background(), f.projectRoot, st.AgentID, st.SessionPath, st.SessionID, opts)
		return err
	})
	return capture, err
}

func rawOf(t *testing.T, st *session.RecordingState) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(st.SessionPath, artifactRaw))
	require.NoError(t, err)
	return string(b)
}

// Failure prevented: the binding was made only when a hook discovered the
// transcript, and these recordings never need discovering. One active before
// the upgrade, or started by hand with its transcript passed in, was never
// bound, so the catch-up skipped it for good and answers written after its
// last Stop hook never reached raw.jsonl or the live stream. Every hook's
// stdin names the transcript, so the next hook binds it.
func TestHookCatchUpBindsARecordingWhoseTranscriptNeedsNoDiscovery(t *testing.T) {
	for name, byHand := range map[string]bool{"active before the upgrade": false, "started by hand": true} {
		t.Run(name, func(t *testing.T) {
			f := newCatchUpFixture(t)
			st, transcript, nativeID := f.startUnbound("OxLate", byHand)
			m := f.watcher(slog.Default())
			m.NoteHook(context.Background(), f.projectRoot, "OxLate")
			f.appendTurn(transcript, nativeID, "an answer before its next hook")
			settle(t, transcript)
			assert.Equal(t, 0, m.CatchUpHookRecordings(context.Background(), f.projectRoot), "a transcript no hook named was caught up")

			_, err := f.captureAs(st, hookStdin(transcript, nativeID)) // its next hook
			require.NoError(t, err)
			f.appendTurn(transcript, nativeID, "the late answer")
			settle(t, transcript)

			assert.Equal(t, 1, m.CatchUpHookRecordings(context.Background(), f.projectRoot), "the next hook did not bind the transcript")
			assert.Equal(t, 1, strings.Count(rawOf(t, st), "the late answer"))
		})
	}
}

// Failure prevented: a registry write that failed once (a lock timeout, a
// full disk) was never tried again, and the recording was never caught up.
// The failure must not cost the hook its own capture, and the next hook binds.
func TestHookCatchUpRetriesAFailedBindAtTheNextHook(t *testing.T) {
	skipAsRoot(t)
	f := newCatchUpFixture(t)
	st, transcript, nativeID := f.startUnbound("OxRetry", false)
	m := f.watcher(slog.Default())
	m.NoteHook(context.Background(), f.projectRoot, "OxRetry")
	registry := filepath.Join(paths.DataDir(), "started-recordings.json")
	require.NoError(t, os.Chmod(registry, 0))
	t.Cleanup(func() { _ = os.Chmod(registry, 0o600) })

	f.appendTurn(transcript, nativeID, "an answer during the outage")
	_, err := f.captureAs(st, hookStdin(transcript, nativeID))
	require.NoError(t, err, "a failed bind failed the hook's capture")
	assert.Contains(t, rawOf(t, st), "an answer during the outage", "a failed bind cost the hook its capture")

	require.NoError(t, os.Chmod(registry, 0o600))
	_, err = f.captureAs(st, hookStdin(transcript, nativeID)) // the next hook
	require.NoError(t, err)
	f.appendTurn(transcript, nativeID, "the late answer")
	settle(t, transcript)

	assert.Equal(t, 1, m.CatchUpHookRecordings(context.Background(), f.projectRoot), "the next hook did not retry the bind")
	assert.Equal(t, 1, strings.Count(rawOf(t, st), "the late answer"))
}

// Failure prevented: a hook's rediscovery rebound the recording to whatever
// its marker led to. A writer of the marker sets session_file to a missing
// path and agent_session_id to another conversation in this repository; the
// next hook rediscovers that conversation and the catch-up then imported it.
// Marker fields never move the binding: only a hook's stdin does.
func TestHookCatchUpBindingMovesOnlyWithTheHooksStdin(t *testing.T) {
	f := newCatchUpFixture(t)
	other := f.claudeTranscript("other", "other", f.projectRoot)
	f.appendTurn(other, "other", "another conversation's turn")
	f.rewriteMarker(map[string]any{
		"session_file": filepath.Join(f.home, ".claude", "projects", "repo", "gone.jsonl"), "agent_session_id": "other",
	})
	// the hook's rediscovery finds what that agent_session_id names; its stdin
	// still names the agent's own transcript
	f.adapter.found = other
	_, err := f.capture(hookStdin(f.source, "session"))
	require.NoError(t, err)
	f.adapter.found = ""
	f.adapter.reads.Store(0)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	m := f.watcher(logger)
	f.appendTurn(other, "other", "a late turn of the other conversation")
	settle(t, other)

	for range 2 {
		assert.Equal(t, 0, m.CatchUpHookRecordings(context.Background(), f.projectRoot))
	}
	assert.Zero(t, f.adapter.reads.Load(), "the catch-up read the transcript the marker led to")
	assert.NotContains(t, f.raw(), "a late turn of the other conversation")
	assert.Equal(t, 1, strings.Count(logs.String(), "level=WARN"), "want one warning:\n%s", logs.String())
	assert.Contains(t, logs.String(), "not the one a local hook found")

	// a hook whose stdin names that conversation does move it
	_, err = f.capture(hookStdin(other, "other"))
	require.NoError(t, err)
	f.appendTurn(other, "other", "a turn after stdin named it")
	settle(t, other)
	assert.Equal(t, 1, m.CatchUpHookRecordings(context.Background(), f.projectRoot))
	assert.Equal(t, 1, strings.Count(f.raw(), "a turn after stdin named it"))
}

// Stdin is bound only once Claude Code's own checks pass: a transcript inside
// its root whose turns ran in another repository is not this recording's.
func TestHookCatchUpNeverBindsATranscriptOfAnotherRepository(t *testing.T) {
	f := newCatchUpFixture(t)
	foreign := f.claudeTranscript("elsewhere", "elsewhere", t.TempDir())
	_, err := f.capture(hookStdin(foreign, "elsewhere"))
	require.NoError(t, err)
	m := f.watcher(slog.Default())
	f.say("assistant", "the late answer")
	f.settle()

	assert.Equal(t, 1, m.CatchUpHookRecordings(context.Background(), f.projectRoot), "another repository's transcript replaced the binding")
	assert.Equal(t, 1, strings.Count(f.raw(), "the late answer"))
}
