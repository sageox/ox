package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
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

// buildConversationURL constructs the universal conversation link for a
// session: <endpoint>/c/<ses_id>. This is the durable canonical URL for
// artifacts that outlive the recording (commit trailers, PR bodies, plan
// footers) — unlike buildSessionURL it does not depend on repo_id or the
// mutable session directory name. Returns "" when the endpoint is missing
// or sessionID is not a valid ses_ identifier (e.g. recordings started
// under an older binary), so callers can fall back to the name-based URL.
func buildConversationURL(cfg *config.ProjectConfig, sessionID string) string {
	if cfg == nil || !sessionid.IsValidSessionID(sessionID) {
		return ""
	}
	ep := endpoint.NormalizeEndpoint(cfg.GetEndpoint())
	if ep == "" {
		return ""
	}
	// the endpoint comes from committed, team-editable config and this URL
	// is handed to `git interpret-trailers` as a --trailer value: any
	// whitespace or control character would smuggle extra trailer lines
	// into commit messages, so reject the endpoint outright
	if strings.ContainsFunc(ep, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return ""
	}
	return fmt.Sprintf("%s/c/%s", ep, url.PathEscape(sessionID))
}

// sessionLinkOutputs derives the prime session URL and the exact-literal PR
// directive for a live recording. The attribution.session toggle gates both:
// an empty toggle (attribution.session: "") or a missing/unlinkable state
// yields ("", "") so every session-link surface disables together. The /c/
// form is preferred; recordings started under an older binary carry no
// start-minted ID and fall back to the name-based view URL.
func sessionLinkOutputs(projCfg *config.ProjectConfig, state *session.RecordingState, attrSession string) (sessionURL, prDirective string) {
	if attrSession == "" || state == nil {
		return "", ""
	}
	if state.LifecycleRegistrationState == "pending" {
		// A locally minted id is not evidence that the remote resolver knows
		// it. Keep the link out of commit/PR guidance until a retry or upload
		// makes it server-visible.
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
