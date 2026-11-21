package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/daemon"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// Failure prevented (N3): a pending teammate note holding a terminal escape
// ("\x1b[2J\x1b[H" clears and homes the screen) repainting the terminal when its
// recipient runs `ox agent <id> whisper history`.
func TestWhisperHistoryPrintsHostileNoteTextSafely(t *testing.T) {
	codexActivityEnv(t, nil)
	sock := daemon.SocketPath()
	require.NoError(t, os.MkdirAll(filepath.Dir(sock), 0o700))
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	hostile := whisperstore.WhisperEntry{
		ID: "tcm_1", Scope: "ledger", Type: whisperstore.WhisperTrigger, Source: whisperstore.SourceTeammateComment,
		Topic: "teammate-comment", Content: "hello\x1b[2J\x1b[H\x1b]0;owned\x07 world", Importance: whisperstore.ImportanceNormal,
		CreatedAt: time.Now(), AgentID: "OxHook1",
	}
	delivered := hostile
	delivered.ID, delivered.CreatedAt = "tcm_0", time.Now().Add(-time.Hour)
	delivered.Content = "seen\x1b[2Jbefore"
	data, err := json.Marshal(daemon.WhisperHistoryResponse{
		Entries: []whisperstore.WhisperEntry{hostile, delivered}, HasCursor: true, Cursor: time.Now().Add(-time.Minute),
	})
	require.NoError(t, err)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				_, _ = bufio.NewReader(conn).ReadBytes('\n')
				out, _ := json.Marshal(daemon.Response{Success: true, Data: data})
				_, _ = conn.Write(append(out, '\n'))
			}()
		}
	}()

	var out bytes.Buffer
	require.NoError(t, runAgentWhisperHistory(&out, &agentinstance.Instance{AgentID: "OxHook1"}))
	assert.NotContains(t, out.String(), "\x1b", "an escape sequence reached the terminal")
	assert.NotContains(t, out.String(), "\x07")
	assert.Contains(t, out.String(), "seenbefore", "a delivered note is sanitized too")
	assert.True(t, strings.Contains(out.String(), "hello") && strings.Contains(out.String(), "world"), "the readable text must survive: %q", out.String())
}
