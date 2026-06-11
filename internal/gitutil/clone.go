package gitutil

// Clone-safety helpers. Leaf utilities (no imports from internal/daemon,
// internal/ledger, or internal/gitserver) so every caller — CLI, daemon, ledger
// — applies the same clone invariants without import cycles. See ADR-022 and
// security/SECURITY.md for the trust model these enforce.

import (
	"fmt"
	"net/url"
	"strings"
)

// ValidateCloneURL rejects clone URLs that can turn `git clone` into arbitrary
// command execution or local-file access.
//
// git's `ext::` transport forks a shell command, and `file://` reaches the local
// filesystem; either is RCE/SSRF when the URL is sourced from an attacker-
// influenced channel (a tampered on-disk credentials file, a compromised API
// response). This validator is the first of two independent defenses; the second
// is HardenedCloneArgs, which disables those transports at the git level even if
// a URL slips through.
//
// Scheme policy:
//   - https://         always allowed
//   - http://          allowed ONLY for localhost / 127.0.0.1 (local dev)
//   - everything else  rejected (ext://, git://, ssh://, file://, …)
//
// Host policy: if trustedHosts is non-empty, the URL host must equal one of them
// or be a subdomain of one. If trustedHosts is empty, any host is accepted (the
// caller is relying on scheme validation + HardenedCloneArgs alone — appropriate
// for user-owned ledger repos that may live on github.com, gitlab.com, or a
// self-hosted forge).
//
// allowLocal permits `file://` URLs and scheme-less local filesystem paths. In
// production this is false (a ledger/team-context clone is always a remote https
// repo, never a local path). Tests that clone from a local bare repo wire this to
// the test-only override (gitserver.TestAllowFileTransport) — the same flag that
// gates HardenedCloneArgs — so both guards relax together. The `ext::` transport
// is rejected regardless of allowLocal: it forks a shell and is never legitimate.
func ValidateCloneURL(cloneURL string, trustedHosts []string, allowLocal bool) error {
	if cloneURL == "" {
		return fmt.Errorf("clone URL is empty")
	}
	// A leading dash would be parsed by git as a flag, not a positional. Callers
	// must also pass "--" before the URL (HardenedCloneArgs callers do), but
	// reject it here too so validation alone is sufficient.
	if strings.HasPrefix(cloneURL, "-") {
		return fmt.Errorf("clone URL may not start with '-': %q", cloneURL)
	}

	parsed, err := url.Parse(cloneURL)
	if err != nil {
		return fmt.Errorf("invalid clone URL %q: %w", cloneURL, err)
	}

	// Local filesystem paths (no scheme) and file:// URLs: only when explicitly
	// allowed. ext::/git:// etc. fall through to the scheme switch and are rejected.
	if parsed.Scheme == "" || parsed.Scheme == "file" {
		if allowLocal {
			return nil
		}
		return fmt.Errorf("local/file clone URLs are not permitted: %s", cloneURL)
	}

	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return fmt.Errorf("clone URL has no host: %s", cloneURL)
	}

	isLocalHost := host == "localhost" || host == "127.0.0.1"

	switch parsed.Scheme {
	case "https":
		// ok
	case "http":
		if !isLocalHost {
			return fmt.Errorf("only https:// is supported for remote hosts, got: %s", cloneURL)
		}
		return nil // local dev over http needs no host allowlist
	default:
		return fmt.Errorf("unsupported clone URL scheme %q (only https is allowed): %s", parsed.Scheme, cloneURL)
	}

	if len(trustedHosts) == 0 {
		return nil
	}
	for _, trusted := range trustedHosts {
		if host == trusted || strings.HasSuffix(host, "."+trusted) {
			return nil
		}
	}
	return fmt.Errorf("untrusted git host: %s (allowed: %v)", parsed.Host, trustedHosts)
}

// HardenedCloneArgs returns the `-c` flags that disable git's dangerous
// transports for a clone. Prepend these to the git argument list, before the
// "clone" subcommand. Always pass "--" before the positional <url> <path> too,
// so a hostile URL can never be parsed as a flag.
//
// allowFileTransport must be wired to a test-only override (e.g.
// gitserver.TestAllowFileTransport) so the suite can clone from file:// bare
// repos while production stays locked. In production it is always false.
func HardenedCloneArgs(allowFileTransport bool) []string {
	args := []string{"-c", "protocol.ext.allow=never"}
	if !allowFileTransport {
		args = append(args, "-c", "protocol.file.allow=never")
	}
	return args
}
