package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
)

// heldSessionGuidance replaces the stop guidance for a held session. Without
// it, an empty summary_prompt reads as "the daemon uploads it", which is the
// opposite of what a held session does.
const heldSessionGuidance = "Session held on this machine (session_publishing: manual): it is not summarized, uploaded, or deleted. Do not publish it unless the coworker asks; to publish, run 'ox session upload <name>' with the name in upload_warning."

// stopHoldsSession reports whether a stopping session is held on this machine.
// It fails closed: the mode resolved now (contract D13: env > user > project >
// team) or the mode recorded when the recording started makes it held. The
// session-link surfaces (sessionLinkOutputs, autoSessionURL, the commit-msg
// trailer) apply the same rule: a held session is never registered, so its /c/
// link would 404.
func stopHoldsSession(projectRoot string, state *session.RecordingState) bool {
	return config.GetSessionPublishing(projectRoot) == config.SessionPublishingManual ||
		state.RecordedManualPublishing()
}

// writeSessionHold records a hold. A variable so tests can make it fail.
var writeSessionHold = session.WriteHoldMarker

// recordManualPublishing saves the manual choice on the recording itself, for
// a stop that holds a recording started in auto mode. If the hold cannot be
// written, the kept recording must still say manual: daemon recovery reads only
// the recorded mode, and would publish a recording that says auto.
func recordManualPublishing(state *session.RecordingState) error {
	if state == nil || state.SessionPath == "" || state.RecordedManualPublishing() {
		return nil
	}
	err := session.UpdateRecordingStateAt(state.SessionPath, state.SessionID, func(s *session.RecordingState) {
		s.PublishingMode = config.SessionPublishingManual
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil // no recording left to keep
	}
	if err != nil {
		return fmt.Errorf("record manual publishing on the recording: %w", err)
	}
	state.PublishingMode = config.SessionPublishingManual
	return nil
}

// holdStoppedSession records the hold on the session's cache folder. Call it
// before anything the daemon reacts to (.needs-summary) is written and before
// .recording.json is cleared, so no window exists where the folder looks like
// unfinished work with no hold. On error the caller must keep .recording.json:
// without it or the hold, the folder looks like an orphan the daemon publishes.
func holdStoppedSession(result *agentSessionResult, state *session.RecordingState, cacheDir, source string) error {
	recordErr := recordManualPublishing(state)
	if err := writeSessionHold(cacheDir, session.HoldManualPublishing, source); err != nil {
		return fmt.Errorf("hold session on this machine: %w", errors.Join(err, recordErr))
	}
	result.Held = true
	return nil
}

// heldSessionWarning is the stop message for a held session.
func heldSessionWarning(sessionName string) string {
	return fmt.Sprintf("Held on this machine (publishing mode: manual): not summarized or uploaded. Publish with 'ox session upload %s'.", sessionName)
}

// finalizeOrHoldOnHookStop hands a recording stopped by the /clear or
// SessionEnd hook to the daemon (fire-and-forget IPC), or holds it when the
// coworker publishes manually. A held session gets its hold and no IPC: the
// finalize request bypasses the daemon's scan and would summarize and upload
// it (GH #1093). Call it before the recording state is cleared, and keep the
// state when it returns an error: the hold could not be recorded.
func finalizeOrHoldOnHookStop(ctx *HookContext, state *session.RecordingState, phase string) error {
	if state.SessionPath == "" {
		return nil
	}
	if stopHoldsSession(ctx.ProjectRoot, state) {
		recordErr := recordManualPublishing(state)
		if err := writeSessionHold(state.SessionPath, session.HoldManualPublishing, "hook_"+phase); err != nil {
			return fmt.Errorf("hold session on this machine: %w", errors.Join(err, recordErr))
		}
		return nil
	}
	ledgerPath := deriveLedgerPath(state.SessionPath)
	if ledgerPath == "" {
		return nil
	}
	if ipcErr := sendHookFinalizeIPC(daemon.SessionFinalizeIPCPayload{
		SessionName: filepath.Base(state.SessionPath),
		LedgerPath:  ledgerPath,
		CachePath:   state.SessionPath,
		ProjectRoot: ctx.ProjectRoot,
	}); ipcErr != nil {
		slog.Debug("hook: finalize IPC failed", "phase", phase, "error", ipcErr)
	}
	return nil
}

// sendHookFinalizeIPC is the daemon finalize request a hook sends. A variable
// so tests can observe whether one was sent at all.
var sendHookFinalizeIPC = func(payload daemon.SessionFinalizeIPCPayload) error {
	return daemon.NewClientForCurrentRepoWithTimeout(100 * time.Millisecond).SessionFinalize(payload)
}

// sessionHeldByName reports whether the session at sessionPath is held, in
// that folder or in any cache copy of it for this project's Ledger. Callers
// often hold the Ledger copy, which never carries the marker.
func sessionHeldByName(sessionPath string) bool {
	if session.IsHeld(sessionPath) {
		return true
	}
	ledgerPath, err := resolveLedgerPath()
	if err != nil || ledgerPath == "" {
		return false
	}
	return session.IsHeldInLedger(ledgerPath, filepath.Base(sessionPath))
}

// unpublishedCacheSessions are cache sessions not yet in the Ledger, by name:
// Held ones the coworker keeps on this machine (GH #1095), and Waiting ones
// the daemon reclaims once their AI coworker exits (GH #1077).
type unpublishedCacheSessions struct {
	Held    []string
	Waiting []string
}

// listUnpublishedCacheSessions scans every cache location for the Ledger at
// ledgerPath. A name appears once, and a hold on any copy wins. Live
// recordings and sessions with no conversation are left out.
func listUnpublishedCacheSessions(ledgerPath string) unpublishedCacheSessions {
	var out unpublishedCacheSessions
	if ledgerPath == "" {
		return out
	}
	seen := map[string]bool{}
	for _, dir := range session.HeldSessionDirs(ledgerPath) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() || seen[name] {
				continue
			}
			sessionDir := filepath.Join(dir, name)
			if lfs.HasDownloadedMarker(sessionDir) {
				continue // a copy downloaded from the Ledger is not this machine's work
			}
			if session.IsHeldInLedger(ledgerPath, name) {
				seen[name] = true
				out.Held = append(out.Held, name)
				continue
			}
			if _, err := os.Stat(filepath.Join(sessionDir, ".recording.json")); err == nil {
				continue // a recording: other checks own it
			}
			if session.ClassifyRawFile(filepath.Join(sessionDir, ledgerFileRaw)) != session.RawSubstantive {
				continue
			}
			meta, err := lfs.ReadSessionMeta(filepath.Join(ledgerPath, "sessions", name))
			if errors.Is(err, os.ErrNotExist) || (err == nil && meta.IsDraft()) {
				seen[name] = true
				out.Waiting = append(out.Waiting, name)
			}
		}
	}
	slices.Sort(out.Held)
	slices.Sort(out.Waiting)
	return out
}

// checkHeldSessions is the doctor's view of unpublished cache sessions. It is
// information only and never publishes. In manual mode a cache-only session
// with no hold (stopped before holds existed) is held first: the coworker's
// standing choice is to publish explicitly. A hold that cannot be written is
// reported as a warning; doctor's own upload retry leaves that session alone
// (findOrphanedSessions).
func checkHeldSessions(projectRoot, ledgerPath string) (checkResult, bool) {
	sessions := listUnpublishedCacheSessions(ledgerPath)
	holdErrs := map[string]error{}
	if config.GetSessionPublishing(projectRoot) == config.SessionPublishingManual && len(sessions.Waiting) > 0 {
		for _, name := range sessions.Waiting {
			if err := holdCacheCopies(ledgerPath, name, "doctor"); err != nil {
				holdErrs[name] = err
			}
		}
		sessions = listUnpublishedCacheSessions(ledgerPath)
	}
	if len(sessions.Held) == 0 && len(sessions.Waiting) == 0 {
		return checkResult{}, false
	}
	result := checkResult{
		name:     "held sessions",
		passed:   true,
		priority: "info",
		message:  fmt.Sprintf("%d held on this machine, %d waiting for recovery", len(sessions.Held), len(sessions.Waiting)),
		detail:   "publish one with 'ox session upload <name>'",
	}
	for _, name := range sessions.Held {
		result.children = append(result.children, checkResult{name: name, passed: true, priority: "info", message: "held (session_publishing: manual)"})
	}
	for _, name := range sessions.Waiting {
		if err, failed := holdErrs[name]; failed {
			// doctor's retry leaves it alone, but the daemon reclaims an
			// unheld cache session like any other
			result.warning, result.priority = true, ""
			result.detail = "fix the error, then run 'ox doctor' again to hold it; publish one with 'ox session upload <name>'"
			result.children = append(result.children, checkResult{name: name, passed: true, warning: true, message: "could not be held, so the daemon may still publish it: " + err.Error()})
			continue
		}
		result.children = append(result.children, checkResult{name: name, passed: true, priority: "info", message: "cache only; recovered when its AI coworker exits"})
	}
	return result, true
}

// holdCacheCopies holds every cache copy of the session named name that has a
// transcript. The errors of the copies that could not be held are joined.
func holdCacheCopies(ledgerPath, name, source string) error {
	var errs []error
	for _, dir := range session.HeldSessionDirs(ledgerPath) {
		sessionDir := filepath.Join(dir, name)
		if _, err := os.Stat(filepath.Join(sessionDir, ledgerFileRaw)); err != nil {
			continue
		}
		if err := writeSessionHold(sessionDir, session.HoldManualPublishing, source); err != nil {
			slog.Warn("could not hold session", "session", name, "dir", dir, "source", source, "error", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
