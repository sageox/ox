package repotools

import (
	"fmt"
	"os/exec"
	"strings"
)

// GitIdentity holds git user configuration
type GitIdentity struct {
	Name  string // git config user.name
	Email string // git config user.email
}

// DetectGitIdentity reads git user configuration
// Returns nil if no identity is configured (both name and email empty)
func DetectGitIdentity() (*GitIdentity, error) {
	if err := RequireVCS(VCSGit); err != nil {
		return nil, err
	}

	name := getGitConfig("user.name")
	email := getGitConfig("user.email")

	if name == "" && email == "" {
		return nil, nil
	}

	return &GitIdentity{
		Name:  name,
		Email: email,
	}, nil
}

// Slug returns a filesystem-safe identifier for the git identity
// Uses email username (before @) if available, otherwise name
// Example: "ryan@example.com" -> "ryan"
func (g *GitIdentity) Slug() string {
	if g == nil {
		return "anonymous"
	}

	identifier := g.Email
	if identifier != "" {
		// extract username part before @ for emails
		if atIdx := strings.Index(identifier, "@"); atIdx > 0 {
			identifier = identifier[:atIdx]
		}
	}
	if identifier == "" {
		identifier = g.Name
	}
	if identifier == "" {
		return "anonymous"
	}

	slug := slugify(identifier)
	if slug == "" {
		return "anonymous"
	}

	return slug
}

// GetInitialCommitHash returns the hash of the initial (first) commit in the repo
// This is used as repo_salt for secure hashing of remote URLs
func GetInitialCommitHash() (string, error) {
	if err := RequireVCS(VCSGit); err != nil {
		return "", err
	}

	// git rev-list --max-parents=0 HEAD returns the initial commit(s)
	cmd := exec.Command("git", "rev-list", "--max-parents=0", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get initial commit: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return "", fmt.Errorf("no initial commit found")
	}

	// return the first (oldest) initial commit
	return lines[0], nil
}

// GetRemoteURLs returns all configured git remote URLs
func GetRemoteURLs() ([]string, error) {
	if err := RequireVCS(VCSGit); err != nil {
		return nil, err
	}

	cmd := exec.Command("git", "remote", "-v")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get remotes: %w", err)
	}

	// parse remote output: origin	git@github.com:org/repo.git (fetch)
	var urls []string
	seen := make(map[string]bool)
	lines := strings.Split(string(output), "\n")

	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			url := normalizeGitURL(fields[1])
			if url != "" && !seen[url] {
				urls = append(urls, url)
				seen[url] = true
			}
		}
	}

	return urls, nil
}

// GetRepoName returns a human-readable repo name derived from git remotes.
// Prefers "owner/repo" extracted from the first remote origin URL
// (e.g. git@github.com:sageox/ox.git → "sageox/ox").
// Falls back to the git root directory name if no remote is available.
func GetRepoName(gitRoot string) string {
	// try first remote origin
	urls, err := GetRemoteURLs()
	if err == nil && len(urls) > 0 {
		// urls are already normalized: "github.com/sageox/ox"
		// strip the host to get "sageox/ox"
		normalized := urls[0]
		if idx := strings.Index(normalized, "/"); idx >= 0 {
			ownerRepo := normalized[idx+1:]
			if ownerRepo != "" {
				return ownerRepo
			}
		}
	}

	// fallback: directory name of the git root
	if gitRoot != "" {
		// handle trailing slashes
		cleaned := strings.TrimRight(gitRoot, "/")
		if idx := strings.LastIndex(cleaned, "/"); idx >= 0 {
			return cleaned[idx+1:]
		}
		return cleaned
	}

	return ""
}

// IsPublicRepo attempts to detect if the repository is public
// Currently uses heuristics; could be enhanced with GitHub API in future
func IsPublicRepo() (bool, error) {
	urls, err := GetRemoteURLs()
	if err != nil {
		return false, err
	}

	// heuristic: if any remote contains github.com and doesn't have authentication
	// this is a rough approximation; actual detection would require API calls
	for _, url := range urls {
		// if it's using git@ (SSH), it requires authentication
		if strings.Contains(url, "@") {
			continue
		}
		// HTTPS URLs to github.com/gitlab.com might be public
		if strings.Contains(url, "github.com") || strings.Contains(url, "gitlab.com") {
			// assume private by default for safety
			return false, nil
		}
	}

	return false, nil
}

// GetCurrentBranch returns the current git branch for the given directory.
// Returns empty string on any error (best-effort).
func GetCurrentBranch(dir string) string {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}
