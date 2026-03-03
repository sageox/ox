package main

import (
	"fmt"
	"net/url"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
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
