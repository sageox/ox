package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	friction "github.com/sageox/frictionax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSendFrictionEvent_DeliversToSocket verifies that sendFrictionEventTo sends
// a friction event to the daemon socket via IPC. This is a regression test —
// the sendFrictionEvent call was silently dropped during the frictionax migration,
// breaking the CLI→daemon telemetry pipeline with zero test failures.
func TestSendFrictionEvent_DeliversToSocket(t *testing.T) {
	t.Parallel()

	// set up a temp Unix socket (short path for macOS 104-char limit)
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("ox-ftest-%d.sock", time.Now().UnixNano()%100000))
	t.Cleanup(func() { os.Remove(socketPath) })

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	defer listener.Close()

	received := make(chan []byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 8192)
		if n, _ := conn.Read(buf); n > 0 {
			received <- buf[:n]
		}
	}()

	// build event matching what frictionEngine.Handle() returns
	event := &friction.FrictionEvent{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Kind:       "unknown-command",
		Command:    "ox",
		Subcommand: "badcommand",
		Actor:      "human",
		Input:      "ox badcommand",
		ErrorMsg:   "unknown command \"badcommand\" for \"ox\"",
	}

	// exercise the real sendFrictionEventTo with injected socket
	sendFrictionEventTo(event, socketPath)

	select {
	case msg := <-received:
		msgStr := string(msg)
		assert.Contains(t, msgStr, `"type":"friction"`)
		assert.Contains(t, msgStr, `"ox badcommand"`)
		assert.Contains(t, msgStr, `"unknown-command"`)
	case <-time.After(1 * time.Second):
		t.Fatal("friction IPC event was not delivered to socket")
	}
}

// TestFrictionIPC_CalledFromRecoveryPath is a source-level contract test.
// It verifies that executeWithFrictionRecovery calls sendFrictionEvent.
// This catches the exact regression from the frictionax migration where
// sendFrictionEvent was silently removed from the call site.
func TestFrictionIPC_CalledFromRecoveryPath(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("main.go")
	require.NoError(t, err, "should be able to read main.go")

	// check for an uncommented call (tab-indented, no leading //)
	found := false
	for _, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "sendFrictionEvent(result.Event)" {
			found = true
			break
		}
	}
	assert.True(t, found,
		"executeWithFrictionRecovery must call sendFrictionEvent(result.Event) — "+
			"removing this breaks CLI→daemon friction telemetry")
}

// TestSendFrictionEvent_Suppressed verifies every path that must NOT reach the
// daemon, using an injected socket so "suppressed" means "nothing arrived"
// rather than "the call returned".
//
// Failure prevented: two of these are telemetry opt-outs. Asserting only that
// the call does not panic passes against a build that ignores them entirely and
// sends the event anyway.
func TestSendFrictionEvent_Suppressed(t *testing.T) {
	sample := &friction.FrictionEvent{Kind: "unknown-command", Input: "ox bad"}

	tests := []struct {
		name  string
		env   map[string]string
		event *friction.FrictionEvent
	}{
		{
			// The nil guard is the first line of sendFrictionEventTo. Without
			// it a nil event either panics or serializes to a null payload and
			// is delivered as a real event.
			name:  "a nil event",
			event: nil,
		},
		{
			name:  "DO_NOT_TRACK=1",
			env:   map[string]string{"DO_NOT_TRACK": "1"},
			event: sample,
		},
		{
			name:  "SAGEOX_FRICTION=false",
			env:   map[string]string{"SAGEOX_FRICTION": "false"},
			event: sample,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("ox-fsup-%d.sock", time.Now().UnixNano()%100000))
			t.Cleanup(func() { os.Remove(socketPath) })

			listener, err := net.Listen("unix", socketPath)
			require.NoError(t, err)
			defer listener.Close()

			received := make(chan struct{}, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				received <- struct{}{}
			}()

			sendFrictionEventTo(tt.event, socketPath)

			select {
			case <-received:
				t.Fatal("event reached the daemon but delivery should have been suppressed")
			case <-time.After(50 * time.Millisecond):
				// good — nothing sent
			}
		})
	}
}
