package github

import (
	"strings"
)

// ParseGitHubRemote extracts owner and repo from a git remote URL.
// Supports:
//   - https://github.com/owner/repo.git
//   - git@github.com:owner/repo.git
//   - ssh://git@github.com/owner/repo.git
//
// Returns ("", "", false) for non-GitHub URLs.
func ParseGitHubRemote(remoteURL string) (owner, repo string, ok bool) {
	raw := strings.TrimSpace(remoteURL)
	raw = strings.TrimSuffix(raw, ".git")

	var path string

	switch {
	case strings.HasPrefix(raw, "git@github.com:"):
		// git@github.com:owner/repo
		path = strings.TrimPrefix(raw, "git@github.com:")

	case strings.HasPrefix(raw, "ssh://git@github.com/"):
		// ssh://git@github.com/owner/repo
		path = strings.TrimPrefix(raw, "ssh://git@github.com/")

	case strings.Contains(raw, "://"):
		// https://github.com/owner/repo or http://github.com/owner/repo
		after := raw[strings.Index(raw, "://")+3:]
		if !strings.HasPrefix(after, "github.com/") {
			return "", "", false
		}
		path = strings.TrimPrefix(after, "github.com/")

	default:
		return "", "", false
	}

	// path should be "owner/repo" with exactly one slash
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}

	return parts[0], parts[1], true
}
