package repotools

import (
	"os/exec"
	"strings"
)

// GitHubOrigin returns the owner and name of the GitHub repo the "origin"
// remote of gitRoot points at, lowercased (normalizeGitURL lowercases, and
// GitHub paths are case-insensitive). ok is false for a repo with no origin,
// or whose origin is not a plain github.com/{owner}/{name} remote; that
// includes an https remote carrying credentials, which is never matched.
// offline-safe: reads local git config only
func GitHubOrigin(gitRoot string) (owner, name string, ok bool) {
	return parseGitHubRepo(getOriginURL(gitRoot))
}

// IsShallowClone reports whether gitRoot is a shallow clone. Its first commit
// is then the shallow boundary, not the repo's root, so its salted remote
// hashes match no other clone of the same project.
func IsShallowClone(gitRoot string) bool {
	out, err := exec.Command("git", "-C", gitRoot, "rev-parse", "--is-shallow-repository").Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// GitHubUpstream is GitHubOrigin for the "upstream" remote: a contributor who
// clones their fork has the project there, not on origin.
func GitHubUpstream(gitRoot string) (owner, name string, ok bool) {
	return parseGitHubRepo(getRemoteURL(gitRoot, "upstream"))
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
