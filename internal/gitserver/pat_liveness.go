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
// NOTE: Currently GitLab-specific (PRIVATE-TOKEN header, /api/v4/user endpoint).
// SageOx uses GitLab for all git hosting. If multi-provider support is needed,
// this should detect the provider and adapt the probe strategy.
//
// Timeout should be kept short (2-3s) so callers (doctor, status) stay responsive.
func ValidatePATLiveness(ctx context.Context, serverURL, token string) PATLivenessResult {
	if serverURL == "" || token == "" {
		return PATLivenessResult{Skipped: true, Reason: "no credentials"}
	}

	// build a lightweight API probe URL — GitLab /api/v4/user returns 200 for valid tokens
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
func buildProbeURL(serverURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	// ensure scheme
	if u.Scheme == "" {
		u.Scheme = "https"
	}
	// GitLab /api/v4/user is lightweight and returns 200 for valid PATs
	u.Path = "/api/v4/user"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
