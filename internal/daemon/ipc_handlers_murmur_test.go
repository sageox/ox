package daemon

import (
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
)

// newMurmurTestServer returns a Server with a discarding logger so that
// error-path calls to s.logger.Debug() do not panic with a nil logger.
func newMurmurTestServer() *Server {
	return NewServer(slog.Default())
}

func TestHandleMurmur_ValidPayload(t *testing.T) {
	t.Parallel()
	s := newMurmurTestServer()

	want := MurmurPayload{
		TargetDir: "/ledger/path",
		Content:   "fix: update routing logic",
		RelPath:   "murmurs/2026-03-28T10-00-user-Abcd.json",
	}

	var gotPayload MurmurPayload
	s.SetMurmurHandler(func(p MurmurPayload) {
		gotPayload = p
	})

	raw, _ := json.Marshal(want)
	msg := Message{Type: MsgTypeMurmur, Payload: raw}

	result := handleMurmur(s, msg, nil)

	if gotPayload.TargetDir != want.TargetDir {
		t.Errorf("TargetDir: got %q, want %q", gotPayload.TargetDir, want.TargetDir)
	}
	if gotPayload.Content != want.Content {
		t.Errorf("Content: got %q, want %q", gotPayload.Content, want.Content)
	}
	if gotPayload.RelPath != want.RelPath {
		t.Errorf("RelPath: got %q, want %q", gotPayload.RelPath, want.RelPath)
	}
	if !result.SkipDefault {
		t.Error("SkipDefault must be true for fire-and-forget handler")
	}
	if result.Response != nil {
		t.Errorf("Response must be nil for fire-and-forget, got %+v", result.Response)
	}
}

// SkipDefault must be true in all cases — the router skips sending a response
// when SkipDefault is true, so returning false on error paths would cause the
// caller to hang waiting for a response that will never come.
func TestHandleMurmur_SkipDefaultAlwaysTrue(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		payload json.RawMessage
	}{
		{
			name:    "valid payload",
			payload: mustMarshalMurmur(t, MurmurPayload{TargetDir: "/d", RelPath: "r.json"}),
		},
		{
			name:    "invalid json",
			payload: json.RawMessage(`{not json`),
		},
		{
			name:    "empty target_dir",
			payload: mustMarshalMurmur(t, MurmurPayload{TargetDir: "", RelPath: "r.json"}),
		},
		{
			name:    "empty rel_path",
			payload: mustMarshalMurmur(t, MurmurPayload{TargetDir: "/d", RelPath: ""}),
		},
		{
			name:    "both empty",
			payload: mustMarshalMurmur(t, MurmurPayload{}),
		},
		{
			name:    "nil payload bytes",
			payload: nil,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newMurmurTestServer()
			msg := Message{Type: MsgTypeMurmur, Payload: tc.payload}
			result := handleMurmur(s, msg, nil)
			if !result.SkipDefault {
				t.Errorf("SkipDefault must be true for case %q", tc.name)
			}
			if result.Response != nil {
				t.Errorf("Response must be nil for case %q, got %+v", tc.name, result.Response)
			}
		})
	}
}

func TestHandleMurmur_InvalidJSON_DoesNotCallPublish(t *testing.T) {
	t.Parallel()
	s := newMurmurTestServer()

	var called bool
	s.SetMurmurHandler(func(p MurmurPayload) {
		called = true
	})

	msg := Message{
		Type:    MsgTypeMurmur,
		Payload: json.RawMessage(`{not valid json`),
	}

	result := handleMurmur(s, msg, nil)

	if called {
		t.Error("PublishMurmur must not be called when JSON is malformed")
	}
	if !result.SkipDefault {
		t.Error("SkipDefault must be true even when JSON is malformed")
	}
	if result.Response != nil {
		t.Error("Response must be nil even when JSON is malformed")
	}
}

func TestHandleMurmur_EmptyTargetDir_DoesNotCallPublish(t *testing.T) {
	t.Parallel()
	s := newMurmurTestServer()

	var called bool
	s.SetMurmurHandler(func(p MurmurPayload) {
		called = true
	})

	payload, _ := json.Marshal(MurmurPayload{TargetDir: "", RelPath: "murmurs/x.json"})
	msg := Message{Type: MsgTypeMurmur, Payload: payload}

	handleMurmur(s, msg, nil)

	if called {
		t.Error("PublishMurmur must not be called when TargetDir is empty")
	}
}

func TestHandleMurmur_EmptyRelPath_DoesNotCallPublish(t *testing.T) {
	t.Parallel()
	s := newMurmurTestServer()

	var called bool
	s.SetMurmurHandler(func(p MurmurPayload) {
		called = true
	})

	payload, _ := json.Marshal(MurmurPayload{TargetDir: "/ledger/path", RelPath: ""})
	msg := Message{Type: MsgTypeMurmur, Payload: payload}

	handleMurmur(s, msg, nil)

	if called {
		t.Error("PublishMurmur must not be called when RelPath is empty")
	}
}

func TestHandleMurmur_BothFieldsEmpty_DoesNotCallPublish(t *testing.T) {
	t.Parallel()
	s := newMurmurTestServer()

	var called bool
	s.SetMurmurHandler(func(p MurmurPayload) {
		called = true
	})

	payload, _ := json.Marshal(MurmurPayload{TargetDir: "", RelPath: ""})
	msg := Message{Type: MsgTypeMurmur, Payload: payload}

	handleMurmur(s, msg, nil)

	if called {
		t.Error("PublishMurmur must not be called when both TargetDir and RelPath are empty")
	}
}

// No handler wired means CallbackService.PublishMurmur is a no-op (fn == nil).
// The handler must not panic in this default state.
func TestHandleMurmur_NoHandlerWired_DoesNotPanic(t *testing.T) {
	t.Parallel()
	s := newMurmurTestServer()
	// do NOT call SetMurmurHandler — leave onPublishMurmur nil

	payload, _ := json.Marshal(MurmurPayload{TargetDir: "/ledger/path", RelPath: "murmurs/x.json"})
	msg := Message{Type: MsgTypeMurmur, Payload: payload}

	// should not panic
	result := handleMurmur(s, msg, nil)

	if !result.SkipDefault {
		t.Error("SkipDefault must be true even when no handler is wired")
	}
	if result.Response != nil {
		t.Error("Response must be nil even when no handler is wired")
	}
}

// MurmurJSON is an opaque []byte blob. Ensure it survives the JSON round-trip
// through the handler without being dropped, decoded, or re-encoded.
func TestHandleMurmur_MurmurJSONPreserved(t *testing.T) {
	t.Parallel()
	s := newMurmurTestServer()

	// arbitrary nested JSON representing a serialized MurmurFile
	originalJSON := []byte(`{"schema_version":"1","type":"thought","content":"be careful with auth","author":"agent-001","ts":"2026-03-28T10:00:00Z"}`)

	want := MurmurPayload{
		TargetDir:  "/ledger/path",
		RelPath:    "murmurs/2026-03-28T10-00-agent-Abcd.json",
		MurmurJSON: originalJSON,
	}

	var gotJSON []byte
	s.SetMurmurHandler(func(p MurmurPayload) {
		gotJSON = p.MurmurJSON
	})

	raw, _ := json.Marshal(want)
	msg := Message{Type: MsgTypeMurmur, Payload: raw}

	handleMurmur(s, msg, nil)

	if string(gotJSON) != string(originalJSON) {
		t.Errorf("MurmurJSON not preserved\n  got:  %s\n  want: %s", gotJSON, originalJSON)
	}
}

// Concurrent calls must not race or panic. The murmur handler itself is
// stateless; locking is inside CallbackService.PublishMurmur.
func TestHandleMurmur_Concurrent(t *testing.T) {
	t.Parallel()
	s := newMurmurTestServer()

	const goroutines = 50
	var callCount atomic.Int64

	s.SetMurmurHandler(func(p MurmurPayload) {
		callCount.Add(1)
	})

	payload, _ := json.Marshal(MurmurPayload{
		TargetDir: "/ledger/path",
		RelPath:   "murmurs/concurrent.json",
	})
	msg := Message{Type: MsgTypeMurmur, Payload: payload}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			result := handleMurmur(s, msg, nil)
			if !result.SkipDefault {
				t.Errorf("SkipDefault must be true in concurrent call")
			}
			if result.Response != nil {
				t.Errorf("Response must be nil in concurrent call")
			}
		}()
	}
	wg.Wait()

	if got := callCount.Load(); got != goroutines {
		t.Errorf("expected %d PublishMurmur calls, got %d", goroutines, got)
	}
}

// mustMarshalMurmur marshals a MurmurPayload and fatals the test on error.
// Used in table-driven tests where we want clean setup, not error noise.
func mustMarshalMurmur(t *testing.T, p MurmurPayload) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal MurmurPayload: %v", err)
	}
	return b
}
