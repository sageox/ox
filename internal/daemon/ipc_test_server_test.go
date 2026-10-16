package daemon

import (
	"context"
	"testing"
	"time"
)

// ipcTestWait bounds every wait on a test IPC server. It is a ceiling for a
// slow runner, not a pacing delay: a healthy server answers in milliseconds.
const ipcTestWait = 10 * time.Second

// startIPCServer runs server.Start in the background and returns once the
// server is listening, so the test's first dial cannot race the listener.
//
// A fixed sleep before the first dial is not a readiness check: on a loaded CI
// runner the socket is not there yet, and the dial fails with "no such file or
// directory" (or "connection refused" on a stale socket file left by an
// earlier run). Start publishes its listener only after listen returns, so a
// non-nil listener means a dial will connect.
func startIPCServer(ctx context.Context, t *testing.T, server *Server) {
	t.Helper()
	startErr := make(chan error, 1)
	go func() { startErr <- server.Start(ctx) }()

	deadline := time.After(ipcTestWait)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		server.mu.Lock()
		listening := server.listener != nil
		server.mu.Unlock()
		if listening {
			return
		}
		select {
		case err := <-startErr:
			t.Fatalf("ipc server stopped before listening on %s: %v", SocketPath(), err)
		case <-deadline:
			t.Fatalf("ipc server not listening on %s after %s", SocketPath(), ipcTestWait)
		case <-tick.C:
		}
	}
}

// awaitSignal waits for a test's handler to report that it started. A request
// that never reaches the server fails the test instead of hanging it until the
// package timeout.
func awaitSignal(t *testing.T, started <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(ipcTestWait):
		t.Fatalf("timed out after %s waiting for %s", ipcTestWait, what)
	}
}
