package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/kb"
	"github.com/sageox/ox/internal/prime"
	"github.com/sageox/ox/internal/proc"
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
//
// Whisper delivery findings (tested empirically via TestWhisperDelivery_ChannelExperiment):
//
//	Hook event          │ stdout injected into model context?
//	────────────────────┼────────────────────────────────────
//	UserPromptSubmit    │ YES — only reliable channel for Claude Code
//	PreToolUse          │ NO  — stdout discarded, same as PostToolUse
//	PostToolUse         │ NO  — stdout is COMPLETELY DISCARDED by Claude Code
//	Stop                │ NO  — fires after session ends, model never sees it
//	SessionStart        │ YES — but only fires once at session start
//	PreCompact          │ YES — but only fires on /clear or /compact
//
// phasePrompt (UserPromptSubmit) is the PRIMARY whisper delivery channel.
// phaseAfterTool (PostToolUse) is kept as a FALLBACK for non-Claude agents
// whose PostToolUse stdout may be injected. The cursor-based dedup in
// WhisperStore prevents double delivery across channels.
var activePhaseBehavior = map[string]bool{
	phaseStart:     true,
	phaseCompact:   true,
	phaseAfterTool: true,
	phaseStop:      true,
	phasePrompt:    true,
	phaseEnd:       true,
}

// HookContext carries everything a phase handler needs.
type HookContext struct {
	Phase       string            // resolved lifecycle phase
	AgentType   string            // from AGENT_ENV: "claude-code", "gemini", etc.
	Input       *agentx.HookInput // parsed stdin JSON
	Marker      *SessionMarker    // nil if not yet primed
	ProjectRoot string            // git root with .sageox/

	// ClearNotice carries finalized-prior-session info from stopSessionForClear
	// to the prime subprocess so prime can emit a user-facing notice. See ADR-019.
	ClearNotice *ClearNoticeInfo
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

// localEventPhases supplements agentx registry with phase mappings for agents
// not yet defined in agentx (pending module release).
var localEventPhases = map[string]map[agentx.HookEvent]agentx.Phase{
	"gemini": {
		"SessionStart": agentx.PhaseStart,
		"BeforeAgent":  agentx.PhasePrompt,
		"AfterTool":    agentx.PhaseAfterTool,
		"SessionEnd":   agentx.PhaseEnd,
	},
}

// resolvePhase maps an agent's native event name to a canonical lifecycle phase.
// Uses agentx registry to discover event mappings from each agent's definition,
// with local fallbacks for agents not yet in agentx.
// Returns empty string for unknown events.
func resolvePhase(agentType, eventName string) string {
	eventPhases := agentx.BuildEventPhaseMap()

	agentMap, ok := eventPhases[agentType]
	if !ok {
		// check local fallback mappings
		if localMap, ok := localEventPhases[agentType]; ok {
			if phase, ok := localMap[agentx.HookEvent(eventName)]; ok {
				return string(phase)
			}
		}
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

// hookAgentID returns the agent ID for heartbeat purposes.
// Priority: session marker (set by prime) > SAGEOX_AGENT_ID env fallback.
// Returns "" if neither source has an ID — heartbeat is skipped in that case.
func hookAgentID(ctx *HookContext) string {
	if ctx.Marker != nil && ctx.Marker.AgentID != "" {
		return ctx.Marker.AgentID
	}
	return os.Getenv("SAGEOX_AGENT_ID")
}

// dispatchPhase routes to the appropriate handler based on the resolved phase.
// Only phases listed in activePhaseBehavior reach here (others are fast-path nooped).
func dispatchPhase(ctx *HookContext) error {
	// emit heartbeat on every hook so the daemon tracks this agent as active —
	// required for murmur nudge delivery during long sessions with no explicit ox commands.
	if agentID := hookAgentID(ctx); agentID != "" {
		Heartbeat(ctx.ProjectRoot, nil, agentID)
	}

	switch ctx.Phase {
	case phaseStart:
		return handleStart(ctx)
	case phaseCompact:
		return handleCompact(ctx)
	case phasePrompt:
		return handlePrompt(ctx)
	case phaseAfterTool:
		return handleAfterTool(ctx)
	case phaseStop:
		return handleStop(ctx)
	case phaseEnd:
		return handleEnd(ctx)
	default:
		return nil
	}
}

// emitStartupBanner writes a systemMessage JSON line to w for agents
// that support the systemMessage protocol (Claude Code, Gemini CLI).
// Production callers pass os.Stdout; tests pass a buffer.
func emitStartupBanner(w io.Writer, ctx *HookContext) {
	switch ctx.AgentType {
	case "claude-code", "gemini":
		// these agents inject hook stdout into model context
	default:
		return
	}
	// resolve KB binding from the hook's actual working directory so nested
	// KB bindings in subdirectories are honored. Fall back to project root
	// if Getwd fails (e.g., directory deleted out from under the process).
	resolveFrom := ctx.ProjectRoot
	if wd, wdErr := os.Getwd(); wdErr == nil && wd != "" {
		resolveFrom = wd
	}
	kbID, kbType := kb.ResolveCurrentKBIDAndType(resolveFrom)
	recording := config.ResolveSessionRecording(ctx.ProjectRoot, kbID, kbType)
	canonicalType := agentx.ResolveAgentENV(ctx.AgentType)
	if canonicalType == agentx.AgentTypeUnknown {
		canonicalType = agentx.AgentType(ctx.AgentType)
	}
	name := agentDisplayName(canonicalType)
	msg := fmt.Sprintf("%s is being enhanced by team context from SageOx.", name)
	if recording.IsAuto() {
		msg += "\nThis session is being recorded and shared with your team."
	}
	data, err := json.Marshal(map[string]string{"systemMessage": msg})
	if err != nil {
		slog.Debug("hook: failed to marshal startup banner", "error", err)
		return
	}
	fmt.Fprintln(w, string(data))
}

// handleStart handles the session start phase.
// Ensures primed and optionally starts session recording.
//
// Auto-recording uses belt-and-suspenders: prime already auto-starts recording
// (covering agents without hooks), and we call it again here as a safety net.
// startSessionRecording is idempotent (checks session.IsRecording first).
func handleStart(ctx *HookContext) error {
	emitStartupBanner(os.Stdout, ctx)

	source := ""
	if ctx.Input != nil {
		source = ctx.Input.Source
	}
	// prime.IsForceReprimeSource is the single source of truth for this
	// gate — agent_prime.go's compact-re-prime decision (ShouldCompactReprime)
	// re-derives the identical boolean from the same source value so the
	// two call sites can never disagree about what counts as "context was
	// just wiped." See bd ox-32f6.
	forceReprime := prime.IsForceReprimeSource(source)

	if ctx.Marker != nil && !forceReprime {
		// already primed — ensure recording is started (idempotent)
		startSessionRecordingIfConfigured(ctx)
		return nil
	}

	agentID := ""
	if ctx.Marker != nil {
		agentID = ctx.Marker.AgentID
	}

	// on /clear or /compact: stop the current session so it gets finalized,
	// then prime will start a fresh recording for the new context window
	if forceReprime && agentID != "" {
		stopSessionForClear(ctx, agentID)
		// the model's context window was wiped but the on-disk task cursor
		// survives — reset it so pending tasks re-surface in the fresh context.
		resetTaskCursor(ctx.ProjectRoot, agentID)
	}

	// prime auto-starts recording internally; call again as safety net
	if err := runPrimeForHook(agentID, ctx); err != nil {
		return err
	}

	// reload marker after prime — prime runs as a subprocess and writes the
	// marker to disk, but ctx.Marker is still nil from before prime ran.
	// Without this reload, the safety-net recording call below uses agentID=""
	// which fails with "path cannot be empty: agent ID".
	if ctx.Marker == nil && ctx.Input != nil && ctx.Input.SessionID != "" {
		ctx.Marker, _ = ReadSessionMarker(ctx.Input.SessionID)
	}

	startSessionRecordingIfConfigured(ctx)
	return nil
}

// stopSessionForClear stops the current session recording during /clear or /compact.
// This finalizes the old session so it gets uploaded, then prime starts a fresh one.
// Sets StoppedAt, sends fire-and-forget IPC finalization to the daemon, and clears
// recording state so prime can start a fresh session.
//
// As a side effect, populates ctx.ClearNotice with finalized-session info so the
// downstream prime subprocess can render a user-facing notice describing the
// boundary transition (see ADR-019).
func stopSessionForClear(ctx *HookContext, agentID string) {
	state, err := session.LoadRecordingStateForAgent(ctx.ProjectRoot, agentID)
	if err != nil || state == nil {
		return // not recording, nothing to stop
	}

	// capture finalized-session info for the post-clear notice (ADR-019).
	// done before we mutate state so SessionPath is still accurate.
	ctx.ClearNotice = buildClearNoticeFromState(state)

	// set StoppedAt to signal this session is complete
	now := time.Now()
	if updateErr := session.UpdateRecordingStateForAgent(ctx.ProjectRoot, agentID, func(s *session.RecordingState) {
		s.StoppedAt = &now
	}); updateErr != nil {
		slog.Debug("hook: clear could not set StoppedAt", "agent_id", agentID, "error", updateErr)
	}

	// fire-and-forget IPC to daemon to finalize the stopped session
	if state.SessionPath != "" {
		if ledgerPath := deriveLedgerPath(state.SessionPath); ledgerPath != "" {
			client := daemon.NewClientForCurrentRepoWithTimeout(100 * time.Millisecond)
			sessionName := filepath.Base(state.SessionPath)
			if ipcErr := client.SessionFinalize(daemon.SessionFinalizeIPCPayload{
				SessionName: sessionName,
				LedgerPath:  ledgerPath,
				CachePath:   state.SessionPath,
				ProjectRoot: ctx.ProjectRoot,
			}); ipcErr != nil {
				slog.Debug("hook: clear finalize IPC failed", "error", ipcErr)
			}
		}
	}

	// clear recording state so prime starts a fresh session
	if clearErr := session.ClearRecordingStateForAgent(ctx.ProjectRoot, agentID); clearErr != nil {
		slog.Debug("hook: clear could not remove recording state", "agent_id", agentID, "error", clearErr)
	}

	slog.Debug("hook: stopped session for clear", "agent_id", agentID)
}

// handleEnd handles the SessionEnd phase — fires when the calling agent
// (Claude Code, Gemini CLI, etc.) is exiting. Auto-finalizes the active
// recording so the user doesn't have to remember to run `ox session stop`.
//
// Without this handler, sessions only finalize via:
//   - manual `ox agent <id> session stop` (most users never run it)
//   - the daemon's 24h stale-recording sweep
//
// Most users just close their agent and walk away — leaving the recording
// stranded in the cache for up to a day. handleEnd closes that gap.
//
// SessionEnd MUST use the `delegated` mode regardless of the user's
// `agent.summarizer` setting: at this point the calling agent process is
// being torn down, so `inline` (which whispers a prompt back to the agent
// for it to run the LLM in its warm cache) has no agent left to whisper to.
// The daemon IPC dispatch below is the only viable path in this window.
//
// Idempotency: SessionEnd can fire multiple times (window close, debugger
// reattach, IDE reload). The handler is safe to call repeatedly — the
// first call sets StoppedAt and clears recording state; subsequent calls
// see no state and return immediately.
func handleEnd(ctx *HookContext) error {
	agentID := ""
	if ctx.Marker != nil {
		agentID = ctx.Marker.AgentID
	}
	if agentID == "" {
		slog.Debug("hook: end skipped, no agent ID")
		return nil
	}

	state, err := session.LoadRecordingStateForAgent(ctx.ProjectRoot, agentID)
	if err != nil || state == nil {
		slog.Debug("hook: end no recording state", "agent_id", agentID)
		return nil
	}

	if state.StoppedAt != nil {
		slog.Debug("hook: end already finalized", "agent_id", agentID, "stopped_at", *state.StoppedAt)
		return nil
	}

	now := time.Now()
	if updateErr := session.UpdateRecordingStateForAgent(ctx.ProjectRoot, agentID, func(s *session.RecordingState) {
		s.StoppedAt = &now
	}); updateErr != nil {
		slog.Debug("hook: end could not set StoppedAt", "agent_id", agentID, "error", updateErr)
	}

	// dispatch delegated finalization via daemon IPC. Best-effort: if the
	// daemon is unreachable, the daemon's anti-entropy sweep will still
	// pick up the StoppedAt-marked recording within the 24h stale window.
	if state.SessionPath != "" {
		if ledgerPath := deriveLedgerPath(state.SessionPath); ledgerPath != "" {
			client := daemon.NewClientForCurrentRepoWithTimeout(100 * time.Millisecond)
			sessionName := filepath.Base(state.SessionPath)
			if ipcErr := client.SessionFinalize(daemon.SessionFinalizeIPCPayload{
				SessionName: sessionName,
				LedgerPath:  ledgerPath,
				CachePath:   state.SessionPath,
				ProjectRoot: ctx.ProjectRoot,
			}); ipcErr != nil {
				slog.Debug("hook: end finalize IPC failed", "error", ipcErr)
			}
		}
	}

	if clearErr := session.ClearRecordingStateForAgent(ctx.ProjectRoot, agentID); clearErr != nil {
		slog.Debug("hook: end could not remove recording state", "agent_id", agentID, "error", clearErr)
	}

	slog.Info("hook: finalized session on agent end", "agent_id", agentID)
	return nil
}

// handlePrompt handles the user prompt submission phase.
// Emits pending whispers to stdout — this is the PRIMARY whisper delivery channel.
//
// Why UserPromptSubmit and not PostToolUse?
// Claude Code's hook stdout handling differs by event:
//   - UserPromptSubmit: stdout IS injected as a <system-reminder> into the model's
//     context window before the user's prompt is processed. The model sees it.
//   - PostToolUse: stdout is COMPLETELY DISCARDED. We confirmed this empirically —
//     content written to PostToolUse stdout never reaches the model, regardless of
//     XML format used (<new-context>, <system-reminder>, or plain text).
//
// What did NOT work (tested in TestWhisperDelivery_ChannelExperiment):
//  1. PostToolUse + <new-context> XML  → discarded by Claude Code, never seen
//  2. PostToolUse + <system-reminder>  → discarded by Claude Code, never seen
//  3. PostToolUse + plain text         → discarded by Claude Code, never seen
//  4. Stop hook stdout                 → fires after session ends, model never sees it
//
// What DOES work:
//  1. UserPromptSubmit + <system-reminder> → injected into model context (this handler)
//  2. UserPromptSubmit + plain text        → also injected, but <system-reminder>
//     tags are preferred because Claude treats them as trusted system-level context
//  3. `ox agent <id> whisper` via Bash     → active pull, model reads command output
//
// The cursor mechanism in WhisperStore (per-agentID last_seen timestamp) ensures
// whispers are delivered exactly once across all channels. If handlePrompt delivers
// a whisper, the PostToolUse fallback and active pull get 0 entries — no duplication.
func handlePrompt(ctx *HookContext) error {
	// Local-recall preamble runs BEFORE whispers so the model sees prior
	// ledger context first. Strictly additive: any failure / timeout /
	// no-match leaves the existing whisper path completely untouched.
	if ctx.Input != nil {
		if prompt := extractPromptText(ctx.Input.RawBytes); prompt != "" {
			emitLocalRecallPreamble(os.Stdout, prompt)

			// Cloud-query gate (opt-in only; default off). Wired here so the
			// policy + redaction layer is exercised on every prompt — even
			// though the cloud-side runner that consumes the decision is a
			// follow-up (epic ox-r9mq). Today this is a no-op on the default
			// config; under opt-in it logs the decision and produces the
			// redacted prompt the future runner will transmit. Keeping the
			// gate hot means a future runner cannot accidentally skip the
			// redaction step — the only way to get a transmittable prompt
			// is through PrepareCloudQuery.
			cqCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			decision := PrepareCloudQuery(cqCtx, ctx.ProjectRoot, prompt)
			cancel()
			if decision.ShouldQuery {
				slog.Info("hook: cloud-query gate open", "redacted_tokens", decision.RedactedTokens)
				// cloud runner dispatch lives in a follow-up; until it lands
				// the decision is computed (and tested) but no bytes leave
				// the machine.
			}
		}
	}

	agentID := ""
	if ctx.Marker != nil {
		agentID = ctx.Marker.AgentID
	}
	if agentID == "" {
		return nil
	}

	// ADR-020: emit a per-prompt nudge while the session is suspended. This
	// fires on every UserPromptSubmit so the user cannot silently forget that
	// recording is paused. Scoped to the current agent's suspended session
	// only — other agents' paused sessions in this repo do not bleed into
	// this terminal's nudge.
	emitSuspendedNudge(os.Stdout, ctx.ProjectRoot, agentID)

	// Deliver a pending plan-exit enrichment nudge (stashed by handleAfterTool
	// on ExitPlanMode). UserPromptSubmit is the only Claude Code channel whose
	// stdout reaches the model, so delivery happens here — on the turn that
	// begins right after the plan was approved.
	emitPlanNudge(os.Stdout, ctx.ProjectRoot, agentID)

	// Steer the agent toward `ox plan enrich`/`ox plan render` at the planning
	// moment — fired when the agent is in plan mode (permission_mode == "plan")
	// OR the prompt asks to render an HTML plan, once per planning episode.
	// Complements the plan-EXIT nudge above. Fail-open.
	var rawPrompt []byte
	if ctx.Input != nil {
		rawPrompt = ctx.Input.RawBytes
	}
	emitPlanHint(os.Stdout, ctx.ProjectRoot, agentID, rawPrompt)

	emitWhispers(os.Stdout, agentID)

	// Surface any scheduled agent tasks (throttled). This is the sole channel
	// into the model's context for the task queue — see agent_tasks_surface.go.
	// Pass the resolved agent type (ctx.AgentType is defaulted to claude-code
	// when AGENT_ENV is unset) so target-scoped tasks still surface.
	emitAgentTasks(os.Stdout, ctx.ProjectRoot, agentID, ctx.AgentType)
	return nil
}

// emitSuspendedNudge writes a single-line system reminder to stdout when
// the current agent's recording is suspended. No-op when not suspended.
func emitSuspendedNudge(w io.Writer, projectRoot, agentID string) {
	if projectRoot == "" || agentID == "" {
		return
	}
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil || state == nil || state.SuspendedAt == nil {
		return
	}
	dur := formatPausedDuration(time.Since(*state.SuspendedAt))
	fmt.Fprintf(w, "<system-reminder>[ox] ⏸ Recording SUSPENDED (%s ago). Resume: /ox-session-resume · Stop: /ox-session-stop</system-reminder>\n", dur)
}

// handleCompact handles the compact phase.
// Always force re-prime to ensure context survives compaction.
func handleCompact(ctx *HookContext) error {
	agentID := ""
	if ctx.Marker != nil {
		agentID = ctx.Marker.AgentID
	}
	// compaction wipes the context window; reset the task cursor so pending
	// tasks re-surface afterward (the on-disk cursor outlives the context).
	resetTaskCursor(ctx.ProjectRoot, agentID)
	return runPrimeForHook(agentID, ctx)
}

// handleAfterTool incrementally drains new entries from the source JSONL
// into raw.jsonl. Called on PostToolUse and Stop hooks.
//
// Whisper delivery here is a FALLBACK only. For Claude Code, PostToolUse stdout
// is discarded — whispers emitted here never reach the model. This call exists
// for non-Claude agents (Cursor, Windsurf, etc.) whose PostToolUse stdout MAY
// be injected into context. The cursor-based dedup in WhisperStore ensures that
// if handlePrompt (UserPromptSubmit) already delivered the whispers, this call
// returns 0 entries and emits nothing.
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

	// Plan-exit nudge (Gold tier): the closest plan-exit signal Claude Code
	// exposes is this PostToolUse firing after ExitPlanMode. Strictly gated on
	// the tool name so it is a no-op for every other tool — NOT a noisy hook.
	// Enriches the approved plan and stashes a one-line nudge that the next
	// UserPromptSubmit (handlePrompt) delivers. Independent of recording state,
	// so it runs before the recording-state checks below.
	if ctx.Input != nil && ctx.Input.ToolName == exitPlanModeToolName {
		handlePlanExit(ctx, agentID)
	}

	// emit pending whispers (fallback — primary delivery is handlePrompt)
	emitWhispers(os.Stdout, agentID)

	state, err := session.LoadRecordingStateForAgent(ctx.ProjectRoot, agentID)
	if err != nil || state == nil {
		slog.Debug("hook: afterTool no recording state", "agentID", agentID, "err", err)
		return nil // not recording for this agent, silent noop
	}

	// Track every afterTool invocation + its terminal reason so `ox session status`
	// can distinguish a healthy idle session (status=ok) from a broken recording
	// (status=session-file-not-found, adapter-missing, etc.) when EntryCount=0.
	recordHookStatus := func(status string) {
		_ = session.UpdateRecordingStateForAgent(ctx.ProjectRoot, agentID, func(s *session.RecordingState) {
			s.HookInvocations++
			s.LastHookStatus = status
			now := time.Now().UTC()
			s.LastHookAt = &now
		})
	}

	adapter, adapterErr := adapters.GetAdapter(state.AdapterName)
	if adapterErr != nil {
		slog.Info("hook: afterTool adapter not found", "agentID", agentID, "adapter", state.AdapterName, "err", adapterErr)
		recordHookStatus("adapter-not-found")
		return nil
	}
	reader, ok := adapter.(adapters.IncrementalReader)
	if !ok {
		recordHookStatus("adapter-no-incremental-reader")
		return nil // adapter doesn't support incremental reads
	}

	if state.SessionFile == "" {
		// discover session file on first hook call (Claude Code JSONL may not exist at prime time)
		repoRoot := state.WorkspacePath
		if repoRoot == "" {
			repoRoot = ctx.ProjectRoot
		}
		sf, findErr := adapter.FindSessionFile(adapters.SessionLookup{
			RepoRoot: repoRoot,
			AgentID:  agentID,
			Since:    state.StartedAt,
		})
		if findErr != nil || sf == "" {
			slog.Info("hook: session file not found", "agentID", agentID, "adapter", state.AdapterName, "repo", repoRoot, "err", findErr)
			recordHookStatus("session-file-not-found")
			return nil // session file not available yet
		}
		state.SessionFile = sf
		_ = session.UpdateRecordingStateForAgent(ctx.ProjectRoot, agentID, func(s *session.RecordingState) {
			s.SessionFile = sf
		})
		slog.Debug("hook: discovered session file", "file", sf)
	}

	// staleness check: if the source file disappeared or shrank (e.g., Claude Code
	// created a new file after compaction), try to rediscover it
	if fi, statErr := os.Stat(state.SessionFile); statErr != nil || fi.Size() < state.SourceOffset {
		if statErr != nil {
			slog.Info("hook: session file missing, attempting rediscovery", "file", state.SessionFile)
		} else {
			slog.Info("hook: session file shrank, attempting rediscovery", "file", state.SessionFile, "size", fi.Size(), "offset", state.SourceOffset)
		}
		repoRoot := state.WorkspacePath
		if repoRoot == "" {
			repoRoot = ctx.ProjectRoot
		}
		sf, findErr := adapter.FindSessionFile(adapters.SessionLookup{
			RepoRoot: repoRoot,
			AgentID:  agentID,
			Since:    state.StartedAt,
		})
		if findErr == nil && sf != "" && sf != state.SessionFile {
			slog.Info("hook: rediscovered session file", "old", state.SessionFile, "new", sf)
			state.SessionFile = sf
			_ = session.UpdateRecordingStateForAgent(ctx.ProjectRoot, agentID, func(s *session.RecordingState) {
				s.SessionFile = sf
				s.SourceOffset = 0 // reset offset for new file
			})
			state.SourceOffset = 0
		}
	}

	// use StartOffset as minimum read position to skip pre-session content
	readOffset := state.SourceOffset
	if state.StartOffset > 0 && readOffset < state.StartOffset {
		readOffset = state.StartOffset
	}

	entries, newOffset, readErr := reader.ReadFromOffset(state.SessionFile, readOffset)
	if readErr != nil {
		slog.Info("hook: incremental read failed", "agentID", agentID, "adapter", state.AdapterName, "file", state.SessionFile, "offset", readOffset, "error", readErr)
		recordHookStatus("read-error")
		return nil // non-fatal, will catch up at stop
	}

	if len(entries) == 0 {
		recordHookStatus("no-new-entries")
		return nil
	}

	// filter entries by timestamp — strict After() to prevent boundary leaks
	// for legacy states (StartOffset=0) where offset-based filtering isn't available
	if !state.StartedAt.IsZero() {
		filtered := make([]adapters.RawEntry, 0, len(entries))
		for _, e := range entries {
			if e.Timestamp.After(state.StartedAt) {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	if len(entries) == 0 {
		// update offset even if no entries passed filter
		now := time.Now().UTC()
		_ = session.UpdateRecordingStateForAgent(ctx.ProjectRoot, agentID, func(s *session.RecordingState) {
			s.SourceOffset = newOffset
			s.HookInvocations++
			s.LastHookStatus = "no-entries-after-filter"
			s.LastHookAt = &now
		})
		return nil
	}

	redactor, _ := session.NewRedactorWithCustomRules(ctx.ProjectRoot)

	sessionEntries := session.ConvertRawEntries(entries)

	// enrich the last tool entry with error data from hook stdin.
	// hook stdin provides real-time error info that isn't in the JSONL file.
	if ctx.Input != nil && ctx.Input.ToolError != "" {
		for i := len(sessionEntries) - 1; i >= 0; i-- {
			if sessionEntries[i].Type == session.EntryTypeTool {
				sessionEntries[i].IsError = true
				if sessionEntries[i].ToolOutput == "" {
					sessionEntries[i].ToolOutput = ctx.Input.ToolError
				}
				break
			}
		}
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
		slog.Info("hook: append entries failed", "agentID", agentID, "path", rawPath, "error", appendErr)
		recordHookStatus("append-failed")
		return nil // non-fatal
	}

	now := time.Now().UTC()
	_ = session.UpdateRecordingStateForAgent(ctx.ProjectRoot, agentID, func(s *session.RecordingState) {
		s.SourceOffset = newOffset
		s.EntryCount += len(sessionEntries)
		s.HookInvocations++
		s.LastHookStatus = "ok"
		s.LastHookAt = &now
		// Refresh parent_pid only if the stored PID is dead — each hook call runs in a
		// new transient shell, so blindly overwriting with os.Getppid() would store a dead
		// PID. FindAgentAncestorPID walks the tree to find the long-lived agent process.
		if !proc.IsAlive(s.ParentPID) {
			if agentPID := proc.FindAgentAncestorPID(); agentPID > 0 {
				s.ParentPID = agentPID
			}
		}
	})

	return nil
}

// handleStop handles the session stop phase.
// Drain-only: flushes remaining entries to raw.jsonl (same as afterTool).
//
// IMPORTANT: This hook fires on EVERY response turn (PhaseStop = "agent finished
// responding"), NOT only at session end. Setting StoppedAt or sending SessionFinalize
// IPC here would mark active sessions as stopped after every turn and trigger
// premature finalization. Those operations belong in the explicit CLI command
// `ox agent <id> session stop` only.
func handleStop(ctx *HookContext) error {
	if err := handleAfterTool(ctx); err != nil {
		slog.Debug("hook: stop drain failed", "error", err)
	}
	return nil
}

// deriveLedgerPath attempts to extract the ledger root from a session path.
// Session paths follow patterns like:
//   - <ledger>/.sageox/cache/sessions/<name>
//   - <ledger>/sessions/<name>
func deriveLedgerPath(sessionPath string) string {
	// check for .sageox/cache/sessions pattern
	if idx := strings.Index(sessionPath, "/.sageox/cache/sessions/"); idx >= 0 {
		return sessionPath[:idx]
	}
	// check for /sessions/ pattern (direct ledger path)
	dir := filepath.Dir(sessionPath)
	if filepath.Base(dir) == "sessions" {
		return filepath.Dir(dir)
	}
	return ""
}

// appendRedactedEntries appends session entries to a raw.jsonl file via
// the canonical session.RawWriter chokepoint. Per ox-h20u: the writer
// guarantees the three-layer redaction stack (CommandRedactor →
// built-in Redactor → gitleaks extras) runs before any byte reaches
// disk. Callers no longer need to remember to invoke redactors —
// WriteEntry is the only way to land bytes in raw.jsonl.
//
// ox is the sole writer to raw.jsonl, so no file locking is needed.
// Uses fsync for durability so entries survive process crashes.
func appendRedactedEntries(rawPath string, entries []session.Entry) error {
	rw, err := session.NewRawWriter(rawPath, "")
	if err != nil {
		return fmt.Errorf("open raw.jsonl: %w", err)
	}
	for i := range entries {
		if err := rw.WriteEntry(&entries[i]); err != nil {
			_ = rw.Close()
			return fmt.Errorf("encode entry: %w", err)
		}
	}
	return rw.CloseAndSync()
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
	env := buildPrimeEnv(agentID)
	if pair := serializeClearNoticeEnv(ctx.ClearNotice); pair != "" {
		env = append(env, pair)
	}
	cmd.Env = env
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

// buildPrimeEnv constructs the environment for the prime subprocess.
// Passes OX_PARENT_PID for stable PID tracking and SAGEOX_AGENT_ID for
// agent ID reuse (critical for /clear where marker lookup may fail).
func buildPrimeEnv(agentID string) []string {
	env := append(os.Environ(),
		// Pass the long-lived agent PID (e.g., claude) to prime for session recording.
		// Hooks run inside a transient bash shell, so os.Getppid() returns the shell PID
		// which dies immediately. FindAgentAncestorPID walks the tree to find the agent.
		fmt.Sprintf("OX_PARENT_PID=%d", proc.FindAgentAncestorPID()),
	)
	if agentID != "" {
		env = append(env, fmt.Sprintf("SAGEOX_AGENT_ID=%s", agentID))
	}
	return env
}

// startSessionRecordingIfConfigured attempts to start session recording
// if the configuration enables auto-recording.
func startSessionRecordingIfConfigured(ctx *HookContext) {
	// resolve KB binding from the hook's actual working directory so nested
	// KB bindings are honored. Falls back to project root if Getwd fails.
	resolveFrom := ctx.ProjectRoot
	if wd, wdErr := os.Getwd(); wdErr == nil && wd != "" {
		resolveFrom = wd
	}
	kbID, kbType := kb.ResolveCurrentKBIDAndType(resolveFrom)
	resolved := config.ResolveSessionRecording(ctx.ProjectRoot, kbID, kbType)
	if !resolved.IsAuto() {
		return
	}

	agentID := ""
	if ctx.Marker != nil {
		agentID = ctx.Marker.AgentID
	}

	startSessionRecording(ctx.ProjectRoot, agentID, ctx.AgentType, "")
}
