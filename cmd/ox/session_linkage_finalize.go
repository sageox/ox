package main

import (
	"context"
	"log/slog"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/lfs"
)

// finalizeLinkageAfterPush runs the M5 upload-confirmed transition after a
// session's content has been pushed to the ledger. It moves LinkageStatus
// from staged → uploaded, then best-effort notifies the SageOx server so
// the (v2) GitHub App reconciler can refresh any PR sticky comment.
//
// All work is best-effort: a failed notify leaves the session in
// notify_failed for `ox doctor` to retry, and never affects the
// already-successful upload. See docs/specs/session-pr-issue-linkage.md
// (v1.5) for the state machine.
func finalizeLinkageAfterPush(projectRoot, sessionDir string, meta *lfs.SessionMeta, sessionName string) {
	if meta == nil {
		return
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

	// 2. notify the server. Only meaningful when there is linkage to report
	//    OR when the server wants the upload signal regardless. We send
	//    whenever the session has any linkage or produced commits; a session
	//    with none has nothing for the reconciler to act on, so skip the call.
	if len(meta.LinkedPRs) == 0 && len(meta.LinkedIssues) == 0 && len(meta.ProducedCommits) == 0 {
		return
	}

	notified := notifySessionUploaded(projectRoot, meta, sessionName)
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
}

// notifySessionUploaded sends the upload-confirmed notification to the
// SageOx server. Returns true on success (including 404/409 graceful cases,
// which the API client maps to nil error), false on transient failure that
// should be retried by doctor.
func notifySessionUploaded(projectRoot string, meta *lfs.SessionMeta, sessionName string) bool {
	cfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil || cfg == nil || cfg.RepoID == "" {
		return false
	}

	ep := endpoint.GetForProject(projectRoot)
	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil || token == nil || token.AccessToken == "" {
		// no credentials — can't notify; doctor retries once auth is present
		return false
	}

	client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken)
	notification := api.SessionUploadedNotification{
		SessionID:       meta.EffectiveSessionID(),
		RepoID:          cfg.RepoID,
		SessionURL:      buildSessionURL(cfg, sessionName),
		LinkedPRs:       meta.LinkedPRs,
		LinkedIssues:    meta.LinkedIssues,
		ProducedCommits: meta.ProducedCommits,
	}
	if err := client.NotifySessionUploaded(notification); err != nil {
		slog.Debug("linkage finalize: notify failed", "error", err, "session", sessionName)
		return false
	}
	return true
}
