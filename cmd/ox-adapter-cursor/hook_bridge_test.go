package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session/cursorpaths"
	"github.com/stretchr/testify/require"
)

const cursorBridgeConversation = "bfdd31f4-cd2e-4a81-b021-162a714a7097"

func cursorBridgeWorkspace(t *testing.T) (string, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	home, root := filepath.Join(base, "home"), filepath.Join(base, "worktree")
	require.NoError(t, os.MkdirAll(home, 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "config.json"), []byte(`{"repo_id":"fixture"}`), 0600))
	return home, root
}

func cursorBridgePayload(t *testing.T, event, root string) map[string]any {
	t.Helper()
	return map[string]any{
		"conversation_id": cursorBridgeConversation,
		"session_id":      cursorBridgeConversation,
		"generation_id":   "6f9a878c-99c9-4f24-a7e7-b8f7f987b93c",
		"hook_event_name": event, "workspace_roots": []string{root},
		"transcript_path": nil, "future_field": map[string]any{"preserve": true},
	}
}

func cursorBridgeJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}

// Only the three proven context channels may expose child stdout. Prompt and
// stop output must not start a new loop or turn into an invented context API.
func TestCursorBridgeNativeEnvelopesAndNormalizedInput(t *testing.T) {
	home, root := cursorBridgeWorkspace(t)
	for _, event := range cursorHookEvents {
		t.Run(event, func(t *testing.T) {
			input := cursorBridgePayload(t, event, root)
			delete(input, "session_id")
			input["session_file_hint"] = "/private/do-not-forward"
			var stdout, stderr bytes.Buffer
			calls := 0
			runner := func(ctx context.Context, executable, cwd, actualEvent string, raw []byte) ([]byte, error) {
				calls++
				require.Equal(t, "/fixture/ox", executable)
				require.Equal(t, root, cwd)
				require.Equal(t, event, actualEvent)
				deadline, ok := ctx.Deadline()
				require.True(t, ok)
				require.LessOrEqual(t, time.Until(deadline), cursorHookChildLimit)
				var forwarded map[string]any
				require.NoError(t, json.Unmarshal(raw, &forwarded))
				require.Equal(t, cursorBridgeConversation, forwarded["session_id"])
				require.Equal(t, root, forwarded["cwd"])
				require.Equal(t, input["future_field"], forwarded["future_field"])
				require.NotContains(t, forwarded, "session_file_hint")
				return []byte("MODEL_CONTEXT\n"), nil
			}
			err := runCursorHook(event, bytes.NewReader(cursorBridgeJSON(t, input)), &stdout, &stderr, home, root,
				func() (string, error) { return "/fixture/ox", nil }, runner)
			require.NoError(t, err)
			require.Equal(t, 1, calls)
			require.Empty(t, stderr.String())
			var response map[string]any
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &response))
			switch event {
			case "sessionStart", "postToolUse", "postToolUseFailure":
				require.Equal(t, map[string]any{"additional_context": "MODEL_CONTEXT\n"}, response)
			case "beforeSubmitPrompt":
				require.Equal(t, map[string]any{"continue": true}, response)
			default:
				require.Empty(t, response)
			}
			require.NotContains(t, response, "followup_message")
		})
	}
}

// Rebinding only identity/workspace paths keeps the real hook envelope intact.
func TestCursorBridgeAcceptsReboundCapturedHooks(t *testing.T) {
	home, root := cursorBridgeWorkspace(t)
	files, err := filepath.Glob(filepath.Join("testdata", "desktop", "hooks", "*.stdin.json"))
	require.NoError(t, err)
	accepted := 0
	for _, path := range files {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(data, &payload))
		event, ok := payload["hook_event_name"].(string)
		require.True(t, ok)
		supported := false
		for _, candidate := range cursorHookEvents {
			supported = supported || candidate == event
		}
		if !supported {
			continue
		}
		payload["conversation_id"], payload["session_id"] = cursorBridgeConversation, cursorBridgeConversation
		payload["workspace_roots"] = []string{root}
		if payload["transcript_path"] != nil {
			source, err := cursorpaths.SessionPath(home, root, cursorBridgeConversation)
			require.NoError(t, err)
			payload["transcript_path"] = source
		}
		normalized, actualRoot, err := normalizeCursorHook(event, cursorBridgeJSON(t, payload), home, root)
		require.NoError(t, err, filepath.Base(path))
		require.Equal(t, root, actualRoot)
		require.True(t, json.Valid(normalized))
		accepted++
	}
	require.Greater(t, accepted, 15)
}

// Untrusted identity/path data must be refused before any child executes, and
// diagnostics must not echo the rejected payload or native conversation ID.
func TestCursorBridgeRejectsUnsafeInputsWithoutBlockingCursor(t *testing.T) {
	home, root := cursorBridgeWorkspace(t)
	cases := map[string]func(map[string]any){
		"missing identity":       func(p map[string]any) { delete(p, "conversation_id") },
		"conflicting identity":   func(p map[string]any) { p["session_id"] = "another-private-conversation" },
		"generation substituted": func(p map[string]any) { p["conversation_id"] = "../private" },
		"event conflict":         func(p map[string]any) { p["hook_event_name"] = "stop" },
		"multiroot":              func(p map[string]any) { p["workspace_roots"] = []string{root, root} },
		"wrong root":             func(p map[string]any) { p["workspace_roots"] = []string{home} },
		"wrong source":           func(p map[string]any) { p["transcript_path"] = "/private/secret.jsonl" },
		"wrong source type":      func(p map[string]any) { p["transcript_path"] = []string{"secret"} },
		"cloud":                  func(p map[string]any) { p["is_background_agent"] = true },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			payload := cursorBridgePayload(t, "beforeSubmitPrompt", root)
			mutate(payload)
			var stdout, stderr bytes.Buffer
			find := func() (string, error) { t.Fatal("refused input reached executable lookup"); return "", nil }
			err := runCursorHook("beforeSubmitPrompt", bytes.NewReader(cursorBridgeJSON(t, payload)), &stdout, &stderr, home, root, find, nil)
			require.NoError(t, err)
			require.JSONEq(t, `{"continue":true}`, stdout.String())
			require.NotEmpty(t, stderr.String())
			require.NotContains(t, stderr.String(), "private")
			require.NotContains(t, stderr.String(), cursorBridgeConversation)
		})
	}
	for _, data := range [][]byte{[]byte("null"), []byte("{} {}"), bytes.Repeat([]byte("x"), cursorHookInputLimit+1)} {
		var stdout, stderr bytes.Buffer
		err := runCursorHook("sessionStart", bytes.NewReader(data), &stdout, &stderr, home, root, nil, nil)
		require.NoError(t, err)
		require.JSONEq(t, `{}`, stdout.String())
		require.NotEmpty(t, stderr.String())
	}
}

func TestCursorBridgeChildFailureNeverBecomesContext(t *testing.T) {
	home, root := cursorBridgeWorkspace(t)
	for _, childErr := range []error{errors.New("private stderr"), context.DeadlineExceeded, errCursorHookOutputLimit} {
		var stdout, stderr bytes.Buffer
		run := func(context.Context, string, string, string, []byte) ([]byte, error) {
			return []byte("partial private output"), childErr
		}
		err := runCursorHook("sessionStart", bytes.NewReader(cursorBridgeJSON(t, cursorBridgePayload(t, "sessionStart", root))), &stdout, &stderr, home, root,
			func() (string, error) { return "/fixture/ox", nil }, run)
		require.NoError(t, err)
		require.JSONEq(t, `{}`, stdout.String())
		require.NotContains(t, stdout.String()+stderr.String(), "private")
	}
}

func TestCursorBridgePrefersRunnableSibling(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable mode checks qualify macOS")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	adapter := filepath.Join(dir, "ox-adapter-cursor")
	ox := filepath.Join(dir, "ox")
	require.NoError(t, os.WriteFile(adapter, []byte("adapter"), 0700))
	require.NoError(t, os.WriteFile(ox, []byte("ox"), 0700))
	found, err := findCursorOxFrom(adapter, func(string) (string, error) { t.Fatal("sibling must win"); return "", nil })
	require.NoError(t, err)
	require.Equal(t, ox, found)
	require.NoError(t, os.Chmod(ox, 0600))
	_, err = findCursorOxFrom(adapter, func(string) (string, error) { return "./ox", nil })
	require.Error(t, err)
	fallback := filepath.Join(t.TempDir(), "ox")
	require.NoError(t, os.WriteFile(fallback, []byte("ox"), 0700))
	found, err = findCursorOxFrom(adapter, func(string) (string, error) { return fallback, nil })
	require.NoError(t, err)
	require.Equal(t, fallback, found)
}

// Real child processes exercise argv/stdin/env separation, stderr exclusion,
// overflow cancellation and deadlines rather than only stubbing a runner.
func TestCursorBridgeRunsBoundedChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX subprocess fixture; Windows lifecycle is unqualified")
	}
	root := t.TempDir()
	child := filepath.Join(root, "ox with spaces ' quote")
	require.NoError(t, os.WriteFile(child, []byte("#!/bin/sh\n[ \"$1 $2 $3\" = 'agent hook sessionStart' ] || exit 3\n[ \"$AGENT_ENV\" = cursor ] || exit 4\nprintf 'private stderr' >&2\ncat\n"), 0700))
	// Leave process startup headroom when the race suite runs concurrently.
	// The short deadline below independently verifies cancellation latency.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := runCursorOx(ctx, child, root, "sessionStart", []byte("$(must stay data) `and this`"))
	require.NoError(t, err)
	require.Equal(t, "$(must stay data) `and this`", string(output))
	for _, redirect := range []string{"", " >&2"} {
		require.NoError(t, os.WriteFile(child, []byte("#!/bin/sh\nhead -c 1100000 /dev/zero"+redirect+"\n"), 0700))
		_, err = runCursorOx(ctx, child, root, "sessionStart", nil)
		require.ErrorIs(t, err, errCursorHookOutputLimit)
	}
	require.NoError(t, os.WriteFile(child, []byte("#!/bin/sh\nsleep 30 &\nwait\n"), 0700))
	short, stop := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer stop()
	started := time.Now()
	_, err = runCursorOx(short, child, root, "sessionStart", nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), time.Second)
}

func TestCursorBridgePropagatesResponseWriteFailure(t *testing.T) {
	err := emitCursorHookResponse("stop", nil, "", cursorFailingWriter{}, io.Discard)
	require.Error(t, err)
}

type cursorFailingWriter struct{}

func (cursorFailingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestCursorHookOutputBufferRemainsBounded(t *testing.T) {
	canceled := false
	b := &cursorHookBuffer{limit: 4, cancel: func() { canceled = true }}
	n, err := b.Write([]byte(strings.Repeat("x", 100)))
	require.NoError(t, err)
	require.Equal(t, 100, n)
	require.Equal(t, 4, b.Len())
	require.True(t, canceled)
	require.True(t, b.overflow)
}
