package main

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
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

// holdStoppedSession records the hold on the session's cache folder. Call it
// before anything the daemon reacts to (.needs-summary) is written and before
// .recording.json is cleared, so no window exists where the folder looks like
// unfinished work with no hold.
func holdStoppedSession(result *agentSessionResult, cacheDir, source string) {
	result.Held = true
	if err := session.WriteHoldMarker(cacheDir, session.HoldManualPublishing, source); err != nil {
		slog.Warn("could not record session hold", "dir", cacheDir, "source", source, "error", err)
	}
}

// heldSessionWarning is the stop message for a held session.
func heldSessionWarning(sessionName string) string {
	return fmt.Sprintf("Held on this machine (publishing mode: manual): not summarized or uploaded. Publish with 'ox session upload %s'.", sessionName)
}

// finalizeOrHoldOnHookStop hands a recording stopped by the /clear or
// SessionEnd hook to the daemon (fire-and-forget IPC), or holds it when the
// coworker publishes manually. A held session gets its hold and no IPC: the
// finalize request bypasses the daemon's scan and would summarize and upload
// it (GH #1093). Call it before the recording state is cleared.
func finalizeOrHoldOnHookStop(ctx *HookContext, state *session.RecordingState, phase string) {
	if state.SessionPath == "" {
		return
	}
	if stopHoldsSession(ctx.ProjectRoot, state) {
		if err := session.WriteHoldMarker(state.SessionPath, session.HoldManualPublishing, "hook_"+phase); err != nil {
			slog.Warn("hook: could not record session hold", "phase", phase, "error", err)
		}
		return
	}
	ledgerPath := deriveLedgerPath(state.SessionPath)
	if ledgerPath == "" {
		return
	}
	if ipcErr := sendHookFinalizeIPC(daemon.SessionFinalizeIPCPayload{
		SessionName: filepath.Base(state.SessionPath),
		LedgerPath:  ledgerPath,
		CachePath:   state.SessionPath,
		ProjectRoot: ctx.ProjectRoot,
	}); ipcErr != nil {
		slog.Debug("hook: finalize IPC failed", "phase", phase, "error", ipcErr)
	}
}

// sendHookFinalizeIPC is the daemon finalize request a hook sends. A variable
// so tests can observe whether one was sent at all.
var sendHookFinalizeIPC = func(payload daemon.SessionFinalizeIPCPayload) error {
	return daemon.NewClientForCurrentRepoWithTimeout(100 * time.Millisecond).SessionFinalize(payload)
}
