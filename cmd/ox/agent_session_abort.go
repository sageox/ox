package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

// sessionAbortOutput is the JSON output format for session abort.
type sessionAbortOutput struct {
	Success     bool   `json:"success"`
	Type        string `json:"type"`
	AgentID     string `json:"agent_id"`
	SessionName string `json:"session_name,omitempty"`
	Message     string `json:"message"`
	Guidance    string `json:"guidance,omitempty"`

	// LedgerDraftDeleted reports that a published draft placeholder was
	// removed from the ledger, so the session's /c/ page stops advertising it.
	LedgerDraftDeleted bool `json:"ledger_draft_deleted,omitempty"`
	// Warning is set when the abort succeeded locally but a ledger operation
	// did not fully land (typically a push failure). Surfaced so the user
	// knows the placeholder may still be visible to teammates until the next
	// push, rather than discovering it later.
	Warning string `json:"warning,omitempty"`
}

// runAgentSessionAbort discards a session without uploading to ledger.
// This is a destructive operation — all local session data is permanently deleted.
//
// Two modes:
//   - No args: abort this agent's active recording (requires .recording.json)
//   - With session name: abort any session by name (orphaned, ghost, non-recording)
//
// Confirmation behavior:
//   - Interactive terminal: prompts user with y/N confirmation
//   - Non-interactive (agent/pipe): requires --force flag
//
// Usage:
//
//	ox agent <id> session abort [--force]
//	ox agent <id> session abort <session-name> [--force]
func runAgentSessionAbort(inst *agentinstance.Instance, cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return runAgentSessionAbortByName(inst, cmd, args[0])
	}
	return runAgentSessionAbortActive(inst, cmd)
}

// runAgentSessionAbortActive aborts the calling agent's own active recording.
// This is the original behavior when no session name argument is provided.
func runAgentSessionAbortActive(inst *agentinstance.Instance, cmd *cobra.Command) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	state, err := session.LoadRecordingStateForAgent(projectRoot, inst.AgentID)
	if err != nil {
		return fmt.Errorf("failed to load recording state: %w", err)
	}
	if state == nil {
		return fmt.Errorf("no active session to abort\nRun 'ox agent %s session start' to begin recording", inst.AgentID)
	}

	if err := confirmAbort(inst, cmd); err != nil {
		return err
	}

	sessionName := session.GetSessionName(state.SessionPath)

	// Remove any published draft placeholder from the ledger FIRST, while the
	// local state that identifies it still exists. Doing this after
	// os.RemoveAll would mean a failure here leaves the user's data already
	// deleted with a live placeholder still advertising the session.
	draftDeleted, draftWarning := removeLedgerDraftForAbort(sessionName)

	// clear .recording.json so future session start works
	if err := session.ClearRecordingStateForAgent(projectRoot, inst.AgentID); err != nil {
		return fmt.Errorf("failed to clear recording state: %w", err)
	}

	// remove entire session cache folder (raw.jsonl, plan.md, etc.)
	// guard against empty path — os.RemoveAll("") would delete cwd
	if state.SessionPath != "" {
		if err := os.RemoveAll(state.SessionPath); err != nil {
			return fmt.Errorf("recording cleared but failed to remove session data at %s: %w", state.SessionPath, err)
		}
	}

	// intentionally do NOT set doctor.SetNeedsDoctorAgent() — user chose to discard

	// flip the registered /c/ page to "discarded" and drop pending PR-link
	// repair tasks server-side (fire-and-forget)
	notifySessionAbortedAsync(projectRoot, state.SessionID)

	return emitAbortOutput(cmd.OutOrStdout(), inst.AgentID, sessionName, draftDeleted, draftWarning)
}

// runAgentSessionAbortByName aborts a session by name. Only allows discarding
// orphan, ghost, or local-only sessions — not uploaded or actively recording ones.
// This handles the gap where orphaned sessions have no clean discard path.
func runAgentSessionAbortByName(inst *agentinstance.Instance, cmd *cobra.Command, nameArg string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	// resolve session name and path in local cache
	sessionName, sessionPath, err := resolveSessionForAbort(projectRoot, nameArg)
	if errors.Is(err, errDraftOnlySession) {
		// Nothing left locally — the session exists only as a published draft
		// placeholder advertising it on the /c/ page. Removing that IS the
		// abort, and there is no local data to classify or delete.
		return abortDraftOnlySession(inst, cmd, projectRoot, sessionName)
	}
	if err != nil {
		return err
	}

	// classify the session to guard against aborting uploaded or recording sessions
	info := buildSessionInfo(sessionName, sessionPath)
	isUploaded := isSessionInLedger(sessionName)
	status := session.ClassifySession(info, isUploaded)

	switch status {
	case session.StatusOrphan, session.StatusGhost, session.StatusLocal:
		// safe to discard
	case session.StatusRecording:
		return fmt.Errorf("session %q is actively recording — use 'ox agent %s session abort' (without a name) to abort your own session", sessionName, inst.AgentID)
	case session.StatusUploaded:
		return fmt.Errorf("session %q is already uploaded to the ledger — use 'ox agent %s session delete %s' to remove it", sessionName, inst.AgentID, sessionName)
	case session.StatusPaused:
		// paused = stopped but not uploaded; safe to discard locally
	case session.StatusCanceled:
		// already canceled — just clean up if folder somehow remains
	default:
		return fmt.Errorf("session %q has unexpected status %q", sessionName, status)
	}

	if err := confirmAbort(inst, cmd); err != nil {
		return err
	}

	// capture the ses_ ID before the folder is destroyed so the server-side
	// /c/ page can flip from "in progress" to "discarded": recording state
	// first, raw-header carrier as fallback
	abortedSessionID := ""
	recPath := filepath.Join(sessionPath, ".recording.json")
	if data, err := os.ReadFile(recPath); err == nil {
		var rs session.RecordingState
		if json.Unmarshal(data, &rs) == nil {
			abortedSessionID = rs.SessionID
		}
	}
	if abortedSessionID == "" {
		abortedSessionID = session.ReadHeaderSessionID(filepath.Join(sessionPath, "raw.jsonl"))
	}

	// Remove any published draft placeholder from the ledger before touching
	// local data, so a git failure cannot leave the session deleted locally
	// while a placeholder still advertises it.
	draftDeleted, draftWarning := removeLedgerDraftForAbort(sessionName)

	// remove .recording.json if present (unconditional — no agent ownership check)
	if err := os.Remove(recPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to clear recording state: %w", err)
	}

	// remove entire session folder
	if err := os.RemoveAll(sessionPath); err != nil {
		return fmt.Errorf("failed to remove session data at %s: %w", sessionPath, err)
	}

	notifySessionAbortedAsync(projectRoot, abortedSessionID)

	return emitAbortOutput(cmd.OutOrStdout(), inst.AgentID, sessionName, draftDeleted, draftWarning)
}

// buildSessionInfo constructs a SessionInfo from a session folder on disk.
// Reads .recording.json if present; checks raw.jsonl for data presence.
func buildSessionInfo(sessionName, sessionPath string) session.SessionInfo {
	info := session.SessionInfo{
		SessionName: sessionName,
	}

	// check for .recording.json
	recPath := filepath.Join(sessionPath, ".recording.json")
	if data, err := os.ReadFile(recPath); err == nil {
		var state session.RecordingState
		if jsonErr := json.Unmarshal(data, &state); jsonErr == nil {
			info.Recording = true
			info.AgentID = state.AgentID
			info.EntryCount = state.EntryCount
			info.ParentPID = state.ParentPID
			info.CreatedAt = state.StartedAt
		}
	}

	// check for raw.jsonl data
	info.HasRawData = session.RawJSONLHasData(sessionPath)

	return info
}

// isSessionInLedger reports whether a session has real, finalized content in
// the ledger.
//
// A directory holding only a DRAFT placeholder does NOT count. A draft is a
// meta.json-only marker published mid-recording (ADR-029) and carries no turn
// data. Counting it made ClassifySession return StatusUploaded for a live
// recording, which made abort refuse outright with "already uploaded — use
// session delete" — i.e. it broke the privacy escape hatch for exactly the
// sessions a user is most likely to want discarded.
func isSessionInLedger(sessionName string) bool {
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return false
	}
	ledgerSessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	info, statErr := os.Stat(ledgerSessionDir)
	if statErr != nil || !info.IsDir() {
		return false
	}
	// An unreadable meta.json is treated as finalized (the conservative
	// direction): abort then refuses, and the user is pointed at
	// `session delete`, rather than us deleting a directory we cannot classify.
	if meta, metaErr := lfs.ReadSessionMeta(ledgerSessionDir); metaErr == nil && meta.IsDraft() {
		return false
	}
	return true
}

// resolveSessionForAbort finds a session by name across all known session locations.
// Supports partial name resolution (e.g., agent ID suffix like "OxKMZN").
// Returns the resolved session name and full path to the session folder.
//
// Search order, most-authoritative-first:
//  1. XDG cache (active/recent recordings)
//  2. Ledger cache (in-progress sessions before upload)
//  3. Ledger sessions directory (uploaded sessions)
//
// The ledger cache is searched BEFORE the git-tracked ledger sessions dir
// because that is where live recordings actually live. Before draft
// placeholders existed the order did not matter — a session was in one place
// or the other. Now a live recording has a git-tracked draft directory of the
// same name, and resolving to that one would point the caller's os.RemoveAll
// at a tracked directory (deleting it from the worktree with no `git rm`, so
// the next pull restores it) while leaving the real recording untouched.
func resolveSessionForAbort(projectRoot, nameArg string) (string, string, error) {
	// build list of stores to search, in priority order
	var stores []*session.Store

	// 1. XDG cache (primary location for active recordings)
	repoID := getRepoIDOrDefault(projectRoot)
	if contextPath := session.GetContextPath(repoID); contextPath != "" {
		if s, err := session.NewStore(contextPath); err == nil {
			stores = append(stores, s)
		}
	}

	// 2. Ledger cache, then 3. Ledger sessions
	ledgerPath, ledgerErr := resolveLedgerPath()
	if ledgerErr == nil {
		ledgerCachePath := filepath.Join(ledgerPath, ".sageox", "cache")
		if s, err := session.NewStore(ledgerCachePath); err == nil {
			stores = append(stores, s)
		}
		if s, err := session.NewStore(ledgerPath); err == nil {
			stores = append(stores, s)
		}
	}

	draftOnlyName := ""
	for _, s := range stores {
		resolved, resolveErr := s.ResolveSessionName(nameArg)
		if resolveErr != nil {
			// ambiguous match: surface the error immediately
			return "", "", resolveErr
		}
		sessionPath := s.GetSessionPath(resolved)
		info, statErr := os.Stat(sessionPath)
		if statErr != nil || !info.IsDir() {
			continue
		}
		// Never hand a git-tracked ledger directory back as an os.RemoveAll
		// target.
		//
		// The fail-safe direction is load-bearing: an UNREADABLE meta.json here
		// must also be refused, not optimistically treated as not-a-draft.
		// Returning it would have the caller delete a tracked directory from
		// the worktree with no `git rm` — the next pull restores it, the real
		// recording is never touched, and the user is told the session was
		// discarded. For a privacy escape hatch, silently failing to delete is
		// the catastrophic direction, so ambiguity resolves to "refuse".
		if ledgerErr == nil && sessionPath == draftLedgerSessionDir(ledgerPath, resolved) {
			meta, metaErr := lfs.ReadSessionMeta(sessionPath)
			// A MISSING meta.json is not ambiguity — it is a legacy or
			// partially-uploaded session with no placeholder, which has always
			// been resolvable here. Only a meta.json that exists and cannot be
			// parsed is ambiguous, and that is what must be refused.
			if metaErr != nil && !errors.Is(metaErr, fs.ErrNotExist) {
				continue
			}
			if meta.IsDraft() {
				// Remember it: if nothing else matches, this session exists
				// ONLY as a published placeholder and must still be abortable.
				draftOnlyName = resolved
				continue
			}
		}
		return resolved, sessionPath, nil
	}

	// A session whose only remaining presence is a draft placeholder — local
	// cache pruned, agent died, or an earlier abort removed local data but its
	// git removal never landed. Without this branch the placeholder advertises
	// the session forever with no CLI path to remove it.
	if draftOnlyName != "" {
		return draftOnlyName, "", errDraftOnlySession
	}

	return "", "", fmt.Errorf("session not found: %s\nRun 'ox session list' to see available sessions", nameArg)
}

// errDraftOnlySession signals that a name resolved to nothing but a published
// draft placeholder in the ledger. A sentinel rather than a bool so the by-name
// abort path can branch without repeating the lookup.
var errDraftOnlySession = errors.New("session exists only as a published draft placeholder")

// abortDraftOnlySession handles a session whose only remaining presence is a
// published draft placeholder in the ledger.
//
// This is the recovery path for a placeholder that outlived its recording: the
// local cache was pruned, the agent died, or a previous abort deleted local
// data but its git removal never reached the remote. Without it, that
// placeholder advertises an "in progress" session forever with no way to remove
// it — and since the daemon's anti-entropy deliberately skips drafts, nothing
// else will ever clean it up either.
//
// The ses_ id is read from the placeholder BEFORE removing it, so the server
// notification can flip the right /c/ page to discarded.
func abortDraftOnlySession(inst *agentinstance.Instance, cmd *cobra.Command, projectRoot, sessionName string) error {
	if err := confirmAbort(inst, cmd); err != nil {
		return err
	}

	abortedSessionID := ""
	if ledgerPath, err := resolveLedgerPath(); err == nil {
		if meta, metaErr := lfs.ReadSessionMeta(draftLedgerSessionDir(ledgerPath, sessionName)); metaErr == nil {
			abortedSessionID = meta.EffectiveSessionID()
		}
	}

	deleted, warning := removeLedgerDraftForAbort(sessionName)
	notifySessionAbortedAsync(projectRoot, abortedSessionID)
	return emitAbortOutput(cmd.OutOrStdout(), inst.AgentID, sessionName, deleted, warning)
}

// removeLedgerDraftForAbort git-removes a session's draft placeholder from the
// ledger, if one was published. Returns a human-readable warning when the
// removal landed locally but could not be pushed.
//
// Deliberately tolerant. Abort's contract to the user is "this session is
// discarded", and the local recording data is what actually matters for that.
// A ledger failure here is reported, never fatal: the local delete commit is
// durable and the next push carries it, and notifySessionAbortedAsync
// independently flips the /c/ page to discarded.
func removeLedgerDraftForAbort(sessionName string) (bool, string) {
	if sessionName == "" {
		return false, ""
	}
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return false, ""
	}
	res, err := deleteDraftFromLedger(ledgerPath, sessionName)
	if err != nil {
		slog.Warn("draft removal during abort failed", "session", sessionName, "error", err)
		return false, fmt.Sprintf("could not remove the published draft placeholder from the ledger: %v", err)
	}
	return res.Deleted, res.PushWarning
}

// confirmAbort checks --force flag or prompts for interactive confirmation.
// Returns nil if confirmed, error if denied or missing --force.
func confirmAbort(inst *agentinstance.Instance, cmd *cobra.Command) error {
	forceFlag := cmd.Flag("force") != nil && cmd.Flag("force").Value.String() == "true"
	if forceFlag {
		return nil
	}
	if cli.IsInteractive() {
		if !cli.ConfirmYesNo("Abort and discard session? This cannot be undone", false) {
			fmt.Println("Canceled.")
			return fmt.Errorf("canceled")
		}
		return nil
	}
	return fmt.Errorf("session abort is destructive and cannot be undone\nPass --force to confirm: ox agent %s session abort --force", inst.AgentID)
}

// emitAbortOutput writes the JSON (and optional text) output for a successful abort.
func emitAbortOutput(w io.Writer, agentID, sessionName string, draftDeleted bool, warning string) error {
	message := "session aborted and discarded"
	guidance := "Session aborted and discarded. No further action needed. The recording no longer exists: do not add SageOx-Session trailers or session links referencing it to any future commits or PR bodies, and remove the line from any unsubmitted PR drafts. Continue with your current task."
	if draftDeleted {
		// Be accurate rather than reassuring. A published placeholder was
		// committed and pushed to a SHARED repo, so its identity record — the
		// session id, the agent, the timestamp — remains reachable in git
		// history even after the deletion commit. Telling the user "the
		// recording no longer exists" would be false about the part that is
		// irreversible. No transcript content was ever committed.
		message = "session aborted and discarded; published draft placeholder removed from the ledger"
		// The anchor phrase "do not add SageOx-Session" is kept byte-identical
		// (including case) with the non-draft wording: agents match on it, and
		// a capitalized variant is a different string to a substring match.
		guidance = "Session aborted and discarded, and its published placeholder was removed from the ledger. No conversation content was ever published — a placeholder carries only the session id, agent, and timestamps — but that identity record remains in the ledger's git history. As with any aborted session: do not add SageOx-Session trailers or session links referencing it to any future commits or PR bodies, and remove the line from any unsubmitted PR drafts. Continue with your current task."
	}
	output := sessionAbortOutput{
		Success:            true,
		Type:               "session_abort",
		AgentID:            agentID,
		SessionName:        sessionName,
		Message:            message,
		Guidance:           guidance,
		LedgerDraftDeleted: draftDeleted,
		Warning:            warning,
	}

	if cfg.Text || cfg.Review {
		fmt.Fprintf(w, "Session %q aborted and discarded.\n", sessionName)
		if warning != "" {
			fmt.Fprintf(w, "Warning: %s\n", warning)
		}
		if cfg.Review {
			fmt.Fprintln(w)
			fmt.Fprintln(w, "--- Machine Output ---")
		} else {
			return nil
		}
	}

	jsonOut, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("format abort JSON: %w", err)
	}
	fmt.Fprintln(w, string(jsonOut))
	return nil
}
