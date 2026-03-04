package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/pkg/agentx"
)

// agentHookCmd handles lifecycle hooks from AI coworkers.
// Registered as a direct cobra subcommand (no agent ID required) because
// hooks fire before ox agent prime — no agent ID exists yet.
var agentHookCmd = &cobra.Command{
	Use:    "hook <event>",
	Short:  "Handle lifecycle hooks from AI coworkers",
	Hidden: true,
	Args:   cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAgentHook(args)
	},
}

// ReadHookInput reads hook input from stdin.
// Delegates to agentx.ReadHookInputFromStdin for the actual implementation.
// Kept as a package-level function for backward compatibility.
var ReadHookInput = agentx.ReadHookInputFromStdin

// Phase aliases for local use — canonical definitions live in pkg/agentx.
const (
	phaseStart      = string(agentx.PhaseStart)
	phaseEnd        = string(agentx.PhaseEnd)
	phaseBeforeTool = string(agentx.PhaseBeforeTool)
	phaseAfterTool  = string(agentx.PhaseAfterTool)
	phasePrompt     = string(agentx.PhasePrompt)
	phaseStop       = string(agentx.PhaseStop)
	phaseCompact    = string(agentx.PhaseCompact)
)

// activePhaseBehavior tracks which phases currently have behavior.
// Phases not in this set return immediately (fast-path noop).
var activePhaseBehavior = map[string]bool{
	phaseStart:   true,
	phaseCompact: true,
}

// HookContext carries everything a phase handler needs.
type HookContext struct {
	Phase       string              // resolved lifecycle phase
	AgentType   string              // from AGENT_ENV: "claude-code", "gemini", etc.
	Input       *agentx.HookInput   // parsed stdin JSON
	Marker      *SessionMarker      // nil if not yet primed
	ProjectRoot string              // git root with .sageox/
}

// runAgentHook is the entry point for `ox agent hook <event>`.
// It maps the agent's native event to a lifecycle phase and dispatches to the handler.
func runAgentHook(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: ox agent hook <event>")
	}
	eventName := args[0]

	// 1. fast check: is ox initialized?
	projectRoot, err := findProjectRoot()
	if err != nil {
		slog.Debug("hook: project not initialized", "event", eventName)
		return nil // silent noop
	}
	if !config.IsInitialized(projectRoot) {
		slog.Debug("hook: sageox not initialized", "event", eventName)
		return nil
	}

	// 2. read AGENT_ENV
	agentType := os.Getenv("AGENT_ENV")
	if agentType == "" {
		agentType = "claude-code" // default for backward compatibility
	}

	// 3. read stdin
	input := ReadHookInput()

	// 4. map event to phase
	phase := resolvePhase(agentType, eventName)
	if phase == "" {
		slog.Debug("hook: unknown event", "agent", agentType, "event", eventName)
		return nil // silent noop
	}

	// 5. fast-path: phase has no behavior?
	if !activePhaseBehavior[phase] {
		slog.Debug("hook: noop phase", "phase", phase)
		return nil
	}

	// 6. read session marker
	var marker *SessionMarker
	if input != nil && input.SessionID != "" {
		marker, _ = ReadSessionMarker(input.SessionID)
	}

	// 7. dispatch to handler
	ctx := &HookContext{
		Phase:       phase,
		AgentType:   agentType,
		Input:       input,
		Marker:      marker,
		ProjectRoot: projectRoot,
	}

	return dispatchPhase(ctx)
}

// resolvePhase maps an agent's native event name to a canonical lifecycle phase.
// Uses agentx registry to discover event mappings from each agent's definition.
// Returns empty string for unknown events.
func resolvePhase(agentType, eventName string) string {
	eventPhases := agentx.BuildEventPhaseMap()

	agentMap, ok := eventPhases[agentType]
	if !ok {
		// unknown agent type — try all maps as fallback
		for _, m := range eventPhases {
			if phase, ok := m[agentx.HookEvent(eventName)]; ok {
				return string(phase)
			}
		}
		return ""
	}
	phase, ok := agentMap[agentx.HookEvent(eventName)]
	if !ok {
		return ""
	}
	return string(phase)
}

// dispatchPhase routes to the appropriate handler based on the resolved phase.
// Only phases listed in activePhaseBehavior reach here (others are fast-path nooped).
func dispatchPhase(ctx *HookContext) error {
	switch ctx.Phase {
	case phaseStart:
		return handleStart(ctx)
	case phaseCompact:
		return handleCompact(ctx)
	default:
		return nil
	}
}

// handleStart handles the session start phase.
// Ensures primed and optionally starts session recording.
//
// Auto-recording uses belt-and-suspenders: prime already auto-starts recording
// (covering agents without hooks), and we call it again here as a safety net.
// startSessionRecording is idempotent (checks session.IsRecording first).
func handleStart(ctx *HookContext) error {
	source := ""
	if ctx.Input != nil {
		source = ctx.Input.Source
	}
	forceReprime := source == "clear" || source == "compact"

	if ctx.Marker != nil && !forceReprime {
		// already primed — ensure recording is started (idempotent)
		startSessionRecordingIfConfigured(ctx)
		return nil
	}

	agentID := ""
	if ctx.Marker != nil {
		agentID = ctx.Marker.AgentID
	}

	// prime auto-starts recording internally; call again as safety net
	if err := runPrimeForHook(agentID, ctx); err != nil {
		return err
	}

	startSessionRecordingIfConfigured(ctx)
	return nil
}

// handleCompact handles the compact phase.
// Always force re-prime to ensure context survives compaction.
func handleCompact(ctx *HookContext) error {
	agentID := ""
	if ctx.Marker != nil {
		agentID = ctx.Marker.AgentID
	}
	return runPrimeForHook(agentID, ctx)
}

// runPrimeForHook runs ox agent prime as a subprocess.
// Reuses all existing prime logic cleanly via subprocess invocation.
// Passes the original raw stdin bytes to prime to preserve unknown/agent-specific fields.
func runPrimeForHook(agentID string, ctx *HookContext) error {
	oxPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("hook: cannot find ox executable: %w", err)
	}

	args := []string{"agent", "prime"}

	slog.Debug("hook: running prime", "agent_id", agentID, "phase", ctx.Phase)

	cmd := exec.Command(oxPath, args...)
	cmd.Env = os.Environ()
	// pass original raw bytes to preserve unknown fields (not re-serialized)
	if ctx.Input != nil && len(ctx.Input.RawBytes) > 0 {
		cmd.Stdin = strings.NewReader(string(ctx.Input.RawBytes))
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("hook: prime failed: %w", err)
	}
	return nil
}

// startSessionRecordingIfConfigured attempts to start session recording
// if the configuration enables auto-recording.
func startSessionRecordingIfConfigured(ctx *HookContext) {
	resolved := config.ResolveSessionRecording(ctx.ProjectRoot)
	if !resolved.IsAuto() {
		return
	}

	agentID := ""
	if ctx.Marker != nil {
		agentID = ctx.Marker.AgentID
	}

	startSessionRecording(ctx.ProjectRoot, agentID, ctx.AgentType)
}
