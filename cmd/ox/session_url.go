package main

import (
	"fmt"
	"net/url"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionupload"
)

// buildSessionURL constructs the canonical web URL for viewing a session.
// Returns empty string if required config (repo_id, endpoint) is missing.
// Used for best-effort commit trailers — the URL is only available while a
// session is actively recording.
func buildSessionURL(cfg *config.ProjectConfig, sessionName string) string {
	if cfg == nil || cfg.RepoID == "" || sessionName == "" {
		return ""
	}
	ep := endpoint.NormalizeEndpoint(cfg.GetEndpoint())
	if ep == "" {
		return ""
	}
	return fmt.Sprintf("%s/repo/%s/sessions/%s/view",
		ep,
		url.PathEscape(cfg.RepoID),
		url.PathEscape(sessionName),
	)
}

// buildConversationURL constructs the universal /c/<ses_id> link; see
// sessionupload.ConversationURL.
func buildConversationURL(cfg *config.ProjectConfig, sessionID string) string {
	return sessionupload.ConversationURL(cfg, sessionID)
}

// sessionLinkOutputs derives the prime session URL and the exact-literal PR
// directive for a live recording in projectRoot. The attribution.session toggle
// gates both: an empty toggle (attribution.session: "") or a missing/unlinkable
// state yields ("", "") so every session-link surface disables together. The
// /c/ form is preferred; recordings started under an older binary carry no
// start-minted ID and fall back to the name-based view URL.
func sessionLinkOutputs(projectRoot string, projCfg *config.ProjectConfig, state *session.RecordingState, attrSession string) (sessionURL, prDirective string) {
	if attrSession == "" || state == nil {
		return "", ""
	}
	if state.LifecycleRegistrationState == "pending" {
		// A locally minted id is not evidence that the remote resolver knows
		// it. Keep the link out of commit/PR guidance until a retry or upload
		// makes it server-visible.
		return "", ""
	}
	if stopHoldsSession(projectRoot, state) {
		// A session held on this machine is never registered with the server
		// (notifySessionStarted applies the same rule: the mode saved at start
		// or the setting now), so a /c/ link would 404. Identical reasoning to
		// the pending guard above — the id exists locally, which is not the
		// same as being resolvable — and it must hold for commit trailers and
		// PR bodies, which outlive the session and would otherwise carry a
		// permanently dead link.
		return "", ""
	}
	sessionURL = buildConversationURL(projCfg, state.SessionID)
	if sessionURL == "" {
		sessionURL = buildSessionURL(projCfg, session.GetSessionName(state.SessionPath))
	}
	if sessionURL == "" {
		return "", ""
	}
	// exact-literal so the agent copies, never reconstructs — templated
	// placeholders are the confabulation vector
	prDirective = fmt.Sprintf(
		"When you create a PR for this session's work, the LAST line of the PR body must be exactly:\nSageOx-Session: %s\nIf this session is stopped or aborted, stop adding this line.",
		sessionURL)
	return sessionURL, prDirective
}
