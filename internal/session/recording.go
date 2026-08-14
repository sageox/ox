package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/sessionid"
)

var (
	// ErrNotRecording is returned when stop is called but no recording is active
	ErrNotRecording = errors.New("not currently recording")

	// ErrAlreadyRecording is returned when start is called but already recording
	ErrAlreadyRecording = errors.New("already recording a session")

	// ErrNoLedger is returned when session recording is attempted but no ledger is configured
	ErrNoLedger = errors.New("no ledger configured for this project")
)

const recordingFile = ".recording.json"

// LifecycleAction is the durable timeline action recorded in RecordingState.Lifecycle.
// See ADR-019 (session entity lifecycle).
type LifecycleAction string

const (
	LifecycleActionStart         LifecycleAction = "start"
	LifecycleActionPause         LifecycleAction = "pause"
	LifecycleActionResume        LifecycleAction = "resume"
	LifecycleActionStop          LifecycleAction = "stop"
	LifecycleActionClearFinalize LifecycleAction = "clear-finalize"
	LifecycleActionExpired       LifecycleAction = "expired"
)

// LifecycleEvent is one entry in the durable session-entity timeline. The
// timeline is the single source of truth for what regions of raw.jsonl are
// excluded from upload (see ADR-020). At upload time, paired
// (pause, resume) ranges become redaction rules.
//
// Seq is the raw.jsonl entry sequence number at the moment of the event. It
// is the universal cursor — every adapter writes entries in monotonic seq
// order regardless of its native cursor type (byte offset, entry count, ULID).
// Offset is a diagnostic for native adapter cursors.
type LifecycleEvent struct {
	Action LifecycleAction `json:"action"`
	At     time.Time       `json:"at"`
	Seq    int             `json:"seq,omitempty"`
	Offset int64           `json:"offset,omitempty"`
	Reason string          `json:"reason,omitempty"`
}

// RecordingState tracks an active recording session.
// Stored in sessions/<session-name>/.recording.json
type RecordingState struct {
	AgentID string `json:"agent_id"`
	// SessionID is the durable ses_<UUIDv7> recording identity, minted once at
	// StartRecording and reused verbatim by every finalize path (stop, recover,
	// daemon). It exists from t=0 so conversation URLs (/c/<ses_id>) circulated
	// during the live session — commit trailers, PR bodies, plan footers —
	// keep resolving after upload. omitempty: recordings started under an
	// older binary round-trip with an empty ID and fall back to name-based
	// URLs.
	SessionID        string    `json:"session_id,omitempty"`
	StartedAt        time.Time `json:"started_at"`
	AdapterName      string    `json:"adapter_name"`
	SessionFile      string    `json:"session_file"` // source file from adapter (Claude Code JSONL)
	OutputFile       string    `json:"output_file"`  // output file being recorded
	SessionPath      string    `json:"session_path"` // path to session folder
	Title            string    `json:"title,omitempty"`
	EntryCount       int       `json:"entry_count"`
	LastReminderSeq  int       `json:"last_reminder_seq"`
	ReminderInterval int       `json:"reminder_interval"`
	FilterMode       string    `json:"filter_mode,omitempty"`    // "infra" or "all" - controls event filtering
	WorkspacePath    string    `json:"workspace_path,omitempty"` // git root / project directory
	Branch           string    `json:"branch,omitempty"`         // git branch at recording start

	// Parent-child session tracking for subagent workflows
	// When a parent spawns subagents, each subagent can report its session
	// back to the parent session for aggregation.
	ParentSessionPath string `json:"parent_session_path,omitempty"` // path to parent's session folder
	ParentAgentID     string `json:"parent_agent_id,omitempty"`     // parent's agent ID (e.g., "Oxa7b3")

	AgentType      string `json:"agent_type,omitempty"`      // original agent type for metadata: "codex", "amp", etc. Falls back to AdapterName if empty.
	StopIncomplete bool   `json:"stop_incomplete,omitempty"` // set when stop returned retry guidance (empty file)
	Model          string `json:"model,omitempty"`           // LLM model for generic adapters where ReadMetadata returns nil
	ParentPID      int    `json:"parent_pid,omitempty"`      // parent agent process ID for liveness detection
	SourceOffset   int64  `json:"source_offset,omitempty"`   // byte offset in source file for incremental reading
	StartOffset    int64  `json:"start_offset,omitempty"`    // source file byte offset when recording started (entries before this are pre-session)
	Origin         string `json:"origin,omitempty"`          // session origin: "human", "subagent", "agent" (from agentx.DetectOrigin)
	CacheDir       string `json:"cache_dir,omitempty"`       // cache directory when recording was created (diagnostic breadcrumb)

	WatchMode string     `json:"watch_mode,omitempty"` // how entries are captured: "hook" (CLI-driven) or "tail" (daemon-driven)
	StoppedAt *time.Time `json:"stopped_at,omitempty"` // set by ox session stop to signal daemon to finalize

	// ADR-020 session pause/resume fields. Lifecycle is the durable timeline of
	// session-entity transitions and is the source of truth for which raw.jsonl
	// seq ranges are excluded from upload. SuspendedAt is the hot-path "is the
	// session currently paused" check; cleared on resume/stop/abort. PauseCount
	// is a monotonic counter for observability. InheritedPause + InheritedFromSession
	// trace a pause that was carried across a /clear boundary.
	Lifecycle            []LifecycleEvent `json:"lifecycle,omitempty"`
	SuspendedAt          *time.Time       `json:"suspended_at,omitempty"`
	PauseCount           int              `json:"pause_count,omitempty"`
	InheritedPause       bool             `json:"inherited_pause,omitempty"`
	InheritedFromSession string           `json:"inherited_from_session,omitempty"`

	// ProducedCommits is the reverse-direction index of commit SHAs authored
	// during this recording. Appended by the post-commit hook; rewritten
	// in place by the post-rewrite hook on amend/rebase. Folded into
	// SessionMeta.ProducedCommits at session stop / finalize. omitempty so
	// older .recording.json files round-trip unchanged.
	ProducedCommits []string `json:"produced_commits,omitempty"`

	// ProducedPlans is the reverse-direction index of plan slugs captured
	// during this recording (e.g. by `ox plan` auto-save). Appended at
	// plan-save time, folded into SessionMeta.ProducedPlans at session stop /
	// finalize — mirrors ProducedCommits. omitempty so older .recording.json
	// files round-trip unchanged.
	ProducedPlans []string `json:"produced_plans,omitempty"`

	// LinkedPRs / LinkedIssues are the GitHub PR and issue references this
	// recording is associated with. Appended by the pre-push hook from the
	// pushed commit range, folded into SessionMeta at stop. omitempty for
	// round-trip with older .recording.json files.
	LinkedPRs    []string `json:"linked_prs,omitempty"`
	LinkedIssues []string `json:"linked_issues,omitempty"`

	// LinkageStatus tracks the upload/notify lifecycle for PR/issue linkage
	// during the active recording. Mirrors lfs.LinkageStatus* values; folded
	// into SessionMeta.LinkageStatus at stop. omitempty for round-trip.
	LinkageStatus string `json:"linkage_status,omitempty"`

	// LifecycleRegistrationState distinguishes a server-confirmed /c link from
	// a locally minted identifier. Recording remains local-first, but callers
	// must not circulate a link the server rejected or never observed.
	LifecycleRegistrationState string `json:"lifecycle_registration_state,omitempty"` // confirmed | pending
	LifecycleRegistrationError string `json:"lifecycle_registration_error,omitempty"`

	// Hook observability: lets `ox session status` show whether hooks are firing
	// and why they're skipping. Without these, a broken recording path (e.g.
	// adapter binary missing, session file not discoverable) looks identical to
	// a healthy idle session — both show EntryCount=0 with no signal of cause.
	HookInvocations int        `json:"hook_invocations,omitempty"` // total afterTool hook calls since recording started
	LastHookStatus  string     `json:"last_hook_status,omitempty"` // stable reason code from last afterTool: "ok", "session-file-not-found", "read-error", etc.
	LastHookAt      *time.Time `json:"last_hook_at,omitempty"`     // timestamp of last afterTool invocation

	// TurnCount is the number of agent response turns observed for this
	// recording. Incremented exactly once per Stop-hook invocation: the Stop
	// phase means "the agent finished responding" and fires once per completed
	// turn, which makes it the only real turn signal ox has.
	//
	// It is NOT EntryCount. EntryCount counts raw.jsonl JSONL entries, of which
	// a single turn can produce dozens. It is also NOT HookInvocations, which
	// counts afterTool calls (i.e. tool uses). Conflating any two of these
	// three produces a draft that publishes on the wrong turn.
	//
	// The increment MUST live in the Stop handler and never in the afterTool
	// handler: afterTool also runs on PostToolUse, so counting there counts
	// tool calls. omitempty so .recording.json files written by older binaries
	// round-trip unchanged.
	TurnCount int `json:"turn_count,omitempty"`

	// DraftPublishedAt is set only after a draft placeholder has actually been
	// handed off — the daemon accepted the IPC, or the local commit landed.
	// Nil alongside a non-zero DraftPublishedTurn is the "we tried and it
	// failed" signal that `ox doctor` reports; without the pair, a silently
	// failed publish is indistinguishable from a server that is merely behind.
	DraftPublishedAt *time.Time `json:"draft_published_at,omitempty"`

	// DraftPublishedTurn is the TurnCount at the last SUCCESSFUL publish or
	// refresh. The refresh cadence is measured from it, so a failed refresh
	// does not reset the interval.
	DraftPublishedTurn int `json:"draft_published_turn,omitempty"`

	// DraftAttemptTurn is the TurnCount at the last publish or refresh ATTEMPT,
	// successful or not. Kept separate from DraftPublishedTurn because the two
	// answer different questions and conflating them hid a real failure:
	// DraftPublishedAt is only ever set, never cleared, so a session whose
	// first publish succeeded and whose every later refresh fails would report
	// itself as healthy forever. AttemptTurn > PublishedTurn is the durable
	// "the most recent attempt did not land" signal that `ox doctor` and
	// `ox session status` report.
	DraftAttemptTurn int `json:"draft_attempt_turn,omitempty"`
}

// Duration returns how long the recording has been running.
func (r *RecordingState) Duration() time.Duration {
	if r == nil || r.StartedAt.IsZero() {
		return 0
	}
	return time.Since(r.StartedAt)
}

// IsAgentAlive checks if the recording agent's parent process is still running.
// Uses kill(pid, 0) for instant liveness detection.
// Returns true if no PID is recorded (assume alive for backward compat).
func (r *RecordingState) IsAgentAlive() bool {
	if r == nil || r.ParentPID <= 0 {
		return true // no PID recorded — assume alive
	}
	return isPIDAlive(r.ParentPID)
}

// IsSubagent returns true if this session was spawned by a parent agent.
// Checks both explicit parent tracking and environment-detected origin.
func (r *RecordingState) IsSubagent() bool {
	if r == nil {
		return false
	}
	return r.ParentSessionPath != "" || r.Origin == "subagent"
}

// recordingStatePath returns the path to .recording.json for the given session folder.
func recordingStatePath(sessionPath string) string {
	return filepath.Join(sessionPath, recordingFile)
}

// SaveRecordingState persists recording state to the session folder.
func SaveRecordingState(projectRoot string, state *RecordingState) error {
	if projectRoot == "" {
		return fmt.Errorf("%w: project root", ErrEmptyPath)
	}
	if state == nil {
		return ErrNilState
	}
	if state.SessionPath == "" {
		return fmt.Errorf("%w: session path", ErrEmptyPath)
	}

	// ensure session directory exists
	if err := os.MkdirAll(state.SessionPath, 0755); err != nil {
		return fmt.Errorf("create session dir=%s: %w", state.SessionPath, err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal recording state: %w", err)
	}

	// TODO(server-side): move to server-side for MVP+1; client should not write to ledger directly.
	statePath := recordingStatePath(state.SessionPath)
	if err := os.WriteFile(statePath, data, 0600); err != nil {
		return fmt.Errorf("write recording state file=%s: %w", statePath, err)
	}

	return nil
}

// LoadRecordingState loads active recording state by searching for .recording.json
// in session folders under the sessions directory.
// Returns nil, nil if no recording state exists.
func LoadRecordingState(projectRoot string) (*RecordingState, error) {
	if projectRoot == "" {
		return nil, fmt.Errorf("%w: project root", ErrEmptyPath)
	}

	for _, sessionsDir := range sessionsSearchPaths(projectRoot) {
		entries, err := os.ReadDir(sessionsDir)
		if err != nil {
			continue // directory doesn't exist, try next
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			recordingPath := filepath.Join(sessionsDir, entry.Name(), recordingFile)
			data, err := os.ReadFile(recordingPath)
			if err != nil {
				continue // no .recording.json in this session folder
			}

			var state RecordingState
			if err := json.Unmarshal(data, &state); err != nil {
				continue // invalid JSON, skip
			}

			return &state, nil
		}
	}

	return nil, nil // no recording state found
}

// LoadAllRecordingStates returns all active recording states by searching for
// .recording.json in session folders. Unlike LoadRecordingState which returns
// only the first match, this returns all concurrent recordings (e.g., from
// multiple worktrees or agents).
func LoadAllRecordingStates(projectRoot string) ([]*RecordingState, error) {
	if projectRoot == "" {
		return nil, fmt.Errorf("%w: project root", ErrEmptyPath)
	}

	seen := make(map[string]struct{}) // deduplicate by canonical recording file path
	var states []*RecordingState

	for _, sessionsDir := range sessionsSearchPaths(projectRoot) {
		entries, err := os.ReadDir(sessionsDir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			recordingPath := filepath.Join(sessionsDir, entry.Name(), recordingFile)
			canonicalKey := recordingPath
			if resolved, err := filepath.EvalSymlinks(recordingPath); err == nil {
				canonicalKey = resolved
			}
			if _, ok := seen[canonicalKey]; ok {
				continue
			}

			data, err := os.ReadFile(recordingPath)
			if err != nil {
				continue
			}

			var state RecordingState
			if err := json.Unmarshal(data, &state); err != nil {
				continue
			}

			seen[canonicalKey] = struct{}{}
			states = append(states, &state)
		}
	}

	return states, nil
}

// LoadRecordingStateForAgent loads recording state for a specific agent.
// Returns nil, nil if no recording for that agent exists.
// Use this instead of LoadRecordingState when you have an agent ID to avoid
// accidentally operating on another concurrent agent's recording.
func LoadRecordingStateForAgent(projectRoot, agentID string) (*RecordingState, error) {
	if agentID == "" {
		return nil, fmt.Errorf("%w: agent ID", ErrEmptyPath)
	}
	states, err := LoadAllRecordingStates(projectRoot)
	if err != nil {
		return nil, err
	}
	for _, s := range states {
		if s.AgentID == agentID {
			if s.CacheDir != "" && s.CacheDir != paths.CacheDir() {
				slog.Debug("recording found in different cache dir",
					"agent_id", agentID,
					"recording_cache_dir", s.CacheDir,
					"current_cache_dir", paths.CacheDir(),
				)
			}
			return s, nil
		}
	}
	return nil, nil
}

// LoadRecordingStateForWorkspace finds the recording whose WorkspacePath
// matches the given workspace. Returns nil, nil if no match.
// Use this for human (non-agent) commits where SAGEOX_AGENT_ID is not set —
// it avoids picking a random session from a different worktree.
// Resolves symlinks before comparing to handle macOS /tmp→/private/tmp
// and git worktree symlink differences.
func LoadRecordingStateForWorkspace(projectRoot, workspace string) (*RecordingState, error) {
	if workspace == "" {
		return nil, fmt.Errorf("%w: workspace", ErrEmptyPath)
	}
	// resolve symlinks for the lookup key (macOS: /tmp → /private/tmp)
	resolvedWorkspace := workspace
	if r, err := filepath.EvalSymlinks(workspace); err == nil {
		resolvedWorkspace = r
	}
	states, err := LoadAllRecordingStates(projectRoot)
	if err != nil {
		return nil, err
	}
	for _, s := range states {
		candidate := s.WorkspacePath
		if r, err := filepath.EvalSymlinks(candidate); err == nil {
			candidate = r
		}
		if candidate == resolvedWorkspace {
			return s, nil
		}
	}
	return nil, nil
}

// IsRecordingForAgent checks if a specific agent has an active recording.
func IsRecordingForAgent(projectRoot, agentID string) bool {
	state, _ := LoadRecordingStateForAgent(projectRoot, agentID)
	return state != nil
}

// ClearRecordingStateForAgent removes recording state for a specific agent only.
// Safe for concurrent use: only touches this agent's .recording.json.
func ClearRecordingStateForAgent(projectRoot, agentID string) error {
	state, err := LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil {
		return err
	}
	if state == nil {
		return nil // idempotent: nothing to clear
	}
	statePath := recordingStatePath(state.SessionPath)
	if err := os.Remove(statePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove recording state file=%s: %w", statePath, err)
	}
	return nil
}

// AppendProducedPlan records a plan slug on the recording at sessionPath — the
// reverse-direction index folded into SessionMeta.ProducedPlans at stop. Dedups
// by slug. Best-effort by contract: a session whose recording has already been
// stopped is a no-op, not an error.
//
// Keyed on sessionPath rather than an agent id so a caller that resolved its
// recording by workspace (no SAGEOX_AGENT_ID) can still record the link, and so
// the forward and reverse links always name the same session.
//
// The load happens HERE, immediately before the write, and that placement is
// load-bearing twice over:
//
//   - Resurrection. SaveRecordingState does an unconditional MkdirAll+write, so
//     handing it a state captured earlier RECREATES a .recording.json that
//     `ox session stop` deleted in the meantime. The revived recording is
//     permanent — ghost cleanup skips sessions whose raw.jsonl is an LFS
//     pointer, which is exactly what a finalized session has — and StartRecording
//     then refuses that agent forever with ErrAlreadyRecording. Reading the file
//     first and treating "not there" as done makes that unrepresentable.
//   - Lost update. .recording.json is a whole-file JSON rewrite shared with the
//     hook path (EntryCount, SourceOffset, TurnCount, ProducedCommits, LinkedPRs).
//     Writing back a struct loaded before an intervening plan save reverts every
//     field a concurrent hook touched, and because the ledger auto-resolves
//     sessions/ conflicts to the local side, that stripping propagates to origin
//     and erases the fields for the whole team — the failure `make
//     check-session-meta-rmw` already guards against on sessions/*/meta.json
//     (ox-q42i, GH #710). Reload-then-write keeps the window at ~0, matching
//     UpdateRecordingStateForAgent and every other writer in the codebase.
func AppendProducedPlan(projectRoot, sessionPath, slug string) error {
	if projectRoot == "" {
		return fmt.Errorf("%w: project root", ErrEmptyPath)
	}
	if sessionPath == "" || slug == "" {
		return nil
	}

	statePath := recordingStatePath(sessionPath)
	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // recording already stopped — do NOT recreate it
		}
		return fmt.Errorf("read recording state file=%s: %w", statePath, err)
	}

	var state RecordingState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("parse recording state file=%s: %w", statePath, err)
	}

	for _, existing := range state.ProducedPlans {
		if existing == slug {
			return nil // already recorded
		}
	}
	state.ProducedPlans = append(state.ProducedPlans, slug)
	return SaveRecordingState(projectRoot, &state)
}

// ClearRecordingState removes the recording state file from the session folder.
// Note: returns first-match state. Use ClearRecordingStateForAgent in agent-context
// code to avoid clearing another concurrent agent's recording.
func ClearRecordingState(projectRoot string) error {
	if projectRoot == "" {
		return fmt.Errorf("%w: project root", ErrEmptyPath)
	}

	// load the state to find the session path
	state, err := LoadRecordingState(projectRoot)
	if err != nil {
		return fmt.Errorf("load recording state: %w", err)
	}
	if state == nil {
		return nil // no state to clear
	}

	// remove .recording.json from session folder
	if state.SessionPath != "" {
		statePath := recordingStatePath(state.SessionPath)
		if err := os.Remove(statePath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove recording state file=%s: %w", statePath, err)
		}

		// clean up any stale .lock files left by crashed session log processes
		lockFiles, _ := filepath.Glob(filepath.Join(state.SessionPath, "*.lock"))
		for _, lockFile := range lockFiles {
			_ = os.Remove(lockFile)
		}
	}

	return nil
}

// IsRecording checks if a recording is active for the given project root.
func IsRecording(projectRoot string) bool {
	state, err := LoadRecordingState(projectRoot)
	return err == nil && state != nil
}

// resolveSessionsWritePath returns the single canonical directory for writing
// session data (markers, recording state). Prefers ledger cache, falls back to
// XDG cache. Returns "" if no writable path can be resolved.
//
// This must NEVER return a path inside the user's repository.
func resolveSessionsWritePath(projectRoot string) string {
	repoID := getRepoIDFromProject(projectRoot)
	if repoID != "" {
		ep := getProjectEndpoint(projectRoot)
		if ep != "" {
			ledgerBase := paths.LedgerSessionCacheBase(repoID, ep)
			if ledgerBase != "" {
				return filepath.Join(ledgerBase, "sessions")
			}
		}
		contextPath := GetContextPath(repoID)
		if contextPath != "" {
			return filepath.Join(contextPath, "sessions")
		}
	}
	return ""
}

const explicitStopMarker = ".session_stopped"

// MarkExplicitStop writes a per-agent breadcrumb indicating the user explicitly
// stopped recording. This prevents the next auto-start cycle (e.g. from /clear
// hook re-prime) from silently restarting the session for this specific agent.
//
// Writes to the canonical sessions directory (ledger cache or XDG cache),
// never into the user's repository.
func MarkExplicitStop(projectRoot, agentID string) error {
	if projectRoot == "" {
		return fmt.Errorf("%w: project root", ErrEmptyPath)
	}
	if agentID == "" {
		return fmt.Errorf("agentID must not be empty")
	}

	// Resolve canonical write path — same logic as StartRecording.
	dir := resolveSessionsWritePath(projectRoot)
	if dir == "" {
		return fmt.Errorf("could not write explicit stop marker for project=%s agent=%s: no sessions directory", projectRoot, agentID)
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create sessions dir for marker: %w", err)
	}

	marker := explicitStopMarker + "." + agentID
	markerPath := filepath.Join(dir, marker)
	if err := os.WriteFile(markerPath, []byte(time.Now().UTC().Format(time.RFC3339)), 0600); err != nil {
		return fmt.Errorf("write explicit stop marker: %w", err)
	}
	return nil
}

// ConsumeExplicitStop checks for and removes the per-agent explicit-stop marker.
// Returns true if the marker existed (meaning an auto-start should be skipped).
// Also cleans up any legacy global marker (without agent suffix) as a migration path.
func ConsumeExplicitStop(projectRoot, agentID string) bool {
	if projectRoot == "" {
		return false
	}
	if agentID == "" {
		return false
	}
	found := false
	marker := explicitStopMarker + "." + agentID
	for _, dir := range sessionsSearchPaths(projectRoot) {
		// check per-agent marker
		markerPath := filepath.Join(dir, marker)
		if err := os.Remove(markerPath); err == nil {
			found = true
		}
		// clean up legacy global marker (backward compat migration)
		legacyPath := filepath.Join(dir, explicitStopMarker)
		_ = os.Remove(legacyPath)
	}
	return found
}

// sessionsSearchPaths returns the sessions directory paths to search for reads.
// Used by LoadRecordingState, ConsumeExplicitStop, and cleanup functions.
//
// IMPORTANT: This function is for READ paths only. Write operations (StartRecording,
// MarkExplicitStop) must resolve their own canonical write path — never iterate
// these search paths to find a writable location, as that was the source of the
// bug where session files leaked into the user's repository at projectRoot/sessions/.
//
// Includes alternate cache locations because processes with different
// XDG_CACHE_HOME values (e.g., Conductor GUI vs terminal shell) may
// create recording states in different directories.
func sessionsSearchPaths(projectRoot string) []string {
	var searchPaths []string
	repoID := getRepoIDFromProject(projectRoot)
	if repoID != "" {
		// ledger cache — canonical location, environment-independent.
		// Only include when project has an explicit endpoint configured
		// (avoids using default endpoint for test projects or uninitialized repos).
		ep := getProjectEndpoint(projectRoot)
		if ep != "" {
			ledgerBase := paths.LedgerSessionCacheBase(repoID, ep)
			if ledgerBase != "" {
				searchPaths = append(searchPaths, filepath.Join(ledgerBase, "sessions"))
			}
		}
		// XDG cache — current process's resolved path
		contextPath := GetContextPath(repoID)
		if contextPath != "" {
			searchPaths = append(searchPaths, filepath.Join(contextPath, "sessions"))
		}
		// alternate XDG cache dirs — other processes may have used different paths
		for _, altDir := range paths.AlternateSessionCacheDirs(repoID) {
			searchPaths = append(searchPaths, filepath.Join(altDir, "sessions"))
		}
	}

	// Legacy fallback: older ox versions wrote session data into the user's repo
	// at projectRoot/sessions/. Include as a read-only fallback so in-flight
	// sessions started on older versions can still be found and finalized.
	// This path must NEVER be used for writes — see MarkExplicitStop, StartRecording.
	legacyPath := filepath.Join(projectRoot, "sessions")
	if _, err := os.Stat(legacyPath); err == nil {
		searchPaths = append(searchPaths, legacyPath)
	}

	return searchPaths
}

// GetRecordingDuration returns how long the current recording has been running.
// Returns 0 if no recording is active.
func GetRecordingDuration(projectRoot string) time.Duration {
	state, err := LoadRecordingState(projectRoot)
	if err != nil || state == nil {
		return 0
	}
	return time.Since(state.StartedAt)
}

// staleEmptyThreshold defines how long an empty recording stub must exist
// before it's eligible for automatic cleanup. Set to 48 hours because coding
// sessions can run 12+ hours and raw.jsonl isn't written until session stop.
const staleEmptyThreshold = 48 * time.Hour

// GhostGracePeriod is the minimum recording age before ghost cleanup will remove it,
// even with a dead PID. Handles the race where FindAgentAncestorPID() returns
// a transient shell PID that dies immediately after session start.
const GhostGracePeriod = 10 * time.Minute

// cleanupStaleEmptyRecordings removes stale recording stubs that have no session
// content (no raw.jsonl). These accumulate when agents start sessions but exit
// without calling session stop. Best-effort: errors are logged but not returned.
func cleanupStaleEmptyRecordings(projectRoot string) {
	states, err := LoadAllRecordingStates(projectRoot)
	if err != nil {
		return
	}

	for _, state := range states {
		if time.Since(state.StartedAt) < staleEmptyThreshold {
			continue
		}
		if state.SessionPath == "" {
			continue
		}
		// only clean empty stubs (no raw.jsonl)
		rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
		if _, err := os.Stat(rawPath); err == nil {
			continue // has content, don't auto-delete
		} else if !os.IsNotExist(err) {
			slog.Debug("cleanup stale empty recording: stat error", "path", rawPath, "error", err)
			continue // transient/permission error, skip
		}
		// remove .recording.json
		recPath := recordingStatePath(state.SessionPath)
		if err := os.Remove(recPath); err != nil && !os.IsNotExist(err) {
			slog.Debug("cleanup stale empty recording", "path", recPath, "error", err)
			continue
		}
		// remove empty session directory
		removeEmptyDir(state.SessionPath)
		slog.Debug("cleaned stale empty recording", "session", filepath.Base(state.SessionPath))
	}
}

// GhostCleanupResult reports what was cleaned up.
type GhostCleanupResult struct {
	Removed int      // number of ghost sessions removed
	Names   []string // session folder names that were removed
}

// CleanupGhostSessions removes abandoned recording stubs where the parent process
// is dead and no meaningful data was captured. Uses PID liveness for instant
// detection — no time threshold needed.
//
// Ghost = .recording.json exists + parent PID dead + no substantive raw.jsonl.
// Sessions with real data (raw.jsonl with entries) are NOT removed — those are
// orphans that need recovery, not cleanup.
//
// Safe to call from daemon anti-entropy, doctor --fix, or session start.
// Uses projectRoot to find session directories via sessionsSearchPaths.
func CleanupGhostSessions(projectRoot string) GhostCleanupResult {
	states, err := LoadAllRecordingStates(projectRoot)
	if err != nil {
		return GhostCleanupResult{}
	}
	return cleanupGhosts(states)
}

// CleanupGhostSessionsInDir removes ghost sessions from a specific sessions directory.
// Used by the daemon which has a ledgerPath rather than a projectRoot.
func CleanupGhostSessionsInDir(sessionsDir string) GhostCleanupResult {
	states, err := loadRecordingStatesFromDir(sessionsDir)
	if err != nil {
		return GhostCleanupResult{}
	}
	return cleanupGhosts(states)
}

// cleanupGhosts is the shared implementation for ghost session cleanup.
func cleanupGhosts(states []*RecordingState) GhostCleanupResult {
	var result GhostCleanupResult

	for _, state := range states {
		if state.SessionPath == "" {
			continue
		}

		// skip if parent PID is unknown — can't determine liveness
		if state.ParentPID <= 0 {
			continue
		}

		// skip if parent process is alive — still recording
		if state.IsAgentAlive() {
			continue
		}

		// grace period: don't remove young recordings even with dead PIDs.
		// FindAgentAncestorPID() can return a transient shell PID that dies
		// immediately; give the recording time to establish before cleanup.
		if !state.StartedAt.IsZero() && time.Since(state.StartedAt) < GhostGracePeriod {
			continue
		}

		// parent is dead — check if there's any recoverable data. Classify
		// rather than calling HasSubstantiveEntries, because "no substantive
		// entries" and "nothing to recover" are NOT the same thing here.
		//
		// Only a header-only or missing raw.jsonl is a ghost. A pointer stub
		// is a session whose transcript lives in the content store, so it has
		// very much got recoverable data — deleting it (and, via
		// removeEmptyDir below, its whole session directory) would destroy a
		// synced session. HasSubstantiveEntries reports false for pointers as
		// of GH #710, so this must not be collapsed back into that call.
		rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
		switch ClassifyRawFile(rawPath) {
		case RawSubstantive:
			// has session entries — this is an orphan, not a ghost.
			// don't delete: it has recoverable data.
			continue
		case RawPointerStub:
			// content lives in the content store; never a ghost.
			continue
		case RawMissing, RawHeaderOnly:
			// fall through to ghost cleanup
		}

		// ghost confirmed: dead PID, no meaningful data
		sessionName := filepath.Base(state.SessionPath)
		recPath := recordingStatePath(state.SessionPath)
		if err := os.Remove(recPath); err != nil && !os.IsNotExist(err) {
			slog.Debug("ghost cleanup: failed to remove recording marker", "session", sessionName, "error", err)
			continue
		}

		// remove empty session directory
		removeEmptyDir(state.SessionPath)

		result.Removed++
		result.Names = append(result.Names, sessionName)
		slog.Debug("ghost cleanup: removed", "session", sessionName, "parent_pid", state.ParentPID)
	}

	return result
}

// loadRecordingStatesFromDir loads recording states from a single sessions directory.
func loadRecordingStatesFromDir(sessionsDir string) ([]*RecordingState, error) {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return nil, err
	}

	var states []*RecordingState
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		recordingPath := filepath.Join(sessionsDir, entry.Name(), recordingFile)
		data, readErr := os.ReadFile(recordingPath)
		if readErr != nil {
			continue
		}
		var state RecordingState
		if jsonErr := json.Unmarshal(data, &state); jsonErr != nil {
			continue
		}
		if state.SessionPath == "" {
			state.SessionPath = filepath.Join(sessionsDir, entry.Name())
		}
		states = append(states, &state)
	}
	return states, nil
}

// removeEmptyDir removes a directory only if it's empty.
func removeEmptyDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	if len(entries) == 0 {
		_ = os.Remove(dir)
	}
}

// StartRecordingOptions contains options for starting a recording.
type StartRecordingOptions struct {
	AgentID          string
	AdapterName      string
	SessionFile      string // source file from adapter (Claude Code JSONL)
	OutputFile       string // output file being recorded
	Title            string
	Username         string // attribution slug for paths — via identity.AttributionUsername(). NOT an email.
	RepoContextPath  string // path to repo context directory (for storing sessions)
	ReminderInterval int    // defaults to DefaultReminderInterval if 0
	FilterMode       string // "infra" or "all" - controls event filtering on stop
	WorkspacePath    string // git root / project directory
	Branch           string // git branch at recording start

	// Parent session tracking for subagent workflows
	ParentSessionPath string // path to parent's session folder (optional)
	ParentAgentID     string // parent's agent ID (optional)

	AgentType   string // original agent type for metadata (e.g., "codex", "amp")
	Model       string // LLM model for generic adapters
	ParentPID   int    // parent agent process ID for liveness detection
	Origin      string // session origin: "human", "subagent", "agent" (from agentx.DetectOrigin)
	StartOffset int64  // byte offset of SessionFile at recording start; entries before this are pre-session
	WatchMode   string // "hook" or "tail" — how entries are captured
}

// StartRecording begins a new recording session.
// Returns ErrAlreadyRecording if a recording is already in progress.
func StartRecording(projectRoot string, opts StartRecordingOptions) (*RecordingState, error) {
	if projectRoot == "" {
		return nil, fmt.Errorf("%w: project root", ErrEmptyPath)
	}

	// clean up stale empty recording stubs to prevent accumulation
	cleanupStaleEmptyRecordings(projectRoot)

	// check if THIS agent already has a recording; other agents' recordings are valid
	existing, err := LoadRecordingStateForAgent(projectRoot, opts.AgentID)
	if err != nil {
		return nil, fmt.Errorf("check recording state project=%s: %w", projectRoot, err)
	}
	if existing != nil {
		if existing.StopIncomplete {
			// previous stop returned retry but agent restarted — clear stale state
			slog.Info("clearing incomplete stop state", "agent_id", existing.AgentID)
			if err := ClearRecordingStateForAgent(projectRoot, opts.AgentID); err != nil {
				return nil, fmt.Errorf("clear incomplete recording state: %w", err)
			}
		} else {
			return nil, fmt.Errorf("%w: agent_id=%s started_at=%s", ErrAlreadyRecording, existing.AgentID, existing.StartedAt.Format(time.RFC3339))
		}
	}

	reminderInterval := opts.ReminderInterval
	if reminderInterval <= 0 {
		reminderInterval = DefaultReminderInterval
	}

	// determine username for session name
	username := opts.Username
	if username == "" {
		username = "user"
	}

	// generate session name
	sessionName := GenerateSessionName(opts.AgentID, username)

	// determine sessions base path — prefer ledger cache (environment-independent),
	// fall back to explicit RepoContextPath, then XDG cache.
	// Only use ledger cache when the project has an explicit endpoint configured
	// (not the global default) — this ensures test projects and uninitialized
	// repos fall through to XDG cache.
	var sessionsBasePath string
	repoID := getRepoIDFromProject(projectRoot)
	if repoID != "" {
		ep := getProjectEndpoint(projectRoot)
		if ep != "" {
			ledgerBase := paths.LedgerSessionCacheBase(repoID, ep)
			if ledgerBase != "" {
				sessionsBasePath = filepath.Join(ledgerBase, "sessions")
			}
		}
	}
	if sessionsBasePath == "" && opts.RepoContextPath != "" {
		sessionsBasePath = filepath.Join(opts.RepoContextPath, "sessions")
	}
	if sessionsBasePath == "" && repoID != "" {
		contextPath := GetContextPath(repoID)
		if contextPath != "" {
			sessionsBasePath = filepath.Join(contextPath, "sessions")
		}
	}

	if sessionsBasePath == "" {
		// sessions must go in the ledger or XDG cache, never inside the project
		return nil, ErrNoLedger
	}

	// validate session file is a regular file (not a directory) before creating dirs
	if opts.SessionFile != "" {
		info, err := os.Stat(opts.SessionFile)
		if err != nil {
			return nil, fmt.Errorf("session file not accessible: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("session file is not a regular file: %s", opts.SessionFile)
		}
	}

	// create session folder path
	sessionPath := filepath.Join(sessionsBasePath, sessionName)

	// create the session directory
	if err := os.MkdirAll(sessionPath, 0755); err != nil {
		return nil, fmt.Errorf("create session dir=%s: %w", sessionPath, err)
	}

	// determine session file path (for backward compatibility)
	sessionFile := opts.SessionFile
	if sessionFile == "" {
		sessionFile = filepath.Join(sessionPath, "raw.jsonl")
	}

	// auto-detect session origin if not explicitly provided
	origin := opts.Origin
	if origin == "" {
		origin = string(agentx.DetectOriginFromOS(""))
	}

	state := &RecordingState{
		AgentID:           opts.AgentID,
		SessionID:         sessionid.GenerateSessionID(),
		AdapterName:       opts.AdapterName,
		SessionFile:       opts.SessionFile,
		OutputFile:        sessionFile,
		SessionPath:       sessionPath,
		Title:             opts.Title,
		StartedAt:         time.Now().UTC(),
		EntryCount:        0,
		LastReminderSeq:   0,
		ReminderInterval:  reminderInterval,
		FilterMode:        opts.FilterMode,
		WorkspacePath:     opts.WorkspacePath,
		Branch:            opts.Branch,
		ParentSessionPath: opts.ParentSessionPath,
		ParentAgentID:     opts.ParentAgentID,
		AgentType:         opts.AgentType,
		Model:             opts.Model,
		ParentPID:         opts.ParentPID,
		Origin:            origin,
		CacheDir:          paths.CacheDir(),
		StartOffset:       opts.StartOffset,
		WatchMode:         opts.WatchMode,
	}

	// always capture parent PID for liveness detection and ghost cleanup
	if state.ParentPID <= 0 {
		state.ParentPID = os.Getppid()
	}

	if err := SaveRecordingState(projectRoot, state); err != nil {
		return nil, err
	}

	return state, nil
}

// UpdateRecordingStateForAgent updates recording state for a specific agent.
// Safe for concurrent use: only touches this agent's .recording.json.
func UpdateRecordingStateForAgent(projectRoot, agentID string, updateFn func(*RecordingState)) error {
	state, err := LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil {
		return fmt.Errorf("load recording state: %w", err)
	}
	if state == nil {
		return ErrNotRecording
	}
	updateFn(state)
	if err := SaveRecordingState(projectRoot, state); err != nil {
		return fmt.Errorf("save recording state: %w", err)
	}
	return nil
}

// UpdateRecordingState updates and persists the recording state.
// Useful for updating entry count or last reminder sequence.
// Note: uses first-match state. Use UpdateRecordingStateForAgent in agent-context code.
func UpdateRecordingState(projectRoot string, updateFn func(*RecordingState)) error {
	state, err := LoadRecordingState(projectRoot)
	if err != nil {
		return fmt.Errorf("load recording state: %w", err)
	}
	if state == nil {
		return ErrNotRecording
	}

	updateFn(state)

	if err := SaveRecordingState(projectRoot, state); err != nil {
		return fmt.Errorf("save recording state: %w", err)
	}
	return nil
}

// StopRecording ends an active recording session for a specific agent.
// Returns the final state and ErrNotRecording if no recording is active for this agent.
func StopRecording(projectRoot, agentID string) (*RecordingState, error) {
	if projectRoot == "" {
		return nil, fmt.Errorf("%w: project root", ErrEmptyPath)
	}
	if agentID == "" {
		return nil, fmt.Errorf("%w: agent ID", ErrEmptyPath)
	}

	state, err := LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil {
		return nil, fmt.Errorf("load recording state project=%s agent=%s: %w", projectRoot, agentID, err)
	}
	if state == nil {
		return nil, fmt.Errorf("%w: project=%s agent=%s", ErrNotRecording, projectRoot, agentID)
	}

	if err := ClearRecordingStateForAgent(projectRoot, agentID); err != nil {
		return nil, fmt.Errorf("clear recording state project=%s: %w", projectRoot, err)
	}

	return state, nil
}

// GetSessionName extracts the session name from a session path.
func GetSessionName(sessionPath string) string {
	return filepath.Base(strings.TrimSuffix(sessionPath, "/"))
}

// FindParentSessionPathForAgent looks up the recording state for a specific agent
// and returns its session path. Used by subagents to discover parent session.
func FindParentSessionPathForAgent(projectRoot, agentID string) string {
	state, _ := LoadRecordingStateForAgent(projectRoot, agentID)
	if state == nil {
		return ""
	}
	return state.SessionPath
}

// FindParentSessionPath looks up the active recording state and returns its session path.
// Used by subagents to discover where to report completion.
// Returns empty string if no recording is active.
// Note: returns first-match. Use FindParentSessionPathForAgent when agent ID is known.
func FindParentSessionPath(projectRoot string) string {
	state, err := LoadRecordingState(projectRoot)
	if err != nil || state == nil {
		return ""
	}
	return state.SessionPath
}
