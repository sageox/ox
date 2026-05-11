package gitserver

import (
	"fmt"
	"net/url"
	"os/exec"
	"strings"
)

// RefreshRemoteCredentials reconciles a repo's git auth setup with the
// current credential store. Per ox-eeqi, this no longer embeds the PAT into
// the origin URL. Instead it:
//   - strips any leftover embedded oauth2:TOKEN from origin (one-time
//     migration for ledgers cloned by pre-eeqi ox versions), and
//   - installs/refreshes the ox credential helper in .git/config so future
//     fetch/push operations resolve auth via the helper.
//
// The endpointURL parameter is retained for API compatibility with existing
// callers (login.go, session_upload.go, import.go) but is now used only to
// surface a clearer warning when no credentials are stored for the endpoint
// the repo is going to push to. The helper itself looks credentials up by
// git host at invocation time.
//
// No-op for SSH URLs, local remotes, and non-oauth2 userinfo (deploy tokens).
// Returns nil on success or no-op. offline-safe: missing origin is a clean
// "nothing to do."
func RefreshRemoteCredentials(repoPath, endpointURL string) error {
	pat, remoteURL, err := extractPATFromRemote(repoPath)
	if err != nil {
		// missing origin / unreadable config — let caller proceed; the
		// next git operation will surface a clearer error.
		return nil
	}

	// SSH URLs — nothing to migrate.
	if isSSHURL(remoteURL) {
		return nil
	}

	// Local remotes (file:// or absolute paths) — never inject creds.
	if isLocalRemote(remoteURL) {
		return nil
	}

	// Non-ox userinfo (deploy tokens) — leave alone.
	if pat == "" && hasNonOauth2Userinfo(remoteURL) {
		return nil
	}

	// Host-match guard: only migrate repos whose origin host matches the
	// endpoint we know about. If the caller passed an explicit endpoint and
	// it doesn't match the remote host's derived endpoint, skip — this is
	// almost always a third-party remote (a fork, an upstream pointer, a
	// vendored mirror) and ox has no business touching its credentials.
	repoEp := endpointFromRemoteURL(remoteURL)
	if repoEp == "" {
		return nil
	}
	if endpointURL != "" && !endpointHostsEqual(endpointURL, repoEp) {
		// repo's origin is on a different server than the endpoint we
		// were asked to refresh — no-op.
		return nil
	}

	// Run the helper migration: strip embedded PAT + install helper.
	// Idempotent. The migration internally checks for ssh:// / file:// /
	// non-oauth2 userinfo, so this call is safe to make unconditionally
	// once the host-match guard above has passed.
	_, err = MigrateLedgerCredentials(repoPath, DefaultHelperCommand())
	if err != nil {
		return fmt.Errorf("migrate credentials: %w", err)
	}
	return nil
}

// endpointHostsEqual returns true if two endpoint URLs name the same host
// (case-insensitive, scheme-insensitive, ignoring trailing slash). Used by
// RefreshRemoteCredentials to decide whether the caller's endpoint matches
// the repo's origin host.
func endpointHostsEqual(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(a, "/"), strings.TrimRight(b, "/")) ||
		strings.EqualFold(extractHost(a), extractHost(b))
}

// these are kept to avoid pulling new imports if downstream files reference them
var _ = url.Parse
var _ = exec.Command
var _ = strings.TrimSpace

// endpointFromRemoteURL extracts the SageOx endpoint from a git remote URL.
// Strips the git. prefix and scheme to produce an endpoint URL that matches
// the credential store's slug format.
// e.g., https://oauth2:TOKEN@git.sageox.ai/repo.git → https://sageox.ai
// e.g., https://git.test.sageox.ai/repo.git → https://test.sageox.ai
func endpointFromRemoteURL(remoteURL string) string {
	parsed, err := url.Parse(remoteURL)
	if err != nil || parsed.Host == "" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	// strip git. prefix to get the base endpoint domain
	host = strings.TrimPrefix(host, "git.")
	if host == "" {
		return ""
	}
	scheme := parsed.Scheme
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + host
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

// GetBareRemoteURL returns the origin remote URL with credentials stripped.
// Useful when you need the repo URL for API derivation (e.g., LFS batch endpoint)
// without embedding the PAT.
// offline-safe: returns error for repos with no origin remote; callers must handle
func GetBareRemoteURL(repoPath string) (string, error) {
	_, remoteURL, err := extractPATFromRemote(repoPath)
	if err != nil {
		return "", err
	}
	return SanitizeRemoteURL(remoteURL), nil
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
