package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session/cursorpaths"
)

const (
	cursorHookInputLimit  = 1 << 20
	cursorHookOutputLimit = 1 << 20
	cursorHookErrorLimit  = 64 << 10
	cursorHookChildLimit  = 8 * time.Second
)

var errCursorHookOutputLimit = errors.New("hook-output-limit")

type cursorHookRunner func(context.Context, string, string, string, []byte) ([]byte, error)

func runHook(event string, stdin io.Reader, stdout, stderr io.Writer) error {
	home, homeErr := os.UserHomeDir()
	cwd, cwdErr := os.Getwd()
	if homeErr != nil || cwdErr != nil {
		return emitCursorHookResponse(event, nil, "workspace-unavailable", stdout, stderr)
	}
	return runCursorHook(event, stdin, stdout, stderr, home, cwd, findCursorOx, runCursorOx)
}

func runCursorHook(event string, stdin io.Reader, stdout, stderr io.Writer, home, cwd string, findOx func() (string, error), run cursorHookRunner) error {
	raw, err := io.ReadAll(io.LimitReader(stdin, cursorHookInputLimit+1))
	if err != nil || len(raw) > cursorHookInputLimit {
		return emitCursorHookResponse(event, nil, "hook-input-limit", stdout, stderr)
	}
	payload, root, err := normalizeCursorHook(event, raw, home, cwd)
	if err != nil {
		return emitCursorHookResponse(event, nil, err.Error(), stdout, stderr)
	}
	executable, err := findOx()
	if err != nil {
		return emitCursorHookResponse(event, nil, "ox-not-found", stdout, stderr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cursorHookChildLimit)
	defer cancel()
	output, err := run(ctx, executable, root, event, payload)
	if err != nil {
		code := "hook-child-failed"
		if errors.Is(err, errCursorHookOutputLimit) {
			code = "hook-output-limit"
		} else if errors.Is(err, context.DeadlineExceeded) {
			code = "hook-timeout"
		}
		return emitCursorHookResponse(event, nil, code, stdout, stderr)
	}
	return emitCursorHookResponse(event, output, "", stdout, stderr)
}

func normalizeCursorHook(event string, raw []byte, home, cwd string) ([]byte, string, error) {
	validEvent := false
	for _, candidate := range cursorHookEvents {
		validEvent = validEvent || candidate == event
	}
	if !validEvent {
		return nil, "", errors.New("unsupported-hook-event")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, "", errors.New("invalid-hook-input")
	}
	var input struct {
		ConversationID string   `json:"conversation_id"`
		SessionID      string   `json:"session_id"`
		GenerationID   string   `json:"generation_id"`
		HookEvent      string   `json:"hook_event_name"`
		WorkspaceRoots []string `json:"workspace_roots"`
		Source         *string  `json:"transcript_path"`
		Background     bool     `json:"is_background_agent"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, "", errors.New("invalid-hook-input")
	}
	if cursorpaths.ValidateConversationID(input.ConversationID) != nil {
		return nil, "", errors.New("missing-native-identity")
	}
	if input.SessionID != "" && input.SessionID != input.ConversationID {
		return nil, "", errors.New("identity-conflict")
	}
	if input.HookEvent != event {
		return nil, "", errors.New("hook-event-conflict")
	}
	if input.Background || len(input.WorkspaceRoots) != 1 {
		return nil, "", errors.New("unsupported-scope")
	}
	root, err := filepath.EvalSymlinks(input.WorkspaceRoots[0])
	if err != nil || !filepath.IsAbs(input.WorkspaceRoots[0]) {
		return nil, "", errors.New("workspace-mismatch")
	}
	working, err := filepath.EvalSymlinks(cwd)
	if err != nil || root != working || !config.IsInitialized(root) {
		return nil, "", errors.New("workspace-mismatch")
	}
	hint := ""
	if input.Source != nil {
		hint = *input.Source
	}
	validated, err := cursorpaths.ValidateSource(home, root, input.ConversationID, hint)
	if err != nil {
		return nil, "", errors.New("invalid-source-path")
	}
	fields["session_id"], _ = json.Marshal(input.ConversationID)
	fields["cwd"], _ = json.Marshal(root)
	// A caller cannot smuggle the host-only extension around native validation.
	delete(fields, "session_file_hint")
	if hint != "" {
		fields["session_file_hint"], _ = json.Marshal(validated)
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		return nil, "", errors.New("invalid-hook-input")
	}
	if len(normalized) > cursorHookInputLimit {
		return nil, "", errors.New("hook-input-limit")
	}
	return normalized, root, nil
}

func emitCursorHookResponse(event string, contextOutput []byte, diagnostic string, stdout, stderr io.Writer) error {
	response := map[string]any{}
	switch event {
	case "beforeSubmitPrompt":
		response["continue"] = true
	case "sessionStart", "postToolUse", "postToolUseFailure":
		if len(bytes.TrimSpace(contextOutput)) > 0 {
			response["additional_context"] = string(contextOutput)
		}
	}
	if diagnostic != "" {
		_, _ = fmt.Fprintf(stderr, "cursor-hook: %s\n", diagnostic)
	}
	return json.NewEncoder(stdout).Encode(response)
}

func findCursorOx() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return findCursorOxFrom(executable, exec.LookPath)
}

func findCursorOxFrom(executable string, lookup func(string) (string, error)) (string, error) {
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	sibling := filepath.Join(filepath.Dir(executable), "ox")
	if cursorRunnable(sibling) {
		return sibling, nil
	}
	found, err := lookup("ox")
	if err != nil || !filepath.IsAbs(found) || !cursorRunnable(found) {
		return "", errors.New("ox-not-found")
	}
	return found, nil
}

func cursorRunnable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0
}

type cursorHookBuffer struct {
	data     bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (w *cursorHookBuffer) Len() int { return w.data.Len() }

func (w *cursorHookBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := w.limit - w.Len()
	if n > remaining {
		_, _ = w.data.Write(p[:remaining])
		w.overflow = true
		w.cancel()
		return n, nil
	}
	return w.data.Write(p)
}

func runCursorOx(ctx context.Context, executable, cwd, event string, input []byte) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "agent", "hook", event)
	cmd.Dir = cwd
	cmd.Stdin = bytes.NewReader(input)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "AGENT_ENV=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "AGENT_ENV=cursor")
	out := &cursorHookBuffer{limit: cursorHookOutputLimit, cancel: cancel}
	errOut := &cursorHookBuffer{limit: cursorHookErrorLimit, cancel: cancel}
	cmd.Stdout, cmd.Stderr = out, errOut
	cmd.WaitDelay = 250 * time.Millisecond
	configureCursorHookProcess(cmd)
	defer cleanupCursorHookProcess(cmd)
	err := cmd.Run()
	if out.overflow || errOut.overflow {
		return nil, errCursorHookOutputLimit
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return out.data.Bytes(), nil
}
