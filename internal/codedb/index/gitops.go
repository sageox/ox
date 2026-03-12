package index

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
)

// normalizeRepoURL strips scheme prefixes, trailing slashes, and .git suffix
// from a repo URL, returning the bare host/path form.
// Returns an error if the result is empty.
func normalizeRepoURL(url string) (string, error) {
	stripped := url
	for _, prefix := range []string{"https://", "http://", "git://"} {
		if after, found := strings.CutPrefix(stripped, prefix); found {
			stripped = after
			break
		}
	}
	stripped = strings.TrimRight(stripped, "/")
	stripped = strings.TrimSuffix(stripped, ".git")
	if stripped == "" {
		return "", fmt.Errorf("invalid repo URL: %s", url)
	}
	return stripped, nil
}

// RepoDirFromURL derives a local directory name from a repo URL.
// "https://github.com/user/repo/" -> "github.com/user/repo.git"
func RepoDirFromURL(url string) (string, error) {
	normalized, err := normalizeRepoURL(url)
	if err != nil {
		return "", err
	}
	return normalized + ".git", nil
}

// RepoNameFromURL derives a human-readable name from a URL.
// "https://github.com/user/repo" -> "github.com/user/repo"
func RepoNameFromURL(url string) (string, error) {
	return normalizeRepoURL(url)
}

// CloneOrFetch clones a bare repo at repoPath, or fetches if it already exists.
// Returns the opened go-git Repository.
func CloneOrFetch(url, repoPath string) (*git.Repository, error) {
	if _, err := os.Stat(repoPath); err == nil {
		repo, err := plainOpenTolerant(repoPath)
		if err != nil {
			return nil, fmt.Errorf("open existing repo: %w", err)
		}
		// Non-fatal: fetch may fail if remote is unreachable
		// but we can still use the local clone.
		_ = fetch(repo)
		return repo, nil
	}
	return cloneBare(url, repoPath)
}

func cloneBare(url, path string) (*git.Repository, error) {
	repo, err := git.PlainClone(path, true, &git.CloneOptions{
		URL: url,
	})
	if err != nil {
		return nil, fmt.Errorf("clone %s: %w", url, err)
	}
	return repo, nil
}

func fetch(repo *git.Repository) error {
	err := repo.Fetch(&git.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{"+refs/*:refs/*"},
		Force:      true,
	})
	if errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}
	return err
}
