package main

// session_end_sync.go: finishing a recording at SessionEnd on a machine that
// may not outlive the coding agent (sageox/ox#1314).
//
// handleEnd normally marks the recording stopped and hands it to the daemon.
// That handoff only helps when the daemon keeps running after the agent: an
// Amp orb pauses minutes after its agent goes idle (freezing the daemon with
// it), and Claude Code Cloud and Devin tear the machine down. There the hook
// runs the explicit `ox agent <id> session stop` instead, the one path that
// uploads before it returns, and needs neither the daemon nor an LLM CLI.

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/sageox/ox/internal/proc"
	"github.com/sageox/ox/internal/selfexec"
	"github.com/sageox/ox/internal/session"
)

var (
	// errSessionStopNotLaunched: the stop never ran, so the recording is as
	// handleEnd found it and the daemon handoff can still take it.
	errSessionStopNotLaunched = errors.New("session stop could not be launched")

	// errSessionStopStillRunning: the stop outlived sessionEndStopWait. It
	// keeps uploading in its own process session; handleEnd must not touch
	// the recording underneath it.
	errSessionStopStillRunning = errors.New("session stop still running")
)

// sessionEndStopWait bounds how long SessionEnd waits for the upload. The
// ox-bridge plugin waits a minute longer (STOP_TIMEOUT_MS), so it sees this
// hook return rather than abandoning it.
const sessionEndStopWait = 3 * time.Minute

// runSessionStopAtEnd is the process boundary, swapped by tests: selfexec
// refuses to re-exec a test binary.
var runSessionStopAtEnd = runSessionStopProcess

// finishRecordingAtEnd uploads the agent's recording before returning.
func finishRecordingAtEnd(projectRoot, agentID string) error {
	if err := runSessionStopAtEnd(projectRoot, agentID); err != nil {
		return err
	}
	// The agent ended; the coworker did not ask to stop recording. Drop the
	// explicit-stop signal `session stop` leaves behind, so a later prime for
	// this agent records again, as it does after the daemon handoff.
	session.ConsumeExplicitStop(projectRoot, agentID)
	return nil
}

// runSessionStopProcess runs `ox agent <id> session stop` and waits up to
// sessionEndStopWait. The child inherits the environment, so it sees the
// same venue (AMP_ORB, ...) and also refuses to defer to the daemon.
func runSessionStopProcess(projectRoot, agentID string) error {
	oxPath, err := selfexec.Path()
	if err != nil {
		return fmt.Errorf("%w: %w", errSessionStopNotLaunched, err)
	}
	cmd := sessionStopCommand(oxPath, projectRoot, agentID)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: %w", errSessionStopNotLaunched, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("session stop: %w", err)
		}
		return nil
	case <-time.After(sessionEndStopWait):
		return errSessionStopStillRunning
	}
}

// sessionStopCommand builds the stop child. It runs in its own process
// session, so an agent that kills this hook at its own timeout (Claude Code,
// Codex) does not kill the upload with it. Output goes nowhere: SessionEnd
// output reaches no model, and a pipe to a hook that has exited would stop
// the child on its next write.
func sessionStopCommand(oxPath, projectRoot, agentID string) *exec.Cmd {
	cmd := exec.Command(oxPath, "agent", agentID, "session", "stop")
	cmd.Dir = projectRoot
	proc.Detach(cmd)
	return cmd
}

// sessionEndAgentID names the agent whose recording SessionEnd finishes. The
// session marker, written at prime and keyed by the native session id, is the
// usual source. Amp has no hook stdin of its own: the ox-bridge plugin sends
// the thread URL it builds from Amp's base URL, which prime keyed the marker
// by when Amp exported it as AMP_THREAD_URL. When that lookup misses, match
// the live recording whose native session ids name the same thread.
func sessionEndAgentID(ctx *HookContext) string {
	if ctx.Marker != nil && ctx.Marker.AgentID != "" {
		return ctx.Marker.AgentID
	}
	if ctx.Input == nil || ctx.Input.SessionID == "" {
		return ""
	}
	want := nativeThreadKey(ctx.Input.SessionID)
	states, err := session.LoadAllRecordingStates(ctx.ProjectRoot)
	if err != nil {
		return ""
	}
	for _, s := range states {
		if s.StoppedAt != nil {
			continue
		}
		if nativeThreadKey(s.AgentSessionID) == want {
			return s.AgentID
		}
		for _, native := range s.NativeSessions {
			if nativeThreadKey(native.ID) == want {
				return s.AgentID
			}
		}
	}
	return ""
}

// nativeThreadKey reduces a native session id to what identifies the thread:
// an Amp thread URL (https://ampcode.com/threads/T-...) and its bare id
// compare equal, whatever host or trailing slash the URL carries.
func nativeThreadKey(id string) string {
	id = strings.TrimRight(strings.TrimSpace(id), "/")
	if i := strings.LastIndex(id, "/threads/"); i >= 0 {
		return id[i+len("/threads/"):]
	}
	return id
}
