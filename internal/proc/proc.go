// Package proc provides process tree utilities for identifying long-lived
// ancestor processes (e.g., the agent binary that launched a hook subprocess chain).
//
// Problem: hooks run as: agent (e.g., claude) → bash → ox agent hook → ox agent prime.
// os.Getppid() in any hook subprocess returns the transient bash PID, which dies
// immediately. FindAgentAncestorPID walks the tree to find the actual agent process.
package proc

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/sageox/agentx"
)

// knownAgentBinaries returns the list of known long-lived AI agent process names,
// derived from agentx.SupportedAgents so we stay in sync as new agents are added.
// Process names on macOS/Linux are often truncated by the kernel (e.g., 15 chars on macOS).
func knownAgentBinaries() []string {
	names := make([]string, 0, len(agentx.SupportedAgents))
	for _, at := range agentx.SupportedAgents {
		if at != agentx.AgentTypeUnknown {
			names = append(names, string(at))
		}
	}
	return names
}

// FindAgentAncestorPID walks the process tree from the current process's parent
// upward, looking for the first ancestor whose name matches a known agent binary.
//
// This is necessary because hooks run inside a transient bash shell spawned by
// the agent, so os.Getppid() returns the bash PID (which dies), not the agent PID.
//
// If AGENT_ENV is set in the environment, its resolved binary name is searched first
// so we find the correct agent quickly without scanning all known names.
//
// Returns the matching ancestor PID, or os.Getppid() as fallback if no agent found.
func FindAgentAncestorPID() int {
	// If AGENT_ENV is set, resolve it to a binary name and use as a hint.
	// This avoids scanning all known agent names on every hook call.
	hint := ""
	if agentEnv := os.Getenv("AGENT_ENV"); agentEnv != "" {
		at := agentx.ResolveAgentENV(agentEnv)
		if at != agentx.AgentTypeUnknown {
			hint = string(at)
		}
	}
	return findAgentAncestorFrom(os.Getppid(), hint)
}

// findAgentAncestorFrom walks from startPID upward (max 10 levels) looking for a
// process whose name matches a known agent binary.
// hint, if non-empty, is checked first before scanning all known agent names.
func findAgentAncestorFrom(startPID int, hint string) int {
	pid := startPID
	fallback := startPID

	known := knownAgentBinaries()
	for range 10 {
		if pid <= 1 {
			break
		}
		name := processName(pid)
		if name != "" && matchesAgent(name, hint, known) {
			slog.Debug("proc: found agent ancestor", "pid", pid, "name", name, "provenance", "ancestor")
			return pid
		}
		ppid, err := parentPID(pid)
		if err != nil || ppid <= 1 || ppid == pid {
			break
		}
		pid = ppid
	}

	slog.Debug("proc: no agent ancestor found, using ppid fallback", "pid", fallback, "provenance", "fallback_ppid")
	return fallback
}

// matchesAgent returns true if name matches the hint (checked first) or any known agent.
func matchesAgent(name, hint string, known []string) bool {
	lower := strings.ToLower(name)
	if hint != "" && strings.HasPrefix(lower, hint) {
		return true
	}
	for _, agent := range known {
		if strings.HasPrefix(lower, agent) {
			return true
		}
	}
	return false
}

// IsAlive returns true if the process with the given PID is still running.
func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return isAliveProc(proc)
}

// Terminate asks the process with the given PID to shut down.
//
// On Unix this is SIGINT, giving the target a chance to close resources and
// release locks. Windows has no deliverable equivalent — os.Process.Signal
// supports only os.Kill there and returns syscall.EWINDOWS for os.Interrupt, so
// a caller that "sent an interrupt" on Windows silently did nothing — so the
// process is terminated outright. Callers that need a clean shutdown on Windows
// must arrange it another way (e.g. an RPC the target listens for).
func Terminate(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("terminate: invalid pid %d", pid)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("terminate: find process %d: %w", pid, err)
	}
	return terminateProc(proc)
}
