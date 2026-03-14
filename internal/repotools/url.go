package repotools

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// normalizeGitURL converts various git URL formats to a canonical form
// Handles: git@github.com:org/repo.git, https://github.com/org/repo.git,
// ssh://git@github.com/org/repo.git, etc.
func normalizeGitURL(url string) string {
	// remove .git suffix
	url = strings.TrimSuffix(url, ".git")

	// remove protocol prefixes first so ssh://git@... is handled correctly
	url = strings.TrimPrefix(url, "https://")
	url = strings.TrimPrefix(url, "http://")
	url = strings.TrimPrefix(url, "ssh://")

	// convert SSH to HTTPS-like format for consistency
	// git@github.com:org/repo -> github.com/org/repo
	// git@github.com/org/repo -> github.com/org/repo (after ssh:// strip)
	// git@host:2222/org/repo -> host/org/repo (port stripped)
	if strings.HasPrefix(url, "git@") {
		url = strings.TrimPrefix(url, "git@")
		// SCP-style uses colon as path separator (host:path).
		// If a slash exists and a colon precedes it, the colon is either:
		//   - an SCP path separator (host:org/repo) -> replace with /
		//   - a port separator (host:2222/org/repo) -> strip host:port, keep /path
		if slashIdx := strings.Index(url, "/"); slashIdx >= 0 {
			if colonIdx := strings.Index(url, ":"); colonIdx >= 0 && colonIdx < slashIdx {
				portOrPath := url[colonIdx+1 : slashIdx]
				if isNumeric(portOrPath) {
					// port: host:2222/org/repo -> host/org/repo
					url = url[:colonIdx] + url[slashIdx:]
				} else {
					// SCP path: host:org/repo -> host/org/repo
					url = url[:colonIdx] + "/" + url[colonIdx+1:]
				}
			}
		} else if colonIdx := strings.Index(url, ":"); colonIdx >= 0 {
			// no slash at all: host:org (rare SCP without subpath)
			url = url[:colonIdx] + "/" + url[colonIdx+1:]
		}
	}

	// lowercase for consistency
	return strings.ToLower(url)
}

// isNumeric returns true if s is a non-empty string of digits (e.g. a port number).
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// HashRemoteURLs creates salted SHA256 hashes of remote URLs.
//
// SECURITY: Remote URLs are hashed (not sent plaintext) to protect repo identity.
// Knowing a repo's remote URL can reveal private repo names, internal tooling,
// or organizational structure. The hash allows the server to detect when two
// repos share the same origin (for merge detection) without learning the actual URL.
//
// The salt (typically the repo's first commit hash) prevents enumeration attacks:
// an attacker with server access cannot precompute hashes for known repo URLs
// like "github.com/company/secret-project" to identify which repos are registered.
// Each repo's salt is unique, so the same URL produces different hashes for different repos.
func HashRemoteURLs(salt string, urls []string) []string {
	var hashes []string
	for _, url := range urls {
		// salt + url to prevent rainbow table attacks
		data := salt + ":" + url
		hash := sha256.Sum256([]byte(data))
		hashes = append(hashes, "sha256:"+hex.EncodeToString(hash[:]))
	}
	return hashes
}
