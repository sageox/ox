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
// team) or the mode recorded when the recording started makes it held.
func stopHoldsSession(projectRoot string, state *session.RecordingState) bool {
	return config.GetSessionPublishing(projectRoot) == config.SessionPublishingManual ||
		state.RecordedManualPublishing()
}

// writeSessionHold records a hold. A variable so tests can make it fail.
var writeSessionHold = session.WriteHoldMarker

// holdStoppedSession records the hold on the session's cache folder. Call it
// before anything the daemon reacts to (.needs-summary) is written and before
// .recording.json is cleared, so no window exists where the folder looks like
// unfinished work with no hold. On error the caller must keep .recording.json:
// without it or the hold, the folder looks like an orphan the daemon publishes.
func holdStoppedSession(result *agentSessionResult, cacheDir, source string) error {
	if err := writeSessionHold(cacheDir, session.HoldManualPublishing, source); err != nil {
		return fmt.Errorf("hold session on this machine: %w", err)
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
		if err := writeSessionHold(state.SessionPath, session.HoldManualPublishing, "hook_"+phase); err != nil {
			return fmt.Errorf("hold session on this machine: %w", err)
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
// standing choice is to publish explicitly.
func checkHeldSessions(projectRoot, ledgerPath string) (checkResult, bool) {
	sessions := listUnpublishedCacheSessions(ledgerPath)
	if config.GetSessionPublishing(projectRoot) == config.SessionPublishingManual && len(sessions.Waiting) > 0 {
		for _, name := range sessions.Waiting {
			for _, dir := range session.HeldSessionDirs(ledgerPath) {
				if _, err := os.Stat(filepath.Join(dir, name, ledgerFileRaw)); err == nil {
					_ = session.WriteHoldMarker(filepath.Join(dir, name), session.HoldManualPublishing, "doctor")
				}
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
		result.children = append(result.children, checkResult{name: name, passed: true, priority: "info", message: "cache only; recovered when its AI coworker exits"})
	}
	return result, true
}
