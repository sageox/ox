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
		LedgerPath:  "/ledger/path",
		CachePath:   "/ledger/path/.sageox/cache/sessions/2026-03-31T10-00-ryan-OxAbc1",
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
		LedgerPath:  "/ledger/path",
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

	raw, _ := json.Marshal(SessionWatchStopPayload{LedgerPath: "/p"})
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
		LedgerPath:  "/ledger",
		CachePath:   "/cache",
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
		LedgerPath:  "/ledger",
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
