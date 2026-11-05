package repotools

import "strings"

// GitHubOrigin returns the owner and name of the GitHub repo the "origin"
// remote of gitRoot points at, lowercased (normalizeGitURL lowercases, and
// GitHub paths are case-insensitive). ok is false for a repo with no origin,
// or whose origin is not a plain github.com/{owner}/{name} remote; that
// includes an https remote carrying credentials, which is never matched.
// offline-safe: reads local git config only
func GitHubOrigin(gitRoot string) (owner, name string, ok bool) {
	return parseGitHubRepo(getOriginURL(gitRoot))
}

// parseGitHubRepo splits a normalized remote ("github.com/owner/name") into
// its owner and name.
func parseGitHubRepo(normalized string) (owner, name string, ok bool) {
	rest, found := strings.CutPrefix(normalized, "github.com/")
	if !found {
		return "", "", false
	}
	parts := strings.Split(strings.TrimSuffix(rest, "/"), "/")
	if len(parts) != 2 {
		return "", "", false
	}
	// normalizeGitURL strips ".git" before it trims a trailing slash or
	// lowercases, so "repo.git/" and "Repo.GIT" arrive as "repo.git". GitHub
	// reserves the suffix (no repo name ends in .git), so dropping it here is safe.
	owner, name = parts[0], strings.TrimSuffix(parts[1], ".git")
	if owner == "" || name == "" {
		return "", "", false
	}
	return owner, name, true
}
