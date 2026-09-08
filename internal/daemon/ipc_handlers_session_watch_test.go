package daemon

import (
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	projectconfig "github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionWatchStart_CapturesInRecordingCache reproduces Conductor's IPC
// startup with the real Codex reader and StartRecording's directory layout.
// Previously the service opened ledger/sessions/raw.jsonl, either failing to
// record anything or writing conversation bytes into a published session folder.
func TestSessionWatchStart_CapturesInRecordingCache(t *testing.T) {
	if testing.Short() {
		t.Skip("short: builds Codex adapter and waits for session polling")
	}
	binaryPath := filepath.Join(t.TempDir(), "ox-adapter-codex")
	build := exec.Command("go", "build", "-o", binaryPath, "./cmd/ox-adapter-codex")
	build.Dir = supFindRepoRoot(t)
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build Codex adapter: %s", out)
	adapter, err := adapters.NewExternalAdapter(binaryPath)
	require.NoError(t, err)
	adapters.Register(adapter)
	t.Cleanup(func() { adapters.Unregister("codex") })

	for _, publishedDirExists := range []bool{false, true} {
		name := "before publication"
		if publishedDirExists {
			name = "published folder exists"
		}
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
			t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			t.Setenv("OX_XDG_DISABLE", "")
			projectRoot := t.TempDir()
			cfg := &projectconfig.ProjectConfig{RepoID: "repo_ipc_watch", Endpoint: "https://test.sageox.ai"}
			require.NoError(t, projectconfig.SaveProjectConfig(projectRoot, cfg))
			ledgerPath := paths.LedgersDataDir(cfg.RepoID, cfg.Endpoint)
			source := filepath.Join(home, ".codex", "sessions", "session.jsonl")
			require.NoError(t, os.MkdirAll(filepath.Dir(source), 0o755))
			require.NoError(t, os.WriteFile(source, []byte(`{"timestamp":"2026-09-07T16:50:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Capture this Codex session through IPC."}]}}`+"\n"), 0o600))
			state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
				AgentID: "OxIPC1", AdapterName: "codex", SessionFile: source,
				WorkspacePath: projectRoot, ParentPID: os.Getpid(), WatchMode: "tail",
			})
			require.NoError(t, err)
			sessionName := filepath.Base(state.SessionPath)
			require.Equal(t, filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName), state.SessionPath)
			rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
			writer, err := session.NewRawWriter(rawPath, projectRoot)
			require.NoError(t, err)
			require.NoError(t, writer.WriteRaw(map[string]any{"type": "header", "metadata": map[string]any{"agent_id": state.AgentID}}))
			require.NoError(t, writer.Close())
			publishedPath := filepath.Join(ledgerPath, "sessions", sessionName)
			if publishedDirExists {
				require.NoError(t, os.MkdirAll(publishedPath, 0o755))
			}
			publishedRaw := filepath.Join(publishedPath, "raw.jsonl")

			logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))
			d := New(&Config{LedgerPath: ledgerPath}, logger)
			d.sessionWatcher = agentwork.NewSessionWatcherManager(logger)
			defer d.sessionWatcher.StopAll()
			svc := &daemonServiceImpl{d: d}
			svc.SessionWatchStart(SessionWatchStartPayload{
				SessionName: sessionName, SessionFile: source, AdapterName: "codex",
			})
			require.Eventually(t, func() bool {
				return session.HasSubstantiveEntries(rawPath) || session.RawJSONLHasData(publishedPath) || len(d.sessionWatcher.ActiveSessions()) == 0
			}, 5*time.Second, 10*time.Millisecond, "watcher did not capture the source")
			assert.True(t, session.HasSubstantiveEntries(rawPath), "IPC must write into the active recording cache")
			assert.NoFileExists(t, publishedRaw, "live capture must not write hydrated bytes into tracked ledger sessions")
		})
	}
}

// --- A. session_watch_start handler ---

func newSessionWatchTestServer() *Server {
	return NewServer(slog.Default())
}

// TestHandleSessionWatchStart_ValidPayload verifies the handler dispatches
// a valid payload to the service.
// Failure prevented: IPC message arrives but daemon ignores it.
func TestHandleSessionWatchStart_ValidPayload(t *testing.T) {
	t.Parallel()
	s := newSessionWatchTestServer()

	want := SessionWatchStartPayload{
		SessionName: "2026-03-31T10-00-ryan-OxAbc1",
		SessionFile: "/home/user/.codex/sessions/2026/03/31/session.jsonl",
		AdapterName: "codex",
	}

	var got SessionWatchStartPayload
	s.SetSessionWatchStartHandler(func(p SessionWatchStartPayload) {
		got = p
	})

	raw, _ := json.Marshal(want)
	result := handleSessionWatchStart(s, Message{Type: MsgTypeSessionWatchStart, Payload: raw}, nil)

	assert.Equal(t, want, got)
	assert.True(t, result.SkipDefault, "fire-and-forget must set SkipDefault")
	assert.Nil(t, result.Response, "fire-and-forget must not return a response")
}

// TestHandleSessionWatchStart_InvalidJSON verifies malformed JSON doesn't panic.
// Failure prevented: daemon crashes on malformed IPC message.
func TestHandleSessionWatchStart_InvalidJSON(t *testing.T) {
	t.Parallel()
	s := newSessionWatchTestServer()

	var called atomic.Bool
	s.SetSessionWatchStartHandler(func(_ SessionWatchStartPayload) {
		called.Store(true)
	})

	result := handleSessionWatchStart(s, Message{Payload: []byte(`{bad`)}, nil)

	assert.False(t, called.Load(), "handler must not be called on invalid JSON")
	assert.True(t, result.SkipDefault)
}

// TestHandleSessionWatchStart_MissingRequiredFields verifies incomplete payload
// is rejected gracefully.
// Failure prevented: daemon starts watcher with empty session file path.
func TestHandleSessionWatchStart_MissingRequiredFields(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		payload SessionWatchStartPayload
	}{
		{"empty session_name", SessionWatchStartPayload{SessionFile: "/f", AdapterName: "codex"}},
		{"empty session_file", SessionWatchStartPayload{SessionName: "s", AdapterName: "codex"}},
		{"empty adapter_name", SessionWatchStartPayload{SessionName: "s", SessionFile: "/f"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSessionWatchTestServer()
			var called atomic.Bool
			s.SetSessionWatchStartHandler(func(_ SessionWatchStartPayload) {
				called.Store(true)
			})

			raw, _ := json.Marshal(tc.payload)
			result := handleSessionWatchStart(s, Message{Payload: raw}, nil)

			assert.False(t, called.Load(), "handler must not be called with missing fields")
			assert.True(t, result.SkipDefault)
		})
	}
}

// TestHandleSessionWatchStart_NoHandler verifies no panic when handler not wired.
// Failure prevented: daemon panics during staged startup before handlers are set.
func TestHandleSessionWatchStart_NoHandler(t *testing.T) {
	t.Parallel()
	s := newSessionWatchTestServer()

	payload := SessionWatchStartPayload{
		SessionName: "s", SessionFile: "/f", AdapterName: "codex",
	}
	raw, _ := json.Marshal(payload)
	result := handleSessionWatchStart(s, Message{Payload: raw}, nil)

	assert.True(t, result.SkipDefault)
}

// --- B. session_watch_stop handler ---

// TestHandleSessionWatchStop_ValidPayload verifies the handler dispatches
// a valid payload to the service.
// Failure prevented: stop IPC arrives but daemon keeps tailing.
func TestHandleSessionWatchStop_ValidPayload(t *testing.T) {
	t.Parallel()
	s := newSessionWatchTestServer()

	want := SessionWatchStopPayload{
		SessionName: "2026-03-31T10-00-ryan-OxAbc1",
	}

	var got SessionWatchStopPayload
	s.SetSessionWatchStopHandler(func(p SessionWatchStopPayload) {
		got = p
	})

	raw, _ := json.Marshal(want)
	result := handleSessionWatchStop(s, Message{Type: MsgTypeSessionWatchStop, Payload: raw}, nil)

	assert.Equal(t, want, got)
	assert.True(t, result.SkipDefault)
	assert.Nil(t, result.Response)
}

// TestHandleSessionWatchStop_InvalidJSON verifies malformed JSON doesn't panic.
// Failure prevented: daemon crashes on malformed stop message.
func TestHandleSessionWatchStop_InvalidJSON(t *testing.T) {
	t.Parallel()
	s := newSessionWatchTestServer()

	var called atomic.Bool
	s.SetSessionWatchStopHandler(func(_ SessionWatchStopPayload) {
		called.Store(true)
	})

	result := handleSessionWatchStop(s, Message{Payload: []byte(`{bad`)}, nil)

	assert.False(t, called.Load())
	assert.True(t, result.SkipDefault)
}

// TestHandleSessionWatchStop_MissingSessionName verifies empty session_name is rejected.
// Failure prevented: daemon stops wrong watcher or no-ops silently.
func TestHandleSessionWatchStop_MissingSessionName(t *testing.T) {
	t.Parallel()
	s := newSessionWatchTestServer()

	var called atomic.Bool
	s.SetSessionWatchStopHandler(func(_ SessionWatchStopPayload) {
		called.Store(true)
	})

	raw, _ := json.Marshal(SessionWatchStopPayload{})
	result := handleSessionWatchStop(s, Message{Payload: raw}, nil)

	assert.False(t, called.Load())
	assert.True(t, result.SkipDefault)
}

// --- C. Client methods ---

// TestClient_SessionWatchStart_MarshalPayload verifies payload serialization.
// Failure prevented: client sends malformed JSON the daemon can't parse.
func TestClient_SessionWatchStart_MarshalPayload(t *testing.T) {
	t.Parallel()
	payload := SessionWatchStartPayload{
		SessionName: "test-session",
		SessionFile: "/path/to/session.jsonl",
		AdapterName: "codex",
	}
	raw, err := json.Marshal(payload)
	require.NoError(t, err)

	var decoded SessionWatchStartPayload
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, payload, decoded)
}

// TestClient_SessionWatchStop_MarshalPayload verifies payload serialization.
// Failure prevented: client sends malformed JSON the daemon can't parse.
func TestClient_SessionWatchStop_MarshalPayload(t *testing.T) {
	t.Parallel()
	payload := SessionWatchStopPayload{
		SessionName: "test-session",
	}
	raw, err := json.Marshal(payload)
	require.NoError(t, err)

	var decoded SessionWatchStopPayload
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, payload, decoded)
}

// --- D. CallbackService methods ---

// TestCallbackService_SessionWatch_NilHandlerSafe verifies no panic when
// SessionWatchStart/Stop are called without handlers set.
// Failure prevented: daemon panics during staged startup.
func TestCallbackService_SessionWatch_NilHandlerSafe(t *testing.T) {
	t.Parallel()
	svc := &CallbackService{}

	// must not panic
	svc.SessionWatchStart(SessionWatchStartPayload{SessionName: "s"})
	svc.SessionWatchStop(SessionWatchStopPayload{SessionName: "s"})
}

// --- E. Edge-case and failure-mode tests ---

// TestHandleSessionWatchStart_PathTraversal verifies behavior when SessionFile
// contains path traversal sequences. The IPC handler only checks for empty fields;
// path validation (absolute path check) happens in SessionWatcherManager.StartWatch.
// NOTE: the handler does NOT reject path traversal — it passes the payload through.
// This is acceptable because the downstream SessionWatcherManager validates absolute
// paths and the traversal resolves to an absolute path. But the handler does not
// sanitize or canonicalize the path, which is a gap worth noting.
// Failure prevented: documents that path traversal is not blocked at IPC layer.
func TestHandleSessionWatchStart_PathTraversal(t *testing.T) {
	t.Parallel()
	s := newSessionWatchTestServer()

	var got SessionWatchStartPayload
	s.SetSessionWatchStartHandler(func(p SessionWatchStartPayload) {
		got = p
	})

	payload := SessionWatchStartPayload{
		SessionName: "test-session",
		SessionFile: "/tmp/../../../etc/passwd",
		AdapterName: "codex",
	}

	raw, _ := json.Marshal(payload)
	result := handleSessionWatchStart(s, Message{Type: MsgTypeSessionWatchStart, Payload: raw}, nil)

	// the handler passes it through because all required fields are non-empty
	// and the handler does not validate path contents — only emptiness
	assert.True(t, result.SkipDefault)
	assert.Equal(t, payload.SessionFile, got.SessionFile,
		"handler passes path traversal through without sanitization")
}

// TestHandleSessionWatchStart_RelativeSessionFile verifies that a relative path
// is accepted by the IPC handler (it only checks for empty strings). Path validation
// is delegated to SessionWatcherManager.StartWatch which rejects non-absolute paths.
// Failure prevented: documents the validation boundary between IPC handler and manager.
func TestHandleSessionWatchStart_RelativeSessionFile(t *testing.T) {
	t.Parallel()
	s := newSessionWatchTestServer()

	var got SessionWatchStartPayload
	var handlerCalled atomic.Bool
	s.SetSessionWatchStartHandler(func(p SessionWatchStartPayload) {
		handlerCalled.Store(true)
		got = p
	})

	payload := SessionWatchStartPayload{
		SessionName: "test-session",
		SessionFile: "relative/path.jsonl",
		AdapterName: "codex",
	}

	raw, _ := json.Marshal(payload)
	result := handleSessionWatchStart(s, Message{Type: MsgTypeSessionWatchStart, Payload: raw}, nil)

	// the IPC handler only checks for empty fields, not path format
	assert.True(t, result.SkipDefault)
	assert.True(t, handlerCalled.Load(),
		"handler is called because the field is non-empty; relative path rejection happens in SessionWatcherManager")
	assert.Equal(t, "relative/path.jsonl", got.SessionFile)
}

// TestHandleSessionWatchStart_WhitespaceOnlySessionName verifies that a
// whitespace-only SessionName is treated differently from empty string.
// The handler checks `== ""`, not `strings.TrimSpace`, so whitespace-only
// strings pass validation. This documents the current behavior.
// Failure prevented: whitespace-only session names silently accepted, causing
// lookup failures later when the watcher map key is "   ".
func TestHandleSessionWatchStart_WhitespaceOnlySessionName(t *testing.T) {
	t.Parallel()
	s := newSessionWatchTestServer()

	var got SessionWatchStartPayload
	var handlerCalled atomic.Bool
	s.SetSessionWatchStartHandler(func(p SessionWatchStartPayload) {
		handlerCalled.Store(true)
		got = p
	})

	payload := SessionWatchStartPayload{
		SessionName: "   ",
		SessionFile: "/path/to/session.jsonl",
		AdapterName: "codex",
	}

	raw, _ := json.Marshal(payload)
	result := handleSessionWatchStart(s, Message{Type: MsgTypeSessionWatchStart, Payload: raw}, nil)

	assert.True(t, result.SkipDefault)
	// NOTE: the handler uses `payload.SessionName == ""` not `strings.TrimSpace`,
	// so whitespace-only passes validation. This is a gap — whitespace-only session
	// names are semantically invalid but currently accepted at the IPC layer.
	assert.True(t, handlerCalled.Load(),
		"whitespace-only SessionName passes the empty-string check and reaches the handler")
	assert.Equal(t, "   ", got.SessionName)
}

// TestHandleSessionWatchStop_WhitespaceOnlySessionName mirrors the start test:
// whitespace-only session names pass the `== ""` check in the stop handler too.
// Failure prevented: inconsistent validation between start and stop handlers.
func TestHandleSessionWatchStop_WhitespaceOnlySessionName(t *testing.T) {
	t.Parallel()
	s := newSessionWatchTestServer()

	var got SessionWatchStopPayload
	var handlerCalled atomic.Bool
	s.SetSessionWatchStopHandler(func(p SessionWatchStopPayload) {
		handlerCalled.Store(true)
		got = p
	})

	raw, _ := json.Marshal(SessionWatchStopPayload{
		SessionName: "   ",
	})
	result := handleSessionWatchStop(s, Message{Payload: raw}, nil)

	assert.True(t, result.SkipDefault)
	// same gap as start handler: whitespace-only passes `== ""` check
	assert.True(t, handlerCalled.Load(),
		"whitespace-only SessionName passes the empty-string check in stop handler")
	assert.Equal(t, "   ", got.SessionName)
}
