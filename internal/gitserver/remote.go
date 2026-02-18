package gitserver

import (
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// RefreshRemoteCredentials updates a repo's git remote URL with the current PAT
// from the credential store. Handles three cases:
//   - Stale PAT: replaces with current credentials
//   - Bare HTTPS URL (no PAT): inserts credentials if host matches credential store
//   - Current PAT: no-op
//
// No-op for SSH URLs or non-oauth2 usernames (deploy tokens).
// Returns nil on success or no-op. Returns an error if credentials are unavailable
// or the git command fails — callers should log and continue, not abort.
func RefreshRemoteCredentials(repoPath, endpointURL string) error {
	pat, remoteURL, err := extractPATFromRemote(repoPath)
	if err != nil {
		return err
	}

	// SSH URLs — nothing to do
	if isSSHURL(remoteURL) {
		return nil
	}

	// check if URL has non-oauth2 userinfo (deploy tokens, etc.) — don't touch
	if pat == "" && hasNonOauth2Userinfo(remoteURL) {
		return nil
	}

	// load current credentials from store
	creds, err := LoadCredentialsForEndpoint(endpointURL)
	if err != nil {
		return fmt.Errorf("load credentials: %w", err)
	}
	if creds == nil || creds.Token == "" {
		if pat == "" {
			return nil // bare URL, no credentials to insert
		}
		return fmt.Errorf("no credentials stored for endpoint %s", endpointURL)
	}

	// don't update remote with expired credentials — they'll fail auth anyway
	if !creds.ExpiresAt.IsZero() && creds.ExpiresAt.Before(time.Now()) {
		return fmt.Errorf("credentials expired for endpoint %s", endpointURL)
	}

	// already current
	if pat == creds.Token {
		return nil
	}

	// verify the remote host matches the credential server (multi-endpoint safety)
	if creds.ServerURL != "" {
		remoteHost := extractHost(remoteURL)
		credHost := extractHost(creds.ServerURL)
		if remoteHost != "" && credHost != "" && remoteHost != credHost {
			return nil // different server, not our repo to update
		}
	}

	// rebuild URL with current PAT
	parsed, err := url.Parse(remoteURL)
	if err != nil {
		return fmt.Errorf("parse remote URL: %w", err)
	}
	parsed.User = url.UserPassword("oauth2", creds.Token)
	newURL := parsed.String()

	// update the remote
	cmd := exec.Command("git", "-C", repoPath, "remote", "set-url", "origin", newURL)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git remote set-url: %s: %w", strings.TrimSpace(string(output)), err)
	}

	return nil
}

// extractPATFromRemote reads the origin remote URL and extracts any embedded PAT.
// Returns ("", url, nil) for SSH URLs, bare URLs, or URLs without userinfo.
// Returns ("", "", err) if the remote can't be read.
func extractPATFromRemote(repoPath string) (pat string, remoteURL string, err error) {
	cmd := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin")
	output, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("get remote URL: %w", err)
	}
	remoteURL = strings.TrimSpace(string(output))

	// SSH URLs don't have embedded PATs
	if isSSHURL(remoteURL) {
		return "", remoteURL, nil
	}

	parsed, err := url.Parse(remoteURL)
	if err != nil {
		return "", remoteURL, nil // unparseable, treat as no PAT
	}

	if parsed.User == nil {
		return "", remoteURL, nil
	}

	// only handle oauth2-style auth (ox-managed)
	if parsed.User.Username() != "oauth2" {
		return "", remoteURL, nil
	}

	password, hasPassword := parsed.User.Password()
	if !hasPassword || password == "" {
		return "", remoteURL, nil
	}

	return password, remoteURL, nil
}

// SanitizeRemoteURL removes credentials from a URL for safe display.
// Returns the original string for SSH URLs or unparseable URLs.
func SanitizeRemoteURL(rawURL string) string {
	if isSSHURL(rawURL) {
		return rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	parsed.User = nil
	return parsed.String()
}

// StripRemoteCredentials removes embedded credentials from a repo's git remote URL.
// Transforms https://oauth2:TOKEN@host/repo.git → https://host/repo.git
// No-op for SSH URLs, bare URLs, or URLs without oauth2 userinfo.
func StripRemoteCredentials(repoPath string) error {
	pat, remoteURL, err := extractPATFromRemote(repoPath)
	if err != nil {
		return err
	}

	// no embedded PAT — nothing to strip
	if pat == "" {
		return nil
	}

	parsed, err := url.Parse(remoteURL)
	if err != nil {
		return fmt.Errorf("parse remote URL: %w", err)
	}
	parsed.User = nil
	bareURL := parsed.String()

	cmd := exec.Command("git", "-C", repoPath, "remote", "set-url", "origin", bareURL)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git remote set-url: %s: %w", strings.TrimSpace(string(output)), err)
	}

	return nil
}

// hasNonOauth2Userinfo checks if a URL has userinfo that isn't oauth2-style.
// Used to avoid overwriting deploy tokens or other non-ox credentials.
func hasNonOauth2Userinfo(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if parsed.User == nil {
		return false
	}
	return parsed.User.Username() != "oauth2"
}

// extractHost returns the lowercase hostname from a URL.
// Returns empty string on parse failure.
func extractHost(rawURL string) string {
	// handle bare hostnames (no scheme)
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}
