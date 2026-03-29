package gitserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sageox/ox/internal/gitutil"
)

// PATLivenessResult describes the outcome of a PAT liveness check.
type PATLivenessResult struct {
	// Valid is true if the PAT authenticated successfully
	Valid bool
	// Reason describes the failure (empty if valid)
	Reason string
	// Skipped is true if the check couldn't run (no creds, no repo URL)
	Skipped bool
}

// ValidatePATLiveness probes the git server with the stored PAT to verify it
// actually authenticates. Uses `git ls-remote` against a known repo URL with
// the PAT provided via GIT_ASKPASS (never on the command line). This only
// requires basic git access (no extra API scopes like read_api).
//
// If the token is valid, git ls-remote returns refs (exit 0).
// If the token is expired/revoked, git returns exit 128 with a 401 error.
//
// Requires at least one repo URL in credentials to probe against.
// Timeout should be kept short (3s) so callers (doctor, status) stay responsive.
func ValidatePATLiveness(ctx context.Context, creds *GitCredentials) PATLivenessResult {
	if creds == nil || creds.Token == "" {
		return PATLivenessResult{Skipped: true, Reason: "no credentials"}
	}

	// pick a repo URL to probe — any repo will do for auth validation
	repoURL := pickProbeRepoURL(creds)
	if repoURL == "" {
		return PATLivenessResult{Skipped: true, Reason: "no repo URL available"}
	}

	// inject oauth2 userinfo into the URL (username only, no token on argv)
	probeURL, err := buildProbeURL(repoURL)
	if err != nil {
		return PATLivenessResult{Skipped: true, Reason: fmt.Sprintf("invalid repo URL: %s", err)}
	}

	// create a temporary GIT_ASKPASS script that returns the token
	// this avoids putting the token on the command line (visible in ps)
	askpass, err := writeAskpassScript(creds.Token)
	if err != nil {
		return PATLivenessResult{Skipped: true, Reason: fmt.Sprintf("askpass setup: %s", err)}
	}
	defer os.Remove(askpass)

	slog.Debug("PAT liveness: probing", "cmd", "git ls-remote --heads <repo>", "repo", repoURL)

	// git ls-remote with just HEAD ref to minimize data transfer
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--heads", probeURL)
	// ensure the process is killed promptly when context expires
	cmd.WaitDelay = 100 * time.Millisecond
	// use GIT_ASKPASS for credentials, suppress interactive prompts and
	// system credential helpers (e.g. osxkeychain) so they can't override
	// the fresh token we're testing with
	cmd.Env = append(cmd.Environ(),
		"GIT_ASKPASS="+askpass,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=", // disable credential helper for this probe
	)

	output, err := cmd.CombinedOutput()
	// always sanitize output before logging — never expose tokens
	sanitized := gitutil.SanitizeOutput(strings.TrimSpace(string(output)))

	if err != nil {
		sanitizedLower := strings.ToLower(sanitized)
		// auth failures show as 401/403 in the git output
		if strings.Contains(sanitizedLower, "401") ||
			strings.Contains(sanitizedLower, "403") ||
			strings.Contains(sanitizedLower, "authentication failed") ||
			strings.Contains(sanitizedLower, "could not read username") {
			slog.Debug("PAT liveness: rejected", "output", sanitized)
			return PATLivenessResult{Valid: false, Reason: "PAT rejected by server (revoked or invalid)"}
		}
		// other failures (network, DNS, etc.) — skip rather than report as auth failure
		slog.Debug("PAT liveness: network error", "output", sanitized)
		return PATLivenessResult{Skipped: true, Reason: "network unreachable"}
	}

	// count refs returned
	lines := strings.Split(sanitized, "\n")
	refCount := 0
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			refCount++
		}
	}
	slog.Debug("PAT liveness: valid", "refs", refCount)
	return PATLivenessResult{Valid: true}
}

// pickProbeRepoURL selects a repo URL from credentials to use for the liveness probe.
// Returns empty string if no repos are available.
func pickProbeRepoURL(creds *GitCredentials) string {
	if len(creds.Repos) == 0 {
		return ""
	}
	// pick the first available repo — order doesn't matter for auth validation
	for _, repo := range creds.Repos {
		if repo.URL != "" {
			return repo.URL
		}
	}
	return ""
}

// buildProbeURL injects the oauth2 username into the repo URL (without the token).
// The actual token is provided via GIT_ASKPASS to avoid exposing it in argv.
// Transforms https://host/path.git → https://oauth2@host/path.git
func buildProbeURL(repoURL string) (string, error) {
	u, err := url.Parse(repoURL)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" {
		u.Scheme = "https"
	}
	u.User = url.User("oauth2")
	return u.String(), nil
}

// ClearCredentialHelperEntry evicts any stored credential for the given git
// server from all configured credential helpers (osxkeychain, libsecret,
// wincred, etc.) using `git credential reject`. This prevents stale entries
// from a previous install from silently overriding GIT_ASKPASS in future
// git operations.
//
// Failures are logged but not returned — this is best-effort cleanup.
// serverURL should be the git server base URL (e.g. "https://git.sageox.ai").
func ClearCredentialHelperEntry(serverURL string) {
	u, err := url.Parse(serverURL)
	if err != nil || u.Host == "" {
		slog.Debug("credential helper clear: skipped", "reason", "invalid server URL", "url", serverURL)
		return
	}

	protocol := u.Scheme
	if protocol == "" {
		protocol = "https"
	}
	host := u.Hostname()

	// `git credential reject` reads protocol/host/username from stdin and
	// instructs each configured helper to delete the matching entry.
	input := fmt.Sprintf("protocol=%s\nhost=%s\nusername=oauth2\n\n", protocol, host)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "credential", "reject")
	cmd.Stdin = strings.NewReader(input)
	if out, err := cmd.CombinedOutput(); err != nil {
		slog.Debug("credential helper clear: git credential reject failed",
			"host", host, "error", err, "output", strings.TrimSpace(string(out)))
	} else {
		slog.Debug("credential helper clear: evicted stale entry", "host", host)
	}
}

// writeAskpassScript creates a temporary script that echoes the token.
// Git calls this script with a prompt like "Password for ..." and expects
// the credential on stdout. Returns the path to the script.
func writeAskpassScript(token string) (string, error) {
	f, err := os.CreateTemp("", "ox-askpass-*")
	if err != nil {
		return "", err
	}
	// write a minimal script that echoes the token
	_, err = fmt.Fprintf(f, "#!/bin/sh\necho '%s'\n", strings.ReplaceAll(token, "'", "'\\''"))
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	f.Close()
	// make it executable
	if err := os.Chmod(f.Name(), 0700); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
