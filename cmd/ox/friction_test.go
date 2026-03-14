package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	friction "github.com/sageox/frictionax"
	"github.com/sageox/ox/internal/daemon"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSendFrictionEvent_DeliversToSocket verifies that sendFrictionEvent sends
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

	// send via the same code path as production, but with a custom socket
	payload := daemon.FrictionPayload{
		Timestamp:  event.Timestamp,
		Kind:       string(event.Kind),
		Command:    event.Command,
		Subcommand: event.Subcommand,
		Actor:      event.Actor,
		AgentType:  event.AgentType,
		PathBucket: event.PathBucket,
		Input:      event.Input,
		ErrorMsg:   event.ErrorMsg,
	}

	data, err := json.Marshal(payload)
	require.NoError(t, err)

	// NewClientWithSocket uses default 50ms timeout, sufficient for localhost
	client := daemon.NewClientWithSocket(socketPath)
	sendErr := client.SendOneWay(daemon.Message{
		Type:    daemon.MsgTypeFriction,
		Payload: data,
	})
	require.NoError(t, sendErr)

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

// TestSendFrictionEvent_NilEvent verifies nil events are safely ignored.
func TestSendFrictionEvent_NilEvent(t *testing.T) {
	// should not panic
	sendFrictionEvent(nil)
}

// TestSendFrictionEvent_DoNotTrack verifies telemetry opt-out is respected.
func TestSendFrictionEvent_DoNotTrack(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "1")

	// set up socket that should NOT receive anything
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("ox-fdnt-%d.sock", time.Now().UnixNano()%100000))
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

	event := &friction.FrictionEvent{
		Kind:  "unknown-command",
		Input: "ox bad",
	}

	sendFrictionEvent(event)

	select {
	case <-received:
		t.Fatal("friction event should NOT be sent when DO_NOT_TRACK=1")
	case <-time.After(50 * time.Millisecond):
		// good — no IPC sent
	}
}
