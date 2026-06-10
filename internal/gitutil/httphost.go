package gitutil

import (
	"fmt"
	"net/url"
	"strings"
)

// ValidateHTTPSHost rejects a URL whose scheme is not https or whose host is not
// in the allowlist. It is used before fetching attacker-influenced URLs (e.g. an
// adapter asset's browser_download_url taken from a GitHub API response).
//
// Per ADR-022 (decision 4) this is a transport guard, NOT the primary integrity
// control: a host allowlist cannot stop malicious bytes served at a legitimate
// URL, and over-tight host lists break when a CDN rotates hosts. The primary
// control for downloaded binaries is checksum verification. Use this only as
// defense-in-depth alongside a checksum gate, never as a substitute.
func ValidateHTTPSHost(rawURL string, allowedHosts map[string]bool) error {
	if rawURL == "" {
		return fmt.Errorf("URL is empty")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("URL must use https, got %q in: %s", parsed.Scheme, rawURL)
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return fmt.Errorf("URL has no host: %s", rawURL)
	}
	if len(allowedHosts) == 0 {
		return nil
	}
	if allowedHosts[host] {
		return nil
	}
	return fmt.Errorf("URL host %q not in allowlist: %s", host, rawURL)
}
