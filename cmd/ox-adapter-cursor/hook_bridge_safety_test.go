package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCursorBridgeRejectsUnsupportedEventsAndUnavailableWorkspace(t *testing.T) {
	for _, failure := range []string{"unsupported event", "relative workspace", "missing workspace"} {
		t.Run(failure, func(t *testing.T) {
			home, root := cursorBridgeWorkspace(t)
			event := "beforeSubmitPrompt"
			payload := cursorBridgePayload(t, event, root)
			switch failure {
			case "unsupported event":
				event = "future-event"
				payload["hook_event_name"] = event
			case "relative workspace":
				payload["workspace_roots"] = []string{"relative-private-workspace"}
			default:
				payload["workspace_roots"] = []string{filepath.Join(root, "missing")}
			}
			var stdout, stderr bytes.Buffer
			find := func() (string, error) { t.Fatal("invalid hook must not start executable lookup"); return "", nil }
			require.NoError(t, runCursorHook(event, bytes.NewReader(cursorBridgeJSON(t, payload)), &stdout, &stderr, home, root, find, nil))
			if event == "beforeSubmitPrompt" {
				require.JSONEq(t, `{"continue":true}`, stdout.String())
			} else {
				require.JSONEq(t, `{}`, stdout.String())
			}
			require.NotEmpty(t, stderr.String())
			require.NotContains(t, stderr.String(), root)
			require.NotContains(t, stderr.String(), "private-workspace")
		})
	}
}

func TestCursorBridgeMissingOxKeepsPromptUsableAndLookupPrivate(t *testing.T) {
	home, root := cursorBridgeWorkspace(t)
	payload := cursorBridgeJSON(t, cursorBridgePayload(t, "beforeSubmitPrompt", root))
	var stdout, stderr bytes.Buffer
	find := func() (string, error) { return "", errors.New("private executable path") }
	run := func(context.Context, string, string, string, []byte) ([]byte, error) {
		t.Fatal("missing executable must not reach child execution")
		return nil, nil
	}
	require.NoError(t, runCursorHook("beforeSubmitPrompt", bytes.NewReader(payload), &stdout, &stderr, home, root, find, run))
	require.JSONEq(t, `{"continue":true}`, stdout.String())
	require.Equal(t, "cursor-hook: ox-not-found\n", stderr.String())
}

func TestCursorBridgeBoundsPayloadAfterNativeNormalization(t *testing.T) {
	home, root := cursorBridgeWorkspace(t)
	payload := cursorBridgePayload(t, "beforeSubmitPrompt", root)
	payload["opaque"] = ""
	base := cursorBridgeJSON(t, payload)
	payload["opaque"] = strings.Repeat("x", cursorHookInputLimit-len(base))
	raw := cursorBridgeJSON(t, payload)
	require.Len(t, raw, cursorHookInputLimit, "native input fits before normalization adds the canonical cwd")
	var stdout, stderr bytes.Buffer
	find := func() (string, error) { t.Fatal("oversized normalized input must not execute"); return "", nil }
	require.NoError(t, runCursorHook("beforeSubmitPrompt", bytes.NewReader(raw), &stdout, &stderr, home, root, find, nil))
	require.JSONEq(t, `{"continue":true}`, stdout.String())
	require.Equal(t, "cursor-hook: hook-input-limit\n", stderr.String())
}

func TestCursorBridgeDiscardsOutputFromFailedChild(t *testing.T) {
	if testing.Short() {
		t.Skip("exercises subprocess or multi-step recording lifecycle")
	}
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell executable")
	}
	root := t.TempDir()
	child := filepath.Join(root, "ox")
	require.NoError(t, os.WriteFile(child, []byte("#!/bin/sh\nprintf private-partial-output\nexit 7\n"), 0o700))
	output, err := runCursorOx(context.Background(), child, root, "sessionStart", []byte("{}"))
	require.Error(t, err)
	require.Empty(t, output, "a failed prime must not deliver partial context")
}
