// Package sessionupload holds the upload-confirmed signal a session sends the
// SageOx server once its content is on the Ledger's origin. Every path that
// pushes a session sends it: `ox agent session stop`, `ox session recover`,
// `ox session import`, and the daemon's finalize. Without it the session's
// /c/<session_id> page stays "in progress" though the session is uploaded.
package sessionupload

import (
	"log/slog"
	"net/url"
	"strings"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/sageox/ox/internal/weblink"
)

// ConversationURL constructs the universal conversation link for a session:
// <endpoint>/c/<ses_id>. This is the durable canonical URL for artifacts that
// outlive the recording (commit trailers, PR bodies, plan footers) — it does
// not depend on repo_id or the mutable session directory name. Returns ""
// when the endpoint is missing or sessionID is not a valid ses_ identifier
// (e.g. recordings started under an older binary), so callers can fall back
// to the name-based URL.
func ConversationURL(cfg *config.ProjectConfig, sessionID string) string {
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
	return weblink.ArtifactURL(ep, sessionID, "/c/", "ses_")
}

// Notify sends the upload-confirmed notification to the SageOx server.
// Returns the server-reported PR-link misses plus true on success (including
// 404/409 graceful cases, which the API client maps to nil error), false on
// transient failure that should be retried by doctor.
func Notify(projectRoot string, meta *lfs.SessionMeta, sessionName string) ([]api.PRLinkMiss, bool) {
	if !canNotify(meta) {
		return nil, false
	}
	cfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil || cfg == nil || cfg.RepoID == "" {
		return nil, false
	}

	ep := endpoint.GetForProject(projectRoot)
	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil || token == nil || token.AccessToken == "" {
		// no credentials — can't notify; doctor retries once auth is present
		return nil, false
	}

	client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken)
	notification := api.SessionUploadedNotification{
		SessionID:       meta.EffectiveSessionID(),
		RepoID:          cfg.RepoID,
		SessionName:     sessionName,
		SessionURL:      ConversationURL(cfg, meta.EffectiveSessionID()),
		LinkedPRs:       meta.LinkedPRs,
		LinkedIssues:    meta.LinkedIssues,
		ProducedCommits: meta.ProducedCommits,
	}
	misses, err := client.NotifySessionUploaded(notification)
	if err != nil {
		slog.Debug("linkage finalize: notify failed", "error", err, "session", sessionName)
		return nil, false
	}
	return FilterPRLinkMisses(misses, "SageOx-Session: "+notification.SessionURL), true
}

// canNotify reports whether meta's session ID is safe to put in the signal's
// URL path. The ID comes from meta.json on the Ledger, which any teammate can
// edit, and an ID like "ses_x/aborted?" would send the authenticated POST to a
// different endpoint.
func canNotify(meta *lfs.SessionMeta) bool {
	return meta != nil && sessionid.IsValidSessionID(meta.EffectiveSessionID())
}

// FilterPRLinkMisses guards the trust boundary between the server and agent
// guidance: misses are server-controlled input that gets formatted into a
// "REPAIR REQUIRED" instruction the agent will act on. Accept only misses
// whose ExpectedLine exactly equals the locally-derived trailer for THIS
// session and whose PR URL is a clean https link — the server confirms a
// miss, it never gets to author the instruction.
func FilterPRLinkMisses(misses []api.PRLinkMiss, expectedLine string) []api.PRLinkMiss {
	if strings.TrimPrefix(expectedLine, "SageOx-Session: ") == "" {
		return nil
	}
	var out []api.PRLinkMiss
	for _, m := range misses {
		if m.ExpectedLine != expectedLine {
			continue
		}
		if strings.ContainsAny(m.PRURL, " \t\r\n") {
			continue
		}
		u, err := url.Parse(m.PRURL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			continue
		}
		out = append(out, m)
	}
	return out
}
