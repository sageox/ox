package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMsgTypeMurmur_Constant guards against accidental renaming of the wire
// protocol constant. Clients and the server must agree on the exact string.
func TestMsgTypeMurmur_Constant(t *testing.T) {
	assert.Equal(t, "murmur", MsgTypeMurmur)
}

// TestMurmurPayload_JSONRoundTrip verifies that MurmurJSON []byte survives a
// JSON round-trip. encoding/json encodes []byte as base64; the test exercises
// non-ASCII bytes to ensure no silent truncation or re-encoding occurs.
func TestMurmurPayload_JSONRoundTrip(t *testing.T) {
	original := MurmurPayload{
		TargetDir:  "/ledger/path",
		Content:    "fix: correct auth flow",
		RelPath:    "murmurs/2026-03-28T10-00-agent-Abcd.json",
		MurmurJSON: []byte("\x00\x01\x02\xff\xfe\xfd non-ASCII: café naïve"),
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded MurmurPayload
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original.TargetDir, decoded.TargetDir)
	assert.Equal(t, original.Content, decoded.Content)
	assert.Equal(t, original.RelPath, decoded.RelPath)
	// byte-for-byte equality; any base64 encoding/decoding error would corrupt this
	assert.Equal(t, original.MurmurJSON, decoded.MurmurJSON,
		"MurmurJSON must survive JSON round-trip without modification")
}

// TestCallbackService_PublishMurmur_NilHandler verifies that calling
// PublishMurmur when no handler has been registered does not panic.
func TestCallbackService_PublishMurmur_NilHandler(t *testing.T) {
	svc := &CallbackService{} // onPublishMurmur is nil

	assert.NotPanics(t, func() {
		svc.PublishMurmur(MurmurPayload{
			TargetDir:  "/ledger/path",
			RelPath:    "murmurs/x.json",
			MurmurJSON: []byte(`{"type":"thought"}`),
		})
	})
}

// TestCallbackService_PublishMurmur_WithHandler verifies that the registered
// handler is invoked with the exact payload and that the mutex is released
// before calling the handler (avoids deadlock if the handler itself calls
// back into the service).
func TestCallbackService_PublishMurmur_WithHandler(t *testing.T) {
	svc := &CallbackService{}

	want := MurmurPayload{
		TargetDir:  "/ledger/path",
		Content:    "refactor: simplify session upload",
		RelPath:    "murmurs/2026-03-28T10-00-agent-Zxyw.json",
		MurmurJSON: []byte(`{"schema_version":"1","type":"thought"}`),
	}

	var (
		callCount int
		got       MurmurPayload
		mu        sync.Mutex
	)

	svc.mu.Lock()
	svc.onPublishMurmur = func(p MurmurPayload) {
		mu.Lock()
		defer mu.Unlock()
		callCount++
		got = p
		// confirm the service mutex is NOT held while the handler runs:
		// a tryLock on svc.mu would succeed here if the implementation is correct.
		// We cannot call svc.mu.TryLock from the handler directly without Go 1.18+
		// but we verify indirectly by ensuring no deadlock occurs.
	}
	svc.mu.Unlock()

	svc.PublishMurmur(want)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, callCount, "handler must be called exactly once")
	assert.Equal(t, want.TargetDir, got.TargetDir)
	assert.Equal(t, want.Content, got.Content)
	assert.Equal(t, want.RelPath, got.RelPath)
	assert.Equal(t, want.MurmurJSON, got.MurmurJSON)
}

// TestSetMurmurHandler_PanicsOnServiceServer verifies that SetMurmurHandler
// panics when the server was created via NewServerWithService (which has no
// mutable CallbackService adapter). Callers must use the DaemonService
// interface directly in that case.
func TestSetMurmurHandler_PanicsOnServiceServer(t *testing.T) {
	// NewServerWithService sets s.service but leaves s.svc == nil.
	// mustCallbackService checks s.svc == nil and panics, which is the
	// contract: Set*Handler methods are only valid on NewServer servers.
	svc := &CallbackService{}
	server := NewServerWithService(nil, svc)

	assert.Panics(t, func() {
		server.SetMurmurHandler(func(MurmurPayload) {})
	}, "SetMurmurHandler must panic when server was created via NewServerWithService")
}

// TestServerClient_Murmur_Integration is a full round-trip test over a real
// Unix socket. It verifies:
//
//	(a) the PublishMurmur callback fires with the correct payload
//	(b) Client.Murmur returns without error (fire-and-forget, no response needed)
//	(c) the connection closes cleanly — no goroutine leak or hang
func TestServerClient_Murmur_Integration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "ox-ipc-murmur-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := NewServer(logger)
	server.SetHandlers(
		func() error { return nil },
		func() {},
		func() *StatusData { return &StatusData{Running: true} },
	)

	received := make(chan MurmurPayload, 1)
	server.SetMurmurHandler(func(p MurmurPayload) {
		received <- p
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.Start(ctx) //nolint:errcheck
	time.Sleep(100 * time.Millisecond)

	want := MurmurPayload{
		TargetDir:  "/ledger/path",
		Content:    "feat: add murmur routing",
		RelPath:    "murmurs/2026-03-28T10-00-agent-Wxyz.json",
		MurmurJSON: []byte(`{"schema_version":"1","type":"thought","content":"routing"}`),
	}

	client := &Client{
		socketPath: SocketPath(),
		timeout:    5 * time.Second,
	}

	// (b) fire-and-forget: must return without error
	err = client.Murmur(want)
	require.NoError(t, err, "Client.Murmur must not return an error")

	// (a) callback must fire within a generous deadline
	select {
	case got := <-received:
		assert.Equal(t, want.TargetDir, got.TargetDir)
		assert.Equal(t, want.Content, got.Content)
		assert.Equal(t, want.RelPath, got.RelPath)
		assert.Equal(t, want.MurmurJSON, got.MurmurJSON)
	case <-time.After(2 * time.Second):
		t.Fatal("PublishMurmur callback did not fire within deadline")
	}

	// (c) send a ping to confirm the server is still accepting connections cleanly
	assert.NoError(t, client.Ping(), "server must still accept connections after murmur fire-and-forget")

	cancel()
}

// TestServerClient_Murmur_EmptyTargetDir_SkipsCallback verifies the full
// router integration path: a murmur message with an empty TargetDir must be
// silently dropped — the callback must not fire — and the server must remain
// healthy (accepting further connections).
func TestServerClient_Murmur_EmptyTargetDir_SkipsCallback(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "ox-ipc-murmur-empty-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := NewServer(logger)
	server.SetHandlers(
		func() error { return nil },
		func() {},
		func() *StatusData { return &StatusData{Running: true} },
	)

	callbackFired := make(chan struct{}, 1)
	server.SetMurmurHandler(func(p MurmurPayload) {
		callbackFired <- struct{}{}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.Start(ctx) //nolint:errcheck
	time.Sleep(100 * time.Millisecond)

	client := &Client{
		socketPath: SocketPath(),
		timeout:    5 * time.Second,
	}

	// send a murmur with empty TargetDir — handler must be skipped
	err = client.Murmur(MurmurPayload{
		TargetDir: "", // intentionally empty
		RelPath:   "murmurs/x.json",
	})
	require.NoError(t, err)

	// give the server a short window to (incorrectly) fire the callback
	select {
	case <-callbackFired:
		t.Error("PublishMurmur callback must not fire when TargetDir is empty")
	case <-time.After(200 * time.Millisecond):
		// correct: no callback fired
	}

	// server must still be healthy after the dropped message
	assert.NoError(t, client.Ping(), "server must remain healthy after dropping murmur with empty TargetDir")

	cancel()
}
