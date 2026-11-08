package main

import (
	"context"
	"log/slog"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/sessionupload"
)

// finalizeLinkageAfterPush runs the M5 upload-confirmed transition after a
// session's content has been pushed to the ledger. It moves LinkageStatus
// from staged → uploaded, then best-effort notifies the SageOx server so
// the (v2) GitHub App reconciler can refresh any PR sticky comment and the
// /c/<session_id> conversation link resolves to the uploaded session.
//
// Returns any PR-link repair tasks the server reported (linked PRs whose
// bodies are missing the SageOx-Session trailer) so the stop path can hand
// them to the agent.
//
// All work is best-effort: a failed notify leaves the session in
// notify_failed for `ox doctor` to retry, and never affects the
// already-successful upload. See docs/specs/session-pr-issue-linkage.md
// (v1.5) for the state machine.
func finalizeLinkageAfterPush(projectRoot, sessionDir string, meta *lfs.SessionMeta, sessionName string) []api.PRLinkMiss {
	if meta == nil {
		return nil
	}

	// 1. transition staged → uploaded in meta.json (the push just succeeded,
	//    so the session URL is now viewable). Done under the meta flock so we
	//    don't clobber a concurrent daemon write.
	if err := lfs.MutateSessionMeta(context.Background(), sessionDir, func(m *lfs.SessionMeta) (*lfs.SessionMeta, error) {
		if m == nil {
			return nil, nil // meta vanished — nothing to update
		}
		m.LinkageStatus = lfs.LinkageStatusUploaded
		return m, nil
	}); err != nil {
		slog.Debug("linkage finalize: status transition failed", "error", err, "session", sessionName)
		// fall through: still try to notify; status best-effort
	}

	// 2. notify the server — always, not only when PR/issue linkage exists.
	//    The /c/<session_id> conversation link must resolve for every
	//    uploaded session (trailered commits reach main without PRs, and
	//    plan footers link /c/ too), so the upload signal itself is the
	//    point, and the response carries any PR-link repair tasks.
	misses, notified := notifySessionUploaded(projectRoot, meta, sessionName)
	finalStatus := lfs.LinkageStatusNotified
	if !notified {
		finalStatus = lfs.LinkageStatusNotifyFailed
	}
	if err := lfs.MutateSessionMeta(context.Background(), sessionDir, func(m *lfs.SessionMeta) (*lfs.SessionMeta, error) {
		if m == nil {
			return nil, nil
		}
		m.LinkageStatus = finalStatus
		return m, nil
	}); err != nil {
		slog.Debug("linkage finalize: notify-status write failed", "error", err, "session", sessionName)
	}
	return misses
}

// notifySessionUploaded sends the upload-confirmed notification; see
// sessionupload.Notify.
func notifySessionUploaded(projectRoot string, meta *lfs.SessionMeta, sessionName string) ([]api.PRLinkMiss, bool) {
	return sessionupload.Notify(projectRoot, meta, sessionName)
}

func filterPRLinkMisses(misses []api.PRLinkMiss, expectedLine string) []api.PRLinkMiss {
	return sessionupload.FilterPRLinkMisses(misses, expectedLine)
}
