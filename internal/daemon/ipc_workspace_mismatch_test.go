package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingHandler keeps every slog record, at every level, for assertions.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}
func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

// byMessage returns the records whose message equals msg.
func (h *recordingHandler) byMessage(msg string) []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []slog.Record
	for _, r := range h.records {
		if r.Message == msg {
			out = append(out, r)
		}
	}
	return out
}

func recordAttr(r slog.Record, key string) (string, bool) {
	var val string
	var found bool
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			val, found = a.Value.String(), true
			return false
		}
		return true
	})
	return val, found
}

// startRecordingServer runs a real IPC server whose logger records everything,
// in an isolated runtime dir short enough for a unix socket path.
func startRecordingServer(t *testing.T) (*Server, *recordingHandler) {
	t.Helper()
	runtimeDir, err := os.MkdirTemp("/tmp", "ox-wsm-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	handler := &recordingHandler{}
	server := NewServer(slog.New(handler))
	server.SetHandlers(func() error { return nil }, func() {}, func() *StatusData { return &StatusData{Running: true} })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("server did not stop")
		}
	})

	require.Eventually(t, func() bool {
		_, err := os.Stat(SocketPath())
		return err == nil
	}, 2*time.Second, 10*time.Millisecond, "server should bind its socket")
	return server, handler
}

// sendPing sends one ping with the given identity and waits for the response,
// so the daemon has finished handling it before the caller counts log lines.
func sendPing(t *testing.T, msg Message) {
	t.Helper()
	msg.Type = MsgTypePing
	conn, err := net.DialTimeout("unix", SocketPath(), 2*time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))

	line, err := json.Marshal(msg)
	require.NoError(t, err)
	_, err = conn.Write(append(line, '\n'))
	require.NoError(t, err)
	_, err = bufio.NewReader(conn).ReadBytes('\n')
	require.NoError(t, err)
}

// TestWorkspaceMismatch_WarnsOncePerCaller pins the log volume of the most
// frequent line in the daemon log.
//
// Failure prevented: every IPC request from a clone or subdirectory whose
// workspace ID differs from the daemon's logged a WARN — ~39,000 lines a day
// from one coworker, bursting to 64 per minute, burying every real warning and
// growing the log to 96 MB in a week. The request is still served normally, so
// the line carried no new information after the first.
func TestWorkspaceMismatch_WarnsOncePerCaller(t *testing.T) {
	const foreign = "ffffffff"

	tests := []struct {
		name      string
		msgs      func() []Message
		wantWarns int
		wantDebug int
	}{
		{
			name: "100 requests from one caller warn exactly once",
			msgs: func() []Message {
				msgs := make([]Message, 100)
				for i := range msgs {
					msgs[i] = Message{WorkspaceID: foreign, CallerID: "caller-a"}
				}
				return msgs
			},
			wantWarns: 1,
			wantDebug: 99,
		},
		{
			name: "each distinct caller gets its own single warning",
			msgs: func() []Message {
				var msgs []Message
				for i := 0; i < 10; i++ {
					msgs = append(msgs,
						Message{WorkspaceID: foreign, CallerID: "caller-a"},
						Message{WorkspaceID: foreign, CallerID: "caller-b"},
					)
				}
				return msgs
			},
			wantWarns: 2,
			wantDebug: 18,
		},
		{
			name: "callers without an ID are keyed by their workspace ID",
			msgs: func() []Message {
				var msgs []Message
				for i := 0; i < 5; i++ {
					msgs = append(msgs,
						Message{WorkspaceID: "aaaaaaaa"},
						Message{WorkspaceID: "bbbbbbbb"},
					)
				}
				return msgs
			},
			wantWarns: 2,
			wantDebug: 8,
		},
		{
			name: "a matching workspace ID logs nothing",
			msgs: func() []Message {
				msgs := make([]Message, 20)
				for i := range msgs {
					msgs[i] = Message{WorkspaceID: CurrentWorkspaceID(), CallerID: "caller-a"}
				}
				return msgs
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, handler := startRecordingServer(t)

			for _, msg := range tt.msgs() {
				sendPing(t, msg)
			}

			var warns, debugs []slog.Record
			for _, r := range handler.byMessage("workspace mismatch") {
				switch r.Level {
				case slog.LevelWarn:
					warns = append(warns, r)
				case slog.LevelDebug:
					debugs = append(debugs, r)
				default:
					t.Errorf("unexpected level %s for workspace mismatch", r.Level)
				}
			}
			assert.Equal(t, tt.wantWarns, len(warns), "WARN count for workspace mismatch")
			assert.Equal(t, tt.wantDebug, len(debugs), "DEBUG count for workspace mismatch")

			// the line must say which request type tripped it
			for _, r := range append(warns, debugs...) {
				msgType, ok := recordAttr(r, "msg_type")
				assert.True(t, ok, "workspace mismatch must carry msg_type")
				assert.Equal(t, MsgTypePing, msgType)
			}
		})
	}
}
