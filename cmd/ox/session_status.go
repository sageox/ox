package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

// countRawEntries counts turn entries in a raw.jsonl file — total lines minus
// the header line (if present). Returns -1 if the file can't be read; the
// caller should treat that as "unknown" and skip cross-checking.
//
// Used to cross-check against RecordingState.EntryCount so `ox session status`
// can warn when the two disagree — the failure mode from issue #519 where the
// state said "recording" but raw.jsonl only contained the header.
func countRawEntries(rawPath string) int {
	f, err := os.Open(rawPath)
	if err != nil {
		return -1
	}
	defer f.Close()

	n := 0
	scanner := bufio.NewScanner(f)
	// raise the default 64KB line cap — some entries (large tool outputs) can
	// legitimately exceed it and we'd rather count than miss.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		n++
	}
	if scanner.Err() != nil {
		return -1
	}
	if n == 0 {
		return 0
	}
	return n - 1 // subtract header line
}

// stallGracePeriod is how long a recording can run with 0 entries before we
// flag it as stalled. Generous because quiet sessions (user reading, thinking)
// legitimately have few hooks. The stall condition requires BOTH (a) significant
// hook activity AND (b) zero captured entries — both must be true.
const stallGracePeriod = 3 * time.Minute

// stallHookThreshold: require at least this many hook invocations with zero
// entries before warning. Filters out fresh sessions that have only had one
// or two hook fires.
const stallHookThreshold = 5

// sessionStallStatus returns (stalled, human-readable reason) for a recording.
// A session is "stalled" when hooks have been firing but no entries have been
// captured — the silent-failure class of bug exposed by issue #519.
func sessionStallStatus(state *session.RecordingState) (bool, string) {
	if state == nil {
		return false, ""
	}
	if state.EntryCount > 0 {
		return false, ""
	}
	if state.HookInvocations < stallHookThreshold {
		return false, ""
	}
	if state.Duration() < stallGracePeriod {
		return false, ""
	}
	reason := state.LastHookStatus
	if reason == "" {
		reason = "hooks fired but no entries captured"
	} else {
		reason = fmt.Sprintf("hooks fired %d× with 0 entries (last status: %s)", state.HookInvocations, reason)
	}
	return true, reason
}

var sessionStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check recording status",
	Long: `Check the current session recording status.

Shows all active recordings in this project. With multiple worktrees
or agents, there may be several recordings in progress simultaneously.

Use --current to filter to the calling agent's recording only
(requires SAGEOX_AGENT_ID environment variable).

Examples:
  ox session status              # show all active recordings
  ox session status --json       # JSON output
  ox session status --current    # only this agent's recording`,
	RunE: runSessionStatus,
}

// sessionStatusOutput is the JSON output format for session status.
type sessionStatusOutput struct {
	Recording     bool                    `json:"recording"`
	AgentAlive    *bool                   `json:"agent_alive,omitempty"` // nil if no PID available, false if agent exited
	Guidance      string                  `json:"guidance,omitempty"`
	Count         int                     `json:"count,omitempty"`
	Sessions      []sessionRecordingEntry `json:"sessions,omitempty"`
	Title         string                  `json:"title,omitempty"`
	DurationSecs  int                     `json:"duration_seconds,omitempty"`
	Duration      string                  `json:"duration,omitempty"`
	EntryCount    int                     `json:"entry_count,omitempty"`
	FilterMode    string                  `json:"filter_mode,omitempty"`
	Model         string                  `json:"model,omitempty"`
	Agent         string                  `json:"agent,omitempty"`
	AgentID       string                  `json:"agent_id,omitempty"`
	ProcessAlive  *bool                   `json:"process_alive,omitempty"`  // nil if unknown, true/false if checked
	ProcessStatus string                  `json:"process_status,omitempty"` // "alive", "dead", or "unknown"
	SessionFile   string                  `json:"session_file,omitempty"`
	StartedAt     string                  `json:"started_at,omitempty"`
	WorkspacePath string                  `json:"workspace_path,omitempty"`
	Branch        string                  `json:"branch,omitempty"`

	// Hook observability (see RecordingState for details)
	HookInvocations int    `json:"hook_invocations,omitempty"`
	LastHookStatus  string `json:"last_hook_status,omitempty"`
	LastHookAt      string `json:"last_hook_at,omitempty"`
	Stalled         bool   `json:"stalled,omitempty"` // hooks fired but no entries captured
	StalledReason   string `json:"stalled_reason,omitempty"`

	// ADR-020 pause/resume surface
	Suspended            bool   `json:"suspended,omitempty"`              // true while SuspendedAt != nil
	SuspendedAt          string `json:"suspended_at,omitempty"`           // RFC3339
	SuspendedDuration    string `json:"suspended_duration,omitempty"`     // human-readable
	PauseCount           int    `json:"pause_count,omitempty"`            // monotonic counter
	InheritedPause       bool   `json:"inherited_pause,omitempty"`        // started suspended via /clear marker
	InheritedFromSession string `json:"inherited_from_session,omitempty"` // session that finalized at /clear

	// ADR-029 draft-placeholder surface.
	//
	// Without these, "the /c/ link in my PR doesn't resolve" is
	// indistinguishable from "the placeholder published fine and the server is
	// behind" — which is the whole reason the git-backed channel was added
	// alongside the already-silent started-notification. ConversationURL is
	// included so an agent never has to reconstruct the link; reconstruction
	// is the confabulation vector sessionLinkOutputs exists to close.
	TurnCount        int    `json:"turn_count,omitempty"`
	DraftState       string `json:"draft_state,omitempty"`        // disabled | pending | published | stale | failed
	DraftPublishedAt string `json:"draft_published_at,omitempty"` // RFC3339
	ConversationURL  string `json:"conversation_url,omitempty"`   // https://<endpoint>/c/<session_id>
}

// sessionRecordingEntry represents one active recording in the multi-session output.
type sessionRecordingEntry struct {
	AgentID       string `json:"agent_id"`
	AgentAlive    *bool  `json:"agent_alive,omitempty"` // nil if no PID available, false if agent exited
	Title         string `json:"title,omitempty"`
	Agent         string `json:"agent,omitempty"`
	DurationSecs  int    `json:"duration_seconds"`
	Duration      string `json:"duration"`
	EntryCount    int    `json:"entry_count"`
	FilterMode    string `json:"filter_mode,omitempty"`
	Model         string `json:"model,omitempty"`
	ProcessAlive  *bool  `json:"process_alive,omitempty"`  // nil if unknown, true/false if checked
	ProcessStatus string `json:"process_status,omitempty"` // "alive", "dead", or "unknown"
	StartedAt     string `json:"started_at"`
	SessionFile   string `json:"session_file,omitempty"`
	WorkspacePath string `json:"workspace_path,omitempty"`
	Branch        string `json:"branch,omitempty"`

	// ADR-020 pause/resume surface — kept in sync with sessionStatusOutput.
	// Multi-session JSON consumers (status dashboards, daemon, agents
	// enumerating siblings) need to tell suspended sessions apart from
	// actively-recording ones.
	Suspended            bool   `json:"suspended,omitempty"`
	SuspendedAt          string `json:"suspended_at,omitempty"`
	SuspendedDuration    string `json:"suspended_duration,omitempty"`
	PauseCount           int    `json:"pause_count,omitempty"`
	InheritedPause       bool   `json:"inherited_pause,omitempty"`
	InheritedFromSession string `json:"inherited_from_session,omitempty"`

	// ADR-029 draft surface — kept in sync with sessionStatusOutput for the
	// same reason the pause fields are: multi-session consumers need to tell
	// a session whose placeholder published from one whose publish is failing.
	TurnCount        int    `json:"turn_count,omitempty"`
	DraftState       string `json:"draft_state,omitempty"`
	DraftPublishedAt string `json:"draft_published_at,omitempty"`
	ConversationURL  string `json:"conversation_url,omitempty"`
}

// buildSessionRecordingEntry maps a RecordingState into the multi-session
// JSON row, including ADR-020 pause-state fields. Extracted from
// runSessionStatus so the mapping can be unit-tested without setting up
// the full multi-recording fixture on disk.
func buildSessionRecordingEntry(s *session.RecordingState, alive *bool, status string, draftCtx draftStatusContext) sessionRecordingEntry {
	d := s.Duration()
	entry := sessionRecordingEntry{
		AgentID:       s.AgentID,
		AgentAlive:    agentAlivePtr(s),
		Title:         s.Title,
		Agent:         s.AdapterName,
		DurationSecs:  int(d.Seconds()),
		Duration:      formatDurationHuman(d),
		EntryCount:    s.EntryCount,
		FilterMode:    s.FilterMode,
		Model:         s.Model,
		ProcessAlive:  alive,
		ProcessStatus: status,
		StartedAt:     s.StartedAt.Format("2006-01-02T15:04:05Z07:00"),
		SessionFile:   s.SessionFile,
		WorkspacePath: s.WorkspacePath,
		Branch:        s.Branch,
	}
	// ADR-020: propagate pause state per-row so multi-session JSON
	// consumers can distinguish suspended sessions from active ones.
	// Mirrors the single-session output's pause surface.
	if s.SuspendedAt != nil {
		entry.Suspended = true
		entry.SuspendedAt = s.SuspendedAt.Format("2006-01-02T15:04:05Z07:00")
		entry.SuspendedDuration = formatPausedDuration(time.Since(*s.SuspendedAt))
		entry.PauseCount = s.PauseCount
		entry.InheritedPause = s.InheritedPause
		entry.InheritedFromSession = s.InheritedFromSession
	}
	draftCtx.applyTo(s, &entry.TurnCount, &entry.DraftState, &entry.DraftPublishedAt, &entry.ConversationURL)
	return entry
}

// draftStateFor classifies where this recording is in the draft-placeholder
// lifecycle (ADR-029).
//
// The distinction that matters is published vs failed. DraftPublishedTurn is
// stamped on every ATTEMPT while DraftPublishedAt is stamped only on success,
// so the (turn set, time nil) pair is the durable record of "we tried and it
// did not land" — the state that otherwise looks identical to a healthy
// session whose server page is merely lagging.
func draftStateFor(resolved *config.ResolvedSessionDraft, s *session.RecordingState) string {
	if s == nil {
		return ""
	}
	if resolved == nil || !resolved.Enabled {
		return "disabled"
	}
	// Order matters: check the FAILED shape before the published shape.
	// DraftPublishedAt is only ever set, never cleared, so a session whose
	// first publish succeeded and whose later refreshes are all failing would
	// otherwise report "published" forever — hiding exactly the breakage this
	// field exists to surface. DraftPublishedTurn is stamped on every attempt,
	// so an attempt newer than the last success means the latest one failed.
	if s.DraftPublishedAt != nil {
		if s.DraftAttemptTurn > s.DraftPublishedTurn {
			return "stale"
		}
		return "published"
	}
	if s.DraftAttemptTurn > 0 {
		return "failed"
	}
	return "pending"
}

// conversationURLForState builds the durable /c/<session_id> link for a
// recording, or "" when one cannot be derived. Best-effort: this is a status
// display, and a missing project config must not fail the command.
//
// Gated on the attribution.session toggle, like every other session-link
// surface. sessionLinkOutputs documents the invariant: an empty toggle must
// disable them ALL together. Emitting the link here regardless would hand a
// /c/ URL to users who deliberately turned session attribution off.
//
// projectCfg is passed in rather than loaded per call: this runs once per
// active recording row, and ten agents in a repo would otherwise mean ten
// redundant project-config reads for one status invocation.
func conversationURLForState(projectCfg *config.ProjectConfig, attributionOn bool, s *session.RecordingState) string {
	if !attributionOn || projectCfg == nil || s == nil || s.SessionID == "" {
		return ""
	}
	return buildConversationURL(projectCfg, s.SessionID)
}

// draftStatusContext is the per-invocation config snapshot shared by every
// recording row, so `ox session status` reads each config file once rather than
// once per active agent.
type draftStatusContext struct {
	resolved      *config.ResolvedSessionDraft
	projectCfg    *config.ProjectConfig
	attributionOn bool
}

func newDraftStatusContext() draftStatusContext {
	projectCfg, _ := config.LoadProjectConfig("")
	return draftStatusContext{
		resolved:      config.ResolveSessionDraft(),
		projectCfg:    projectCfg,
		attributionOn: loadResolvedAttribution().Session != "",
	}
}

// applyTo fills the draft observability fields on any row-like target.
func (c draftStatusContext) applyTo(s *session.RecordingState, turnCount *int, state *string, publishedAt *string, url *string) {
	if s == nil {
		return
	}
	*turnCount = s.TurnCount
	*state = draftStateFor(c.resolved, s)
	if s.DraftPublishedAt != nil {
		*publishedAt = s.DraftPublishedAt.Format("2006-01-02T15:04:05Z07:00")
	}
	*url = conversationURLForState(c.projectCfg, c.attributionOn, s)
}

func init() {
	sessionCmd.AddCommand(sessionStatusCmd)
	sessionStatusCmd.Flags().Bool("current", false, "only show this agent's recording (uses SAGEOX_AGENT_ID)")
}

func runSessionStatus(cmd *cobra.Command, args []string) error {
	jsonOutput, _ := cmd.Root().PersistentFlags().GetBool("json")
	currentOnly, _ := cmd.Flags().GetBool("current")

	projectRoot, err := requireProjectRoot()
	if err != nil {
		if jsonOutput {
			return outputJSON(cmd.OutOrStdout(), sessionStatusOutput{Recording: false})
		}
		return err
	}

	// load all recording states
	states, err := session.LoadAllRecordingStates(projectRoot)
	if err != nil {
		if jsonOutput {
			return outputJSON(cmd.OutOrStdout(), sessionStatusOutput{Recording: false})
		}
		return fmt.Errorf("failed to check recording state: %w", err)
	}

	// filter to current agent if --current
	if currentOnly {
		agentID := os.Getenv("SAGEOX_AGENT_ID")
		if agentID == "" {
			if jsonOutput {
				return outputJSON(cmd.OutOrStdout(), sessionStatusOutput{Recording: false})
			}
			return fmt.Errorf("--current requires SAGEOX_AGENT_ID environment variable (set by 'ox agent prime')")
		}
		var filtered []*session.RecordingState
		for _, s := range states {
			if s.AgentID == agentID {
				filtered = append(filtered, s)
			}
		}
		states = filtered
	}

	// build agent liveness map: agent_id → alive (true/false)
	// Three sources: recording state PID, disk instance store PID, and daemon heartbeats.
	agentAlive := resolveAgentLiveness(projectRoot, states)

	// no recordings
	if len(states) == 0 {
		if jsonOutput {
			return outputJSON(cmd.OutOrStdout(), sessionStatusOutput{
				Recording: false,
				Guidance:  "Run 'ox agent <id> session start' to begin recording",
			})
		}
		fmt.Println(cli.StyleDim.Render("Not recording"))
		fmt.Println()
		fmt.Println("Run 'ox agent <id> session start' to begin recording")
		return nil
	}

	// single recording — backward-compatible output
	if len(states) == 1 {
		state := states[0]
		duration := state.Duration()
		durationStr := formatDurationHuman(duration)
		alive, status := agentLivenessFor(agentAlive, state.AgentID)

		stalled, stalledReason := sessionStallStatus(state)
		lastHookAtStr := ""
		if state.LastHookAt != nil {
			lastHookAtStr = state.LastHookAt.Format("2006-01-02T15:04:05Z07:00")
		}

		if jsonOutput {
			output := sessionStatusOutput{
				Recording:       true,
				AgentAlive:      agentAlivePtr(state),
				Guidance:        "Run 'ox agent <id> session stop' to save the recording",
				Count:           1,
				Title:           state.Title,
				DurationSecs:    int(duration.Seconds()),
				Duration:        durationStr,
				EntryCount:      state.EntryCount,
				FilterMode:      state.FilterMode,
				Model:           state.Model,
				Agent:           state.AdapterName,
				AgentID:         state.AgentID,
				ProcessAlive:    alive,
				ProcessStatus:   status,
				SessionFile:     state.SessionFile,
				StartedAt:       state.StartedAt.Format("2006-01-02T15:04:05Z07:00"),
				WorkspacePath:   state.WorkspacePath,
				Branch:          state.Branch,
				HookInvocations: state.HookInvocations,
				LastHookStatus:  state.LastHookStatus,
				LastHookAt:      lastHookAtStr,
				Stalled:         stalled,
				StalledReason:   stalledReason,
			}
			// ADR-020: surface pause state in status JSON
			if state.SuspendedAt != nil {
				output.Suspended = true
				output.SuspendedAt = state.SuspendedAt.Format("2006-01-02T15:04:05Z07:00")
				output.SuspendedDuration = formatPausedDuration(time.Since(*state.SuspendedAt))
				output.PauseCount = state.PauseCount
				output.InheritedPause = state.InheritedPause
				output.InheritedFromSession = state.InheritedFromSession
				output.Guidance = "Recording is SUSPENDED. Resume with `ox session resume` or stop with `ox session stop`."
			}
			// ADR-029: surface draft-placeholder state so "my /c/ link 404s"
			// is diagnosable rather than a guess.
			newDraftStatusContext().applyTo(state, &output.TurnCount, &output.DraftState,
				&output.DraftPublishedAt, &output.ConversationURL)
			return outputJSON(cmd.OutOrStdout(), output)
		}

		if !state.IsAgentAlive() {
			fmt.Println(cli.StyleWarning.Render("Recording in progress (agent exited)"))
		} else {
			fmt.Println(cli.StyleSuccess.Render("Recording in progress"))
		}
		fmt.Println()
		if state.Title != "" {
			fmt.Printf("  Title:    %s\n", state.Title)
		}
		fmt.Printf("  Duration: %s\n", durationStr)
		rawEntries := -1
		if state.SessionPath != "" {
			rawEntries = countRawEntries(filepath.Join(state.SessionPath, "raw.jsonl"))
		}
		if rawEntries >= 0 && rawEntries != state.EntryCount {
			fmt.Printf("  Entries:  %d %s\n", state.EntryCount,
				cli.StyleWarning.Render(fmt.Sprintf("(raw.jsonl has %d — state may be stale)", rawEntries)))
		} else {
			fmt.Printf("  Entries:  %d\n", state.EntryCount)
		}
		fmt.Printf("  Agent:    %s\n", state.AdapterName)
		fmt.Printf("  Started:  %s\n", state.StartedAt.Format("15:04:05"))
		if state.AgentID != "" {
			fmt.Printf("  Agent ID: %s\n", state.AgentID)
		}
		fmt.Printf("  Process:  %s\n", formatProcessStatus(status))
		if state.HookInvocations > 0 || state.LastHookStatus != "" {
			fmt.Printf("  Hooks:    %d invocations", state.HookInvocations)
			if state.LastHookStatus != "" {
				fmt.Printf(", last=%s", state.LastHookStatus)
			}
			fmt.Println()
		}
		if state.WorkspacePath != "" {
			fmt.Printf("  Workspace: %s\n", state.WorkspacePath)
		}
		if state.Branch != "" {
			fmt.Printf("  Branch:   %s\n", state.Branch)
		}
		fmt.Println()
		if stalled {
			fmt.Println(cli.StyleWarning.Render("⚠ Recording is stalled: " + stalledReason))
			fmt.Println(cli.StyleDim.Render("  Run 'ox doctor' to diagnose."))
		} else if status == "dead" {
			fmt.Println(cli.StyleWarning.Render("Agent process is no longer running. Run 'ox agent <id> session stop' to finalize."))
		} else {
			fmt.Println(cli.StyleDim.Render("Run 'ox agent <id> session stop' to save the recording"))
		}
		return nil
	}

	// multiple recordings
	if jsonOutput {
		// Loaded once for the whole invocation, not per row.
		draftCtx := newDraftStatusContext()
		var entries []sessionRecordingEntry
		for _, s := range states {
			alive, status := agentLivenessFor(agentAlive, s.AgentID)
			entries = append(entries, buildSessionRecordingEntry(s, alive, status, draftCtx))
		}
		return outputJSON(cmd.OutOrStdout(), sessionStatusOutput{
			Recording: true,
			Guidance:  "Use --current to filter to this agent's recording",
			Count:     len(states),
			Sessions:  entries,
		})
	}

	// text output for multiple recordings
	fmt.Println(cli.StyleSuccess.Render(fmt.Sprintf("%d recordings in progress", len(states))))
	fmt.Println()

	for i, state := range states {
		if i > 0 {
			fmt.Println()
		}
		durationStr := formatDurationHuman(state.Duration())
		_, status := agentLivenessFor(agentAlive, state.AgentID)

		label := state.AgentID
		if label == "" {
			label = state.AdapterName
		}
		statusTag := formatProcessStatus(status)
		fmt.Printf("  %s %s %s\n", cli.StyleBold.Render(label), statusTag, cli.StyleDim.Render(fmt.Sprintf("(%s, %d entries)", durationStr, state.EntryCount)))

		if state.Title != "" {
			fmt.Printf("    Title:   %s\n", state.Title)
		}
		fmt.Printf("    Agent:   %s\n", state.AdapterName)
		fmt.Printf("    Started: %s\n", state.StartedAt.Format("15:04:05"))
		if state.WorkspacePath != "" {
			fmt.Printf("    Workspace: %s\n", state.WorkspacePath)
		}
		if state.Branch != "" {
			fmt.Printf("    Branch:  %s\n", state.Branch)
		}
	}

	fmt.Println()
	fmt.Println(cli.StyleDim.Render("Use --current to filter to this agent's recording"))

	return nil
}

// resolveAgentLiveness builds a map of agent_id → alive status.
// Uses three sources: recording state PID (direct), disk instance store PID,
// and daemon heartbeats. Returns nil map if no liveness info is available.
func resolveAgentLiveness(projectRoot string, states []*session.RecordingState) map[string]bool {
	if len(states) == 0 {
		return nil
	}

	result := make(map[string]bool)

	// collect agent IDs we care about
	agentIDs := make(map[string]bool)
	for _, s := range states {
		if s.AgentID != "" {
			agentIDs[s.AgentID] = true
		}
	}

	// source 1: recording state PID — direct from .recording.json
	for _, s := range states {
		if s.AgentID != "" && s.ParentPID > 0 {
			result[s.AgentID] = s.IsAgentAlive()
		}
	}

	// source 2: disk instance store — PID-based liveness check
	store, err := getInstanceStore(projectRoot)
	if err == nil {
		for agentID := range agentIDs {
			if _, known := result[agentID]; known {
				continue // already resolved from recording state
			}
			inst, err := store.Get(agentID)
			if err != nil {
				continue
			}
			result[agentID] = inst.IsProcessAlive()
		}
	}

	// source 3: daemon heartbeat instances — recent heartbeat means alive
	if client := daemon.TryConnect(); client != nil {
		instances, err := client.Instances()
		if err == nil {
			for _, inst := range instances {
				if !agentIDs[inst.AgentID] {
					continue
				}
				if inst.Status == daemon.StatusActive {
					result[inst.AgentID] = true
				}
			}
		}
	}

	return result
}

// agentLivenessFor returns the process_alive pointer and status string for a given agent.
func agentLivenessFor(livenessMap map[string]bool, agentID string) (*bool, string) {
	if agentID == "" || livenessMap == nil {
		return nil, "unknown"
	}
	alive, known := livenessMap[agentID]
	if !known {
		return nil, "unknown"
	}
	status := "dead"
	if alive {
		status = "alive"
	}
	return &alive, status
}

// agentAlivePtr returns a *bool indicating agent liveness from PID.
// Returns nil if no PID is recorded (backward compat with old recordings).
func agentAlivePtr(state *session.RecordingState) *bool {
	if state == nil || state.ParentPID <= 0 {
		return nil
	}
	alive := state.IsAgentAlive()
	return &alive
}

// formatProcessStatus returns a styled string for process status display.
func formatProcessStatus(status string) string {
	switch status {
	case "alive":
		return cli.StyleSuccess.Render("alive")
	case "dead":
		return cli.StyleWarning.Render("dead")
	default:
		return cli.StyleDim.Render("unknown")
	}
}

// outputJSON writes JSON to w with 2-space indentation.
func outputJSON(w io.Writer, v any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}
