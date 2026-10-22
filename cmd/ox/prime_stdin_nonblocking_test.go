package main

import (
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func swapStdinForProbeTest(t *testing.T, r *os.File) {
	t.Helper()
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig })
}

// Failure prevented: a bare `ox agent prime` run by an AI coworker whose stdin
// is a pipe or socket that never closes (a harness's background shell) blocked
// until EOF, which never came. The hook-payload probe must give up and let
// prime run without a payload.
func TestReadAgentHookInput_NeverWaitsForAnOpenStdin(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close(); _ = r.Close() }) // the writer stays open during the call
	swapStdinForProbeTest(t, r)

	done := make(chan *AgentHookInput, 1)
	go func() { done <- ReadAgentHookInput() }()

	select {
	case got := <-done:
		assert.Nil(t, got, "no payload arrived, so there is no hook input")
	case <-time.After(10 * time.Second):
		t.Fatal("ReadAgentHookInput blocked on a stdin that never reaches EOF")
	}
}

// Negative control: a real hook delivers its payload and closes stdin; that
// must still be read, or the guard above would also disable hooks.
func TestReadAgentHookInput_StillReadsAClosedPayload(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })
	_, err = io.WriteString(w, `{"session_id":"s-probe","hook_event_name":"SessionStart","source":"startup"}`)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	swapStdinForProbeTest(t, r)

	got := ReadAgentHookInput()

	require.NotNil(t, got)
	assert.Equal(t, "s-probe", got.SessionID)
}
