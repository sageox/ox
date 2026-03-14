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
	if strings.HasPrefix(url, "git@") {
		url = strings.TrimPrefix(url, "git@")
		url = strings.Replace(url, ":", "/", 1)
	}

	// lowercase for consistency
	return strings.ToLower(url)
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
