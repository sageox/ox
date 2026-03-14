package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
)

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
	phaseStart:     true,
	phaseCompact:   true,
	phaseAfterTool: true,
	phaseStop:      true,
}

// HookContext carries everything a phase handler needs.
type HookContext struct {
	Phase       string            // resolved lifecycle phase
	AgentType   string            // from AGENT_ENV: "claude-code", "gemini", etc.
	Input       *agentx.HookInput // parsed stdin JSON
	Marker      *SessionMarker    // nil if not yet primed
	ProjectRoot string            // git root with .sageox/
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
	case phaseAfterTool:
		return handleAfterTool(ctx)
	case phaseStop:
		return handleAfterTool(ctx) // same drain logic on stop
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

// handleAfterTool incrementally drains new entries from the source JSONL
// into raw.jsonl. Called on PostToolUse and Stop hooks.
func handleAfterTool(ctx *HookContext) error {
	agentID := ""
	if ctx.Marker != nil {
		agentID = ctx.Marker.AgentID
	}

	// Load recording state for this specific agent only — never fall back to repo-wide
	// lookup, which could return a different agent's state in multi-agent repos
	if agentID == "" {
		slog.Debug("hook: afterTool skipped, no agent ID available")
		return nil
	}
	state, err := session.LoadRecordingStateForAgent(ctx.ProjectRoot, agentID)
	if err != nil || state == nil {
		return nil // not recording for this agent, silent noop
	}

	adapter, adapterErr := adapters.GetAdapter(state.AdapterName)
	if adapterErr != nil {
		return nil
	}
	reader, ok := adapter.(adapters.IncrementalReader)
	if !ok {
		return nil // adapter doesn't support incremental reads
	}

	if state.SessionFile == "" {
		// discover session file on first hook call (Claude Code JSONL may not exist at prime time)
		if sf, findErr := adapter.FindSessionFile(agentID, state.StartedAt); findErr == nil && sf != "" {
			state.SessionFile = sf
			_ = session.UpdateRecordingStateForAgent(ctx.ProjectRoot, agentID, func(s *session.RecordingState) {
				s.SessionFile = sf
			})
			slog.Debug("hook: discovered session file", "file", sf)
		} else {
			return nil // session file not available yet
		}
	}

	entries, newOffset, readErr := reader.ReadFromOffset(state.SessionFile, state.SourceOffset)
	if readErr != nil {
		slog.Debug("hook: incremental read failed", "error", readErr)
		return nil // non-fatal, will catch up at stop
	}

	if len(entries) == 0 {
		return nil
	}

	// filter entries after session start time
	if !state.StartedAt.IsZero() {
		filtered := make([]adapters.RawEntry, 0, len(entries))
		for _, e := range entries {
			if !e.Timestamp.Before(state.StartedAt) {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	if len(entries) == 0 {
		// update offset even if no entries passed filter
		_ = session.UpdateRecordingStateForAgent(ctx.ProjectRoot, agentID, func(s *session.RecordingState) {
			s.SourceOffset = newOffset
		})
		return nil
	}

	redactor, _ := session.NewRedactorWithCustomRules(ctx.ProjectRoot)

	sessionEntries := make([]session.Entry, 0, len(entries))
	for _, raw := range entries {
		entry := session.Entry{
			Timestamp: raw.Timestamp,
			Content:   raw.Content,
			ToolName:  raw.ToolName,
			ToolInput: raw.ToolInput,
		}
		entry.Type = mapRoleToEntryType(raw.Role)
		sessionEntries = append(sessionEntries, entry)
	}
	redactor.RedactEntries(sessionEntries)

	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")

	// ensure raw.jsonl header exists before appending entries
	if _, statErr := os.Stat(rawPath); os.IsNotExist(statErr) {
		if headerErr := writeRawHeader(ctx.ProjectRoot, state); headerErr != nil {
			slog.Debug("hook: failed to write raw.jsonl header", "error", headerErr)
		}
	}

	if appendErr := appendRedactedEntries(rawPath, sessionEntries); appendErr != nil {
		slog.Debug("hook: append entries failed", "error", appendErr)
		return nil // non-fatal
	}

	_ = session.UpdateRecordingStateForAgent(ctx.ProjectRoot, agentID, func(s *session.RecordingState) {
		s.SourceOffset = newOffset
		s.EntryCount += len(sessionEntries)
	})

	return nil
}

// appendRedactedEntries appends redacted session entries to a raw.jsonl file.
// ox is the sole writer to raw.jsonl, so no file locking is needed.
// Uses fsync for durability so entries survive process crashes.
func appendRedactedEntries(rawPath string, entries []session.Entry) error {
	f, err := os.OpenFile(rawPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open raw.jsonl: %w", err)
	}
	defer f.Close()

	encoder := json.NewEncoder(f)
	for _, entry := range entries {
		data := map[string]any{
			"type":      string(entry.Type),
			"content":   entry.Content,
			"timestamp": entry.Timestamp,
		}
		if entry.ToolName != "" {
			data["tool_name"] = entry.ToolName
		}
		if entry.ToolInput != "" {
			data["tool_input"] = entry.ToolInput
		}
		if entry.ToolOutput != "" {
			data["tool_output"] = entry.ToolOutput
		}
		if err := encoder.Encode(data); err != nil {
			return fmt.Errorf("encode entry: %w", err)
		}
	}

	return f.Sync()
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
