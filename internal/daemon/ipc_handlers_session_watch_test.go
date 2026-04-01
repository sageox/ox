package daemon

import (
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
