package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/selfexec"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
)

// prepareCursorHookBoundary runs before prime, including on the non-deliverable
// pre-prompt channel. Sampling and marker selection are one transaction, so an
// overlapping hook cannot replace the first prompt with a later exported EOF.
func prepareCursorHookBoundary(input *cursorNativeInput, event string) (*SessionMarker, error) {
	if event == "beforeSubmitPrompt" && input.GenerationID == "" {
		return nil, fmt.Errorf("boundary-unavailable: Cursor prompt generation is required")
	}
	return UpdateSessionMarker(input.ConversationID, func(marker *SessionMarker) error {
		previous := marker.CursorSourceBoundary
		if previous != nil && (previous.WorkspacePath != input.WorkspacePath || previous.SourcePath != input.SourcePath) {
			return fmt.Errorf("workspace-mismatch: Cursor conversation is bound to another workspace")
		}
		active := false
		if marker.AgentID != "" {
			state, err := session.LoadRecordingStateForAgent(input.WorkspacePath, marker.AgentID)
			if err != nil {
				return err
			}
			if state != nil && (state.AdapterName != "cursor" || state.AgentSessionID != input.ConversationID) {
				return fmt.Errorf("identity-conflict: Cursor hook does not own this recording")
			}
			active = state != nil
			if active && event == "beforeSubmitPrompt" {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				if err := session.RecordCursorPrompt(context.Background(), input.WorkspacePath, state, home, input.GenerationID); err != nil && !errors.Is(err, session.ErrNotRecording) {
					return err
				}
			}
		}
		if previous != nil && (active || event == "sessionStart" || previous.GenerationID == input.GenerationID || previous.GenerationID == "") {
			if previous.GenerationID == "" && event == "beforeSubmitPrompt" {
				previous.GenerationID = input.GenerationID
			}
			return nil
		}
		boundary, err := cursorBoundaryForHook(input, event)
		if err != nil {
			return err
		}
		marker.CursorSourceBoundary = &boundary
		return nil
	})
}

func runCursorAgentHook(event string, input *agentx.HookInput, projectRoot string) error {
	return runCursorAgentHookWithPrime(event, input, projectRoot, runCursorPrimeForHook)
}

func runCursorAgentHookWithPrime(event string, input *agentx.HookInput, projectRoot string, primeHook func(string, *HookContext) error) error {
	phases := map[string]string{
		"sessionStart": phaseStart, "beforeSubmitPrompt": phasePrompt,
		"postToolUse": phaseAfterTool, "postToolUseFailure": phaseAfterTool,
		"afterAgentResponse": phaseAfterTool, "stop": phaseStop,
		"sessionEnd": phaseEnd, "preCompact": phaseCompact,
	}
	phase, ok := phases[event]
	if !ok {
		return fmt.Errorf("unsupported-scope: unsupported Cursor hook")
	}
	if input == nil || input.HookEventName != event {
		return fmt.Errorf("identity-conflict: Cursor hook event does not match its payload")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("source-unreadable: Cursor home directory is unavailable")
	}
	native, err := normalizeCursorHookInput(input.RawBytes, projectRoot, home)
	if err != nil {
		return err
	}
	input.SessionID, input.RawBytes = native.ConversationID, native.NormalizedRaw
	marker, err := ReadSessionMarker(native.ConversationID)
	if err != nil {
		return err
	}
	if event == "sessionStart" || event == "beforeSubmitPrompt" {
		marker, err = prepareCursorHookBoundary(native, event)
		if err != nil {
			return err
		}
	}
	hook := &HookContext{Phase: phase, AgentType: "cursor", Input: input, Marker: marker, ProjectRoot: native.WorkspacePath}
	if event == "beforeSubmitPrompt" {
		// This native channel discards model context. It must not invoke prime
		// or advance any whisper/task delivery cursor.
		if marker.IsPrimed() {
			startSessionRecordingIfConfigured(hook)
			touchCursorHookRecording(projectRoot, marker.AgentID, "boundary-saved")
		}
		return nil
	}
	deliverable := event == "sessionStart" || event == "postToolUse" || event == "postToolUseFailure"
	didPrime := false
	if deliverable && !marker.IsPrimed() {
		// Coalesce simultaneous startup/tool hooks. A failed/timed-out prime
		// leaves the source boundary intact for the next deliverable event.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := fileutil.WithFileLock(ctx, markerPath(native.ConversationID)+".prime", func() error {
			current, err := ReadSessionMarker(native.ConversationID)
			if err != nil || current.IsPrimed() {
				hook.Marker = current
				return err
			}
			hook.Marker = current
			id := ""
			if current != nil {
				id = current.AgentID
			}
			if err := primeHook(id, hook); err != nil {
				return err
			}
			didPrime = true
			hook.Marker, err = ReadSessionMarker(native.ConversationID)
			return err
		})
		if err != nil {
			return err
		}
	}
	if didPrime {
		// Prime already starts configured recording and wakes its capture owner.
		// Return its context immediately: another source read or marker-lock
		// wait here could exhaust the native deadline after prime was marked
		// delivered, causing Cursor to discard the entire context envelope.
		return nil
	}
	if hook.Marker == nil || hook.Marker.AgentID == "" {
		return nil
	}
	agentID := hook.Marker.AgentID
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil {
		return err
	}
	if state != nil && (state.AdapterName != "cursor" || state.AgentSessionID != native.ConversationID) {
		return fmt.Errorf("identity-conflict: Cursor hook does not own this recording")
	}
	Heartbeat(projectRoot, nil, agentID)
	if deliverable {
		startSessionRecordingIfConfigured(hook)
		emitSuspendedNudge(os.Stdout, projectRoot, agentID)
		emitWhispers(os.Stdout, agentID)
		emitAgentTasks(os.Stdout, projectRoot, agentID, "cursor")
	}
	if event == "sessionEnd" {
		// Do not enqueue an incomplete raw file directly for publication. The
		// daemon's recovery pass owns final drain, masking and finalization.
		now := time.Now().UTC()
		if err := session.UpdateRecordingStateForAgent(projectRoot, agentID, func(state *session.RecordingState) {
			if state.StoppedAt == nil {
				state.StoppedAt = &now
			}
			state.CursorFinalDrainPending = true
		}); err != nil && !errors.Is(err, session.ErrNotRecording) {
			return err
		}
	}
	status := "ok"
	if err := requestCursorCapture(projectRoot, agentID, home); err != nil {
		var lockTimeout *fileutil.ErrLockTimeout
		switch {
		case errors.As(err, &lockTimeout):
			status = "capture-owner-active"
		case errors.Is(err, session.ErrCursorSourcePending):
			status = "source-pending"
		case errors.Is(err, session.ErrCursorSourceChanged):
			status = "source-changed"
		case errors.Is(err, session.ErrNotRecording):
			status = "stopped"
		default:
			status = "capture-deferred"
		}
		slog.Debug("Cursor hook capture deferred", "error", err)
	}
	touchCursorHookRecording(projectRoot, agentID, status)
	if event == "stop" {
		maybePublishSessionDraft(hook)
	}
	return nil
}

func touchCursorHookRecording(projectRoot, agentID, status string) {
	_ = session.UpdateRecordingStateForAgent(projectRoot, agentID, func(state *session.RecordingState) {
		now := time.Now().UTC()
		state.LastHookAt = &now
		state.HookInvocations++
		state.LastHookStatus = status
	})
}

// runCursorPrimeForHook commits primed status only after the bounded child
// succeeded and its complete context reached hook stdout. The child preserves
// its assigned identity with PrimedAt unset, so timeout/overflow can retry on a
// later deliverable event without allocating another AI coworker.
func runCursorPrimeForHook(agentID string, hook *HookContext) error {
	path, err := selfexec.Path()
	if errors.Is(err, selfexec.ErrUnderTest) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("adapter-missing: ox executable is unavailable")
	}
	return runCursorPrimeExecutableForHook(path, agentID, hook)
}

// Keep executable resolution outside the subprocess transaction so tests can
// exercise delivery with an isolated child without re-executing the test binary.
func runCursorPrimeExecutableForHook(path, agentID string, hook *HookContext) error {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "agent", "prime", "--agent", "cursor")
	cmd.Dir = hook.ProjectRoot
	cmd.Env = append(buildPrimeEnv(agentID), "AGENT_ENV=cursor")
	cmd.Stdin = bytes.NewReader(hook.Input.RawBytes)
	var output cursorPrimeBuffer
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 250 * time.Millisecond
	configureCursorPrimeProcess(cmd)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("hook-prime-failed: Cursor context startup will retry")
	}
	if _, err := os.Stdout.Write(output.data.Bytes()); err != nil {
		return err
	}
	_, err := UpdateSessionMarker(hook.Input.SessionID, func(marker *SessionMarker) error {
		if marker.AgentID == "" {
			return fmt.Errorf("missing-native-identity: Cursor prime did not assign an AI coworker")
		}
		marker.PrimedAt = time.Now().UTC()
		marker.ParentPID = 0
		return nil
	})
	return err
}

// Composition avoids bytes.Buffer's promoted ReaderFrom bypassing this cap
// when os/exec streams child stdout via io.Copy.
type cursorPrimeBuffer struct{ data bytes.Buffer }

func (b *cursorPrimeBuffer) Write(p []byte) (int, error) {
	if len(p) > (1<<20)-b.data.Len() {
		return 0, fmt.Errorf("hook-output-limit")
	}
	return b.data.Write(p)
}

// requestCursorCapture wakes a daemon owner, then attempts a short fallback.
// IPC success or failure says nothing about ownership: the raw lock decides.
func requestCursorCapture(projectRoot, agentID, home string) error {
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil || state == nil {
		return err
	}
	if state.AdapterName != "cursor" {
		return fmt.Errorf("identity-conflict: hook recording is not a Cursor session")
	}
	sendSessionWatchStart(state, projectRoot)
	adapter, err := adapters.GetAdapter("cursor")
	if err != nil {
		return fmt.Errorf("adapter-missing: Cursor adapter is unavailable")
	}
	reader, ok := adapter.(adapters.IncrementalReader)
	if !ok {
		return fmt.Errorf("adapter-missing: Cursor incremental reader is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return fileutil.WithFileLockTimeout(ctx, filepath.Join(state.SessionPath, "raw.jsonl"), 50*time.Millisecond, func() error {
		_, err := session.DrainCursorSource(ctx, projectRoot, state.SessionPath, home, reader, state.CursorFinalDrainPending)
		return err
	})
}
