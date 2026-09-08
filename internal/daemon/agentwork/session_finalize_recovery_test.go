package agentwork

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A watcher in another daemon may still flush after local cleanup. Both
// detectors must wait for its raw lock, then reload the final capture cursor.
func TestNativeRecovery_WaitsForCaptureLock(t *testing.T) {
	for _, detector := range []string{"anti-entropy", "agent-exit"} {
		t.Run(detector, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
			ledgerPath := t.TempDir()
			sessionDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", "recovery-OxLocked")
			require.NoError(t, os.MkdirAll(sessionDir, 0700))
			sourceDir := filepath.Join(home, ".codex", "sessions")
			require.NoError(t, os.MkdirAll(sourceDir, 0700))
			source := filepath.Join(sourceDir, "native.jsonl")
			const first = "first response\n"
			const second = "watcher flush\n"
			const last = "final unread response\n"
			require.NoError(t, os.WriteFile(source, []byte(first+second+last), 0600))
			state := session.RecordingState{
				AgentID: "OxLocked", AdapterName: "codex", WatchMode: "tail",
				SessionFile: source, SessionPath: sessionDir, ParentPID: 99999999,
				StartedAt: time.Now().Add(-time.Hour), SourceOffset: int64(len(first)), EntryCount: 1,
			}
			recPath := filepath.Join(sessionDir, recordingMarker)
			writeRecordingState(t, recPath, state)
			rawPath := filepath.Join(sessionDir, artifactRaw)
			const raw = "{\"_meta\":{\"agent_type\":\"codex\"}}\n{\"type\":\"assistant\",\"content\":\"first response\"}\n"
			require.NoError(t, os.WriteFile(rawPath, []byte(raw), 0600))

			locked, flush, flushed := make(chan struct{}), make(chan struct{}), make(chan struct{})
			release, holderDone := make(chan struct{}), make(chan struct{})
			releaseLock := sync.OnceFunc(func() { close(release) })
			var holderErr error
			go func() {
				defer close(holderDone)
				holderErr = fileutil.WithFileLock(context.Background(), rawPath, func() error {
					close(locked)
					select {
					case <-flush:
					case <-release:
						return nil
					}
					rw, err := session.NewRawWriter(rawPath, "")
					if err != nil {
						return err
					}
					defer rw.Close()
					if err := rw.WriteEntry(&session.Entry{Type: session.EntryTypeAssistant, Content: "watcher flush"}); err != nil {
						return err
					}
					if err := rw.CloseAndSync(); err != nil {
						return err
					}
					next := state
					next.SourceOffset += int64(len(second))
					next.EntryCount++
					data, err := json.Marshal(next)
					if err != nil {
						return err
					}
					if err := os.WriteFile(recPath, data, 0600); err != nil {
						return err
					}
					close(flushed)
					<-release
					return nil
				})
			}()
			t.Cleanup(func() {
				releaseLock()
				<-holderDone
			})
			select {
			case <-locked:
			case <-holderDone:
				require.NoError(t, holderErr)
				t.Fatal("capture lock was not acquired")
			}

			handler := NewSessionFinalizeHandlerForTest(nil)
			started, done := make(chan struct{}), make(chan struct{})
			var items []*WorkItem
			var detectErr error
			go func() {
				defer close(done)
				close(started)
				if detector == "anti-entropy" {
					items, detectErr = handler.Detect(ledgerPath)
				} else {
					items = handler.DetectOrphanedForAgent(ledgerPath, state.AgentID, state.ParentPID)
				}
			}()
			t.Cleanup(func() {
				releaseLock()
				<-done
			})
			<-started
			// This deadline asserts the blocking contract; channel handshakes
			// establish lock ownership and control the writer's final flush.
			select {
			case <-done:
				t.Fatal("finalization completed while another capture writer held the raw lock")
			case <-time.After(100 * time.Millisecond):
			}
			require.FileExists(t, recPath, "a pending writer still owns the recording marker")
			beforeFlush, err := os.ReadFile(rawPath)
			require.NoError(t, err)
			assert.Equal(t, raw, string(beforeFlush), "recovery must not replace a writer's captured prefix")

			close(flush)
			select {
			case <-flushed:
			case <-holderDone:
				require.NoError(t, holderErr)
				t.Fatal("capture writer did not flush")
			}
			require.FileExists(t, recPath)
			assert.Equal(t, 2, countRawJSONLEntries(t, rawPath))
			releaseLock()
			<-holderDone
			require.NoError(t, holderErr)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("finalization did not resume after capture released the raw lock")
			}
			require.NoError(t, detectErr)
			require.Len(t, items, 1)
			stored, err := session.ReadSessionFromPath(rawPath)
			require.NoError(t, err)
			require.Len(t, stored.Entries, 3, "recovery must reload the flushed cursor and capture each entry once")
			assert.Equal(t, "first response", stored.Entries[0]["content"])
			assert.Equal(t, "watcher flush", stored.Entries[1]["content"])
			assert.Equal(t, "final unread response", stored.Entries[2]["content"])
			assert.NoFileExists(t, recPath)
		})
	}
}

// A missed watcher poll must recover before cleanup or either finalization detector
// drops the recording marker, including the header eagerly written by prime.
func TestNativeRecovery_ReachesFinalization(t *testing.T) {
	for _, detector := range []string{"anti-entropy", "agent-exit"} {
		for _, rawState := range []string{"missing", "header", "partial", "caught-up"} {
			t.Run(detector+"/"+rawState, func(t *testing.T) {
				home := t.TempDir()
				t.Setenv("HOME", home)
				t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
				ledgerPath := t.TempDir()
				sessionDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", "recovery-OxTail")
				require.NoError(t, os.MkdirAll(sessionDir, 0700))
				sourceDir := filepath.Join(home, ".codex", "sessions")
				require.NoError(t, os.MkdirAll(sourceDir, 0700))
				source := filepath.Join(sourceDir, "native.jsonl")
				const first = "first captured response\n"
				const last = "last response before process exit\n"
				require.NoError(t, os.WriteFile(source, []byte(first+last), 0600))
				state := session.RecordingState{
					AgentID: "OxTail", SessionID: "ses_01890a5d-ac96-774b-bcce-b302099a8057",
					AdapterName: "codex", WatchMode: "tail", SessionFile: source,
					SessionPath: sessionDir, ParentPID: 99999999,
					StartedAt: time.Now().Add(-72 * time.Hour),
				}
				header := `{"_meta":{"schema_version":"1","agent_id":"OxTail","agent_type":"codex","session_id":"` + state.SessionID + `","username":"coworker","model":"gpt-test"}}` + "\n"
				raw := header
				if rawState == "partial" || rawState == "caught-up" {
					raw += `{"type":"assistant","content":"first captured response","seq":1}` + "\n"
					state.SourceOffset = int64(len(first))
					state.EntryCount = 1
				}
				if rawState == "caught-up" {
					raw += `{"type":"assistant","content":"last response before process exit","seq":2}` + "\n"
					state.SourceOffset += int64(len(last))
					state.EntryCount++
				}
				rawPath := filepath.Join(sessionDir, artifactRaw)
				if rawState != "missing" {
					require.NoError(t, os.WriteFile(rawPath, []byte(raw), 0600))
				}
				recPath := filepath.Join(sessionDir, recordingMarker)
				writeRecordingState(t, recPath, state)
				require.NoError(t, os.Chtimes(sessionDir, state.StartedAt, state.StartedAt))

				handler := NewSessionFinalizeHandlerForTest(nil)
				handler.Cleanup(ledgerPath)
				require.FileExists(t, recPath, "cleanup must leave recoverable native sessions for the detector")
				var items []*WorkItem
				if detector == "anti-entropy" {
					var err error
					items, err = handler.Detect(ledgerPath)
					require.NoError(t, err)
				} else {
					items = handler.DetectOrphanedForAgent(ledgerPath, state.AgentID, state.ParentPID)
				}
				require.Len(t, items, 1)
				stored, err := session.ReadSessionFromPath(rawPath)
				require.NoError(t, err)
				require.Len(t, stored.Entries, 2, "capture and recovery must include each native entry once")
				assert.Equal(t, "first captured response", stored.Entries[0]["content"])
				assert.Equal(t, "last response before process exit", stored.Entries[1]["content"])
				assert.Equal(t, state.SessionID, stored.Meta.SessionID)
				assert.NoFileExists(t, recPath)
				if rawState != "missing" {
					assert.Equal(t, "coworker", stored.Meta.Username, "existing metadata must survive recovery")
					assert.Equal(t, "gpt-test", stored.Meta.Model)
					if rawState == "partial" || rawState == "caught-up" {
						assert.Equal(t, map[string]any{"type": "assistant", "content": "first captured response", "seq": float64(1)}, stored.Entries[0], "all fields of the captured prefix must survive")
					}
				}
				items, err = handler.Detect(ledgerPath)
				require.NoError(t, err)
				require.Len(t, items, 1)
				assert.Equal(t, 2, countRawJSONLEntries(t, rawPath), "a later detect must not append duplicates")
			})
		}
	}
}

// An unreadable native source must retain the captured prefix and its recovery
// cursor instead of uploading an incomplete session and destroying the retry state.
func TestNativeRecovery_SourceFailurePreservesRecording(t *testing.T) {
	for _, detector := range []string{"anti-entropy", "agent-exit"} {
		for _, sourceState := range []string{"missing", "outside-root", "deferred-discovery"} {
			t.Run(detector+"/"+sourceState, func(t *testing.T) {
				home := t.TempDir()
				t.Setenv("HOME", home)
				t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
				ledgerPath := t.TempDir()
				sessionDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", "recovery-OxRetry")
				require.NoError(t, os.MkdirAll(sessionDir, 0700))
				sourceDir := filepath.Join(home, ".codex", "sessions")
				require.NoError(t, os.MkdirAll(sourceDir, 0700))
				source := filepath.Join(sourceDir, "missing.jsonl")
				switch sourceState {
				case "outside-root":
					source = filepath.Join(home, "private.jsonl")
					require.NoError(t, os.WriteFile(source, []byte("private data\n"), 0600))
				case "deferred-discovery":
					source = ""
				}
				state := session.RecordingState{
					AgentID: "OxRetry", AdapterName: "codex", WatchMode: "tail",
					SessionFile: source, SessionPath: sessionDir, ParentPID: 99999999,
					StartedAt: time.Now().Add(-time.Hour), SourceOffset: 12, EntryCount: 1,
				}
				recPath := filepath.Join(sessionDir, recordingMarker)
				writeRecordingState(t, recPath, state)
				recBefore, err := os.ReadFile(recPath)
				require.NoError(t, err)
				rawPath := filepath.Join(sessionDir, artifactRaw)
				const raw = "{\"_meta\":{\"agent_type\":\"codex\"}}\n{\"type\":\"assistant\",\"content\":\"captured\"}\n"
				require.NoError(t, os.WriteFile(rawPath, []byte(raw), 0600))
				handler := NewSessionFinalizeHandlerForTest(nil)
				var items []*WorkItem
				if detector == "anti-entropy" {
					items, err = handler.Detect(ledgerPath)
					require.NoError(t, err)
				} else {
					items = handler.DetectOrphanedForAgent(ledgerPath, state.AgentID, state.ParentPID)
				}
				assert.Empty(t, items)
				recAfter, err := os.ReadFile(recPath)
				require.NoError(t, err)
				assert.Equal(t, recBefore, recAfter)
				rawAfter, err := os.ReadFile(rawPath)
				require.NoError(t, err)
				assert.Equal(t, raw, string(rawAfter))
			})
		}
	}
}

// Catch-up must compose with the same pause mask and credential redaction as
// normal session stop, including a final unread entry during an open pause.
func TestNativeRecovery_HonorsPauseAndRedaction(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(map[bool]string{false: "redaction", true: "pause"}[paused], func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
			ledgerPath := t.TempDir()
			sessionDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", "recovery-OxPrivacy")
			require.NoError(t, os.MkdirAll(sessionDir, 0700))
			sourceDir := filepath.Join(home, ".codex", "sessions")
			require.NoError(t, os.MkdirAll(sourceDir, 0700))
			source := filepath.Join(sourceDir, "native.jsonl")
			const public = "public response\n"
			const private = "paused content\n"
			const secret = "ghp_1234567890abcdefghijklmnopqrstuvwxyz1234"
			require.NoError(t, os.WriteFile(source, []byte(public+private+secret+"\n"), 0600))
			state := session.RecordingState{
				AgentID: "OxPrivacy", AdapterName: "codex", WatchMode: "tail",
				SessionFile: source, SessionPath: sessionDir, ParentPID: 99999999,
				StartedAt: time.Now().Add(-time.Hour), SourceOffset: int64(len(public + private)), EntryCount: 2,
			}
			if paused {
				at := time.Now().Add(-time.Minute)
				state.SuspendedAt = &at
				state.Lifecycle = []session.LifecycleEvent{{Action: session.LifecycleActionPause, At: at, Seq: 1}}
			}
			writeRecordingState(t, filepath.Join(sessionDir, recordingMarker), state)
			rawPath := filepath.Join(sessionDir, artifactRaw)
			raw := "{\"_meta\":{\"agent_type\":\"codex\"}}\n{\"type\":\"assistant\",\"content\":\"public response\"}\n{\"type\":\"assistant\",\"content\":\"paused content\"}\n"
			require.NoError(t, os.WriteFile(rawPath, []byte(raw), 0600))
			handler := NewSessionFinalizeHandlerForTest(nil)
			// Simulate a crash after the recovered raw was committed but before
			// the old recording marker was removed. Retrying cannot mask twice.
			recovered, err := recoverRawFromSessionFile(handler.logger, filepath.Join(sessionDir, recordingMarker), sessionDir, rawPath)
			require.NoError(t, err)
			require.True(t, recovered)
			beforeRetry, err := os.ReadFile(rawPath)
			require.NoError(t, err)
			items := handler.DetectOrphanedForAgent(ledgerPath, state.AgentID, state.ParentPID)
			require.Len(t, items, 1)
			data, err := os.ReadFile(rawPath)
			require.NoError(t, err)
			assert.Equal(t, beforeRetry, data, "retry must not import or mask the same entries twice")
			assert.NotContains(t, string(data), secret)
			if paused {
				assert.NotContains(t, string(data), "paused content")
				assert.Equal(t, 1, countRawJSONLEntries(t, rawPath))
			} else {
				assert.Contains(t, string(data), "REDACTED")
				assert.Equal(t, 3, countRawJSONLEntries(t, rawPath))
			}
		})
	}
}

// Stopped recordings already applied masks and selected the recording window;
// recovery must never import later native activity back into them.
func TestNativeRecovery_LeavesStoppedRecordingUnchanged(t *testing.T) {
	ledgerPath := t.TempDir()
	sessionDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", "stopped-OxStop")
	require.NoError(t, os.MkdirAll(sessionDir, 0700))
	stopped := time.Now()
	state := session.RecordingState{AgentID: "OxStop", AdapterName: "codex", WatchMode: "tail", StoppedAt: &stopped}
	writeRecordingState(t, filepath.Join(sessionDir, recordingMarker), state)
	const raw = "{\"_meta\":{\"agent_type\":\"codex\"}}\n{\"type\":\"assistant\",\"content\":\"captured\"}\n"
	rawPath := filepath.Join(sessionDir, artifactRaw)
	require.NoError(t, os.WriteFile(rawPath, []byte(raw), 0600))
	handler := NewSessionFinalizeHandlerForTest(nil)
	require.Len(t, handler.DetectOrphanedForAgent(ledgerPath, state.AgentID, 0), 1)
	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	assert.Equal(t, raw, string(data))
}

// A successful read proving the native log empty must still release the stub
// for cleanup; preserving recoverable recordings must not create immortals.
func TestNativeRecovery_EmptySourceCanBeCleaned(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	ledgerPath := t.TempDir()
	sessionDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", "empty-OxEmpty")
	require.NoError(t, os.MkdirAll(sessionDir, 0700))
	sourceDir := filepath.Join(home, ".codex", "sessions")
	require.NoError(t, os.MkdirAll(sourceDir, 0700))
	source := filepath.Join(sourceDir, "empty.jsonl")
	require.NoError(t, os.WriteFile(source, nil, 0600))
	state := session.RecordingState{AgentID: "OxEmpty", AdapterName: "codex", WatchMode: "tail", SessionFile: source, SessionPath: sessionDir, ParentPID: 99999999, StartedAt: time.Now().Add(-72 * time.Hour)}
	writeRecordingState(t, filepath.Join(sessionDir, recordingMarker), state)
	header, err := json.Marshal(map[string]any{"_meta": map[string]any{"agent_type": "codex"}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, artifactRaw), append(header, '\n'), 0600))
	handler := NewSessionFinalizeHandlerForTest(nil)
	items, err := handler.Detect(ledgerPath)
	require.NoError(t, err)
	assert.Empty(t, items)
	assert.NoFileExists(t, filepath.Join(sessionDir, recordingMarker))
	require.NoError(t, os.Chtimes(sessionDir, state.StartedAt, state.StartedAt))
	handler.Cleanup(ledgerPath)
	assert.NoDirExists(t, sessionDir)
}
