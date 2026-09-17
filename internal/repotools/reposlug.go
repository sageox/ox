package repotools

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// RepoSlug extracts "owner/repo" from the git remote origin URL, falling back to
// the directory name when the remote is unavailable.
// Examples: "sageox/ox", "my-project"
//
// It is offline-safe: a local-only repository with no origin yields its
// directory name rather than an error.
//
// This lives here rather than in `cmd/ox` because the slug is the key to the
// `repos:` frontmatter filter that teamdocs.DiscoverRules and
// teamdocs.DiscoverSkills both apply. Prime resolves it for rules and the skill
// reconciler resolves it for skills; two implementations would let the same team
// document apply in one path and not the other, which is indistinguishable from
// the document being missing.
func RepoSlug(projectRoot string) string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = projectRoot
	out, err := cmd.Output()
	if err == nil {
		url := strings.TrimSpace(string(out))
		// handle SSH: git@github.com:owner/repo.git
		if idx := strings.Index(url, ":"); idx != -1 && !strings.Contains(url[:idx], "/") {
			url = url[idx+1:]
		}
		// handle HTTPS: https://github.com/owner/repo.git
		url = strings.TrimSuffix(url, ".git")
		parts := strings.Split(url, "/")
		if len(parts) >= 2 {
			return parts[len(parts)-2] + "/" + parts[len(parts)-1]
		}
	}
	return filepath.Base(projectRoot)
}
