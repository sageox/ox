package gitserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// PATLivenessResult describes the outcome of a PAT liveness check.
type PATLivenessResult struct {
	// Valid is true if the PAT authenticated successfully
	Valid bool
	// Reason describes the failure (empty if valid)
	Reason string
	// Skipped is true if the check couldn't run (no creds, no server URL)
	Skipped bool
}

// ValidatePATLiveness probes the git server with the stored PAT to verify it
// actually authenticates. Uses an HTTP GET request against the GitLab API
// rather than git ls-remote, since we may not have a repo URL handy and we
// want to avoid git subprocess overhead.
//
// NOTE: Currently GitLab-specific (PRIVATE-TOKEN header,
// /api/v4/personal_access_tokens/self endpoint). SageOx uses GitLab for all
// git hosting. We use the token introspection endpoint because it works with
// any valid PAT regardless of scopes — our PATs only have
// [read_repository, write_repository], which is insufficient for /api/v4/user.
//
// Timeout should be kept short (2-3s) so callers (doctor, status) stay responsive.
func ValidatePATLiveness(ctx context.Context, serverURL, token string) PATLivenessResult {
	if serverURL == "" || token == "" {
		return PATLivenessResult{Skipped: true, Reason: "no credentials"}
	}

	// build a lightweight API probe URL — token introspection works with any scopes
	probeURL, err := buildProbeURL(serverURL)
	if err != nil {
		return PATLivenessResult{Skipped: true, Reason: fmt.Sprintf("invalid server URL: %s", err)}
	}

	client := &http.Client{Timeout: 3 * time.Second}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return PATLivenessResult{Skipped: true, Reason: fmt.Sprintf("request error: %s", err)}
	}
	req.Header.Set("PRIVATE-TOKEN", token)

	resp, err := client.Do(req)
	if err != nil {
		// network error, not an auth failure
		return PATLivenessResult{Skipped: true, Reason: "network unreachable"}
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return PATLivenessResult{Valid: true}
	case http.StatusUnauthorized, http.StatusForbidden:
		return PATLivenessResult{Valid: false, Reason: "PAT rejected by server (revoked or invalid)"}
	default:
		return PATLivenessResult{Skipped: true, Reason: fmt.Sprintf("unexpected status %d", resp.StatusCode)}
	}
}

// buildProbeURL constructs a lightweight GitLab API endpoint to test PAT validity.
// Uses /api/v4/personal_access_tokens/self which works with any valid PAT
// regardless of scopes (unlike /api/v4/user which requires read_user or api scope).
func buildProbeURL(serverURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" {
		u.Scheme = "https"
	}
	u.Path = "/api/v4/personal_access_tokens/self"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
