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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/proc"
	"github.com/sageox/ox/internal/selfexec"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/pipeline"
	"github.com/spf13/cobra"
)

var (
	// errSessionStopNotLaunched: the stop never ran, so the recording is as
	// handleEnd found it and the daemon handoff can still take it.
	errSessionStopNotLaunched = errors.New("session stop could not be launched")

	// errSessionStopFailed: the stop ran and exited non-zero. If it failed
	// before marking the recording stopped, the recording is untouched.
	errSessionStopFailed = errors.New("session stop failed")

	// errSessionStopStillRunning: the stop outlived sessionEndStopWait. It
	// keeps uploading in its own process session; handleEnd must not touch
	// the recording underneath it.
	errSessionStopStillRunning = errors.New("session stop still running")

	// errSessionUploadFailed: the stop finished but the upload did not. The
	// stop keeps the session in its cache and flags it for `ox doctor`.
	errSessionUploadFailed = errors.New("session upload failed")
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
	return runSessionStopAtEnd(projectRoot, agentID)
}

// stopLeftRecordingUntouched reports whether a failed SessionEnd stop never
// got as far as claiming the recording, so the daemon handoff may still take
// it. A stop that marked the recording owns it: still uploading, or keeping
// it for `ox doctor` after a failed upload. Handing that one over would
// stamp a second stop onto a session already written.
func stopLeftRecordingUntouched(err error, projectRoot, agentID string) bool {
	if errors.Is(err, errSessionStopNotLaunched) {
		return true
	}
	return errors.Is(err, errSessionStopFailed) && !session.HasExplicitStop(projectRoot, agentID)
}

// runAgentSessionStopCommand is `ox agent <id> session stop`. With
// --agent-ended (sessionStopCommand sets it), a completed stop also clears
// its explicit-stop signal: the agent ended and the coworker did not ask to
// stop recording, so a later prime for this agent records again. The stop
// clears it itself because it may outlive the hook that started it.
func runAgentSessionStopCommand(cmd *cobra.Command, inst *agentinstance.Instance) error {
	if err := runAgentSessionStop(inst); err != nil {
		return err
	}
	if agentEnded, _ := cmd.Flags().GetBool("agent-ended"); agentEnded {
		if projectRoot, err := findProjectRoot(); err == nil {
			session.ConsumeExplicitStop(projectRoot, inst.AgentID)
		}
	}
	return nil
}

// runSessionStopProcess runs `ox agent <id> session stop --agent-ended`,
// waits up to sessionEndStopWait, and reads whether the upload landed. The
// child inherits the environment, so it sees the same venue (AMP_ORB, ...)
// and also refuses to defer to the daemon.
func runSessionStopProcess(projectRoot, agentID string) error {
	oxPath, err := selfexec.Path()
	if err != nil {
		return fmt.Errorf("%w: %w", errSessionStopNotLaunched, err)
	}
	return runSessionStopWith(oxPath, projectRoot, agentID, sessionEndStopWait)
}

// runSessionStopWith runs the stop with the given ox executable.
func runSessionStopWith(oxPath, projectRoot, agentID string, wait time.Duration) error {
	// a file, not a pipe: the child may keep writing after this hook exits
	out, err := os.CreateTemp("", "ox-session-stop-*.json")
	if err != nil {
		return fmt.Errorf("%w: %w", errSessionStopNotLaunched, err)
	}
	defer func() {
		_ = out.Close()
		_ = os.Remove(out.Name())
	}()
	cmd := sessionStopCommand(oxPath, projectRoot, agentID)
	cmd.Stdout = out
	if err := waitForSessionStop(cmd, wait); err != nil {
		return err
	}
	printed, err := os.ReadFile(out.Name())
	if err != nil {
		return fmt.Errorf("read session stop output: %w", err)
	}
	return sessionStopOutcome(printed)
}

// waitForSessionStop starts the stop child and waits up to wait for it. On
// timeout the child keeps running: it is detached, and owns the recording.
func waitForSessionStop(cmd *exec.Cmd, wait time.Duration) error {
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: %w", errSessionStopNotLaunched, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%w: %w", errSessionStopFailed, err)
		}
		return nil
	case <-time.After(wait):
		return errSessionStopStillRunning
	}
}

// sessionStopOutcome reads the JSON `session stop` printed. Stop exits 0
// when the upload fails (it keeps the session and flags it for `ox doctor`)
// and says so in upload_warning. Whispers may be printed ahead of the JSON,
// which always starts on a line of its own.
func sessionStopOutcome(printed []byte) error {
	start := bytes.LastIndex(printed, []byte("\n{"))
	if start < 0 {
		start = bytes.IndexByte(printed, '{')
	}
	if start < 0 {
		return fmt.Errorf("session stop printed no result")
	}
	var result pipeline.StopOutput
	if err := json.NewDecoder(bytes.NewReader(printed[start:])).Decode(&result); err != nil {
		return fmt.Errorf("parse session stop result: %w", err)
	}
	if result.UploadWarning != "" {
		return fmt.Errorf("%w: %s", errSessionUploadFailed, result.UploadWarning)
	}
	return nil
}

// sessionStopCommand builds the stop child. It runs in its own process
// session, so an agent that kills this hook at its own timeout (Claude Code,
// Codex) does not kill the upload with it. It is told the agent ended, so
// it does not suppress the agent's next recording.
func sessionStopCommand(oxPath, projectRoot, agentID string) *exec.Cmd {
	cmd := exec.Command(oxPath, "agent", agentID, "session", "stop", "--agent-ended")
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
