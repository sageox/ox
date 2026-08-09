package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
)

// maybePublishSessionDraft increments the turn counter and, at the configured
// turn, publishes (or refreshes) a meta.json-only draft placeholder in the
// ledger so https://<endpoint>/c/<session_id> resolves while the session is
// still running.
//
// Entirely best-effort. Every failure is a log line and a silent return: a hook
// must never fail a turn, and no correctness property depends on a draft
// existing. The worst outcome of a total failure here is the pre-draft
// behavior — the /c/ link stays unresolvable until session stop.
//
// Called ONLY from the Stop hook. The Stop phase means "the agent finished
// responding" and fires exactly once per completed turn, which makes it the
// only real turn signal ox has. It must NOT be called from the afterTool
// handler: that also runs on PostToolUse, so counting there counts tool calls
// rather than turns and the draft would publish on the wrong turn.
func maybePublishSessionDraft(ctx *HookContext) {
	if ctx == nil || ctx.Marker == nil || ctx.Marker.AgentID == "" {
		return
	}
	agentID := ctx.Marker.AgentID

	// Recording state FIRST. It is the only thing that can make anything else
	// matter, and its absence — no active recording for this agent — is the
	// true common case across all the hooks that fire in a repo. Loading config
	// before this made every Stop hook in every repo parse two config files
	// just to discover there was nothing to do. The turn counter is still
	// maintained when drafts are disabled, so `ox session status` reports turn
	// counts and enabling the feature mid-session behaves sensibly.
	var state *session.RecordingState
	err := session.UpdateRecordingStateForAgent(ctx.ProjectRoot, agentID, func(s *session.RecordingState) {
		s.TurnCount++
		snapshot := *s
		state = &snapshot
	})
	if err != nil || state == nil {
		slog.Debug("draft: no recording state for turn count", "agent_id", agentID, "error", err)
		return
	}

	// Cheapest possible early-out before touching config: below the publish
	// threshold nothing can happen regardless of how the feature is configured.
	// DraftPublishTurn is a constant, so this costs one comparison.
	if state.TurnCount < config.DraftPublishTurn && state.DraftAttemptTurn == 0 {
		return
	}

	resolved := config.ResolveSessionDraft()

	// Same gate as notifySessionStartedAsync. If session attribution is off
	// there is no /c/ page to make resolvable, so publishing a draft would be
	// pure ledger noise. Every session-link surface stays on one switch.
	if attr := loadResolvedAttribution(); attr.Session == "" {
		resolved = &config.ResolvedSessionDraft{Enabled: false}
	}

	action := draftDecision(state.TurnCount, state.DraftPublishedTurn, state.DraftPublishedAt != nil, resolved)
	if action == draftActionNone {
		return
	}

	ledgerPath := resolveDraftLedgerPath(ctx.ProjectRoot, state.SessionPath)
	if ledgerPath == "" {
		return
	}

	sessionName := session.GetSessionName(state.SessionPath)
	published := publishDraftPlaceholder(ctx.ProjectRoot, ledgerPath, sessionName, state)

	// DraftAttemptTurn records every attempt; DraftPublishedTurn and
	// DraftPublishedAt record only successes. Keeping them apart is what lets
	// `ox session status` distinguish "published and healthy" from "published
	// once, and every refresh since has failed" — and it keeps the refresh
	// interval anchored to the last thing that actually landed.
	turn := state.TurnCount
	_ = session.UpdateRecordingStateForAgent(ctx.ProjectRoot, agentID, func(s *session.RecordingState) {
		s.DraftAttemptTurn = turn
		if published {
			s.DraftPublishedTurn = turn
			now := time.Now().UTC()
			s.DraftPublishedAt = &now
		}
	})
}

// resolveDraftLedgerPath returns the ledger this recording belongs to, or ""
// when it cannot be established with certainty.
//
// deriveLedgerPath is a pure string operation that returns filepath.Dir for ANY
// path whose parent is named "sessions". That set includes the XDG recording
// cache and the legacy in-repo fallback — where the derived "ledger" is the
// USER'S OWN PROJECT ROOT. Its three prior callers only built IPC payloads, so
// a wrong answer was inert. This is the first caller that runs `git add` and
// `git commit --no-verify` on the result, and committing a placeholder into
// someone's product repo would be an unrecoverable trust failure.
//
// So the derived path is only accepted when it matches the project's CONFIGURED
// ledger. No match, no draft.
func resolveDraftLedgerPath(projectRoot, sessionPath string) string {
	derived := deriveLedgerPath(sessionPath)
	if derived == "" {
		slog.Debug("draft: no ledger derivable from session path", "path", sessionPath)
		return ""
	}
	// The project's CONFIGURED ledger, read from its local config rather than
	// from the process CWD — the hook may run from anywhere in the worktree.
	configured := ""
	if localCfg, err := config.LoadLocalConfig(projectRoot); err == nil &&
		localCfg != nil && localCfg.Ledger != nil {
		configured = localCfg.Ledger.Path
	}
	if configured == "" {
		if ctx, err := config.LoadProjectContext(projectRoot); err == nil && ctx != nil {
			configured = ctx.DefaultLedgerPath()
		}
	}
	if configured == "" || filepath.Clean(configured) != filepath.Clean(derived) {
		slog.Debug("draft: derived path is not this project's ledger",
			"derived", derived, "configured", configured)
		return ""
	}
	return derived
}

// publishDraftPlaceholder writes the draft meta.json into the ledger and
// commits it locally. Returns whether the placeholder is durably committed.
//
// The push is deliberately not done here — it is batched onto the daemon's
// ledger sync cycle by SyncScheduler.pushSessionDraftCommits (~60s). pushLedger
// is a secret scan plus a credential refresh plus an LFS reconcile plus a
// three-attempt pull-rebase loop, which is seconds of latency on a turn
// boundary. If no daemon is running, the local commit still stands and the next
// pushLedger from any path carries it: session stop, plan commit, or
// `ox doctor --fix` on an ahead branch.
func publishDraftPlaceholder(projectRoot, ledgerPath, sessionName string, state *session.RecordingState) bool {
	if sessionName == "" || state.SessionID == "" {
		return false
	}

	ep := endpoint.GetForProject(projectRoot)
	sessionDir := draftLedgerSessionDir(ledgerPath, sessionName)

	input := lfs.DraftInput{
		SessionName: sessionName,
		SessionID:   state.SessionID,
		Username:    identity.AttributionDisplayName(ep, config.GetDisplayName()),
		UserID:      auth.GetUserID(ep),
		RepoID:      getRepoIDOrDefault(projectRoot),
		AgentID:     state.AgentID,
		AgentType:   state.AdapterName,
		Model:       state.Model,
		CreatedAt:   state.StartedAt,
		TurnCount:   state.TurnCount,
		EntryCount:  state.EntryCount,
		Now:         time.Now(),
	}

	// Bounded so a wedged flock on meta.json (a concurrent finalize holding it)
	// cannot hang the agent's turn. Giving up is the correct outcome: the next
	// refresh retries, and a finalize in flight makes the draft moot anyway.
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := lfs.WriteDraftSessionMeta(writeCtx, sessionDir, input); err != nil {
		// ErrNotDraft / ErrDraftDirNotEmpty are expected races, not faults:
		// a finalize landed between the decision and this write, or a name
		// collided with a finalized session pulled from the remote.
		slog.Debug("draft: write skipped", "session", sessionName, "error", err)
		return false
	}

	if err := commitDraftLocally(ledgerPath, sessionName); err != nil {
		slog.Info("draft: local commit failed", "session", sessionName, "error", err)
		return false
	}
	return true
}
