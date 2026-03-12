package index

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	billy "github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// extensionSafeStorer wraps a storage.Storer and strips [extensions] from the
// in-memory config so go-git's verifyExtensions doesn't reject the repo.
// The on-disk .git/config is never modified.
type extensionSafeStorer struct {
	storage.Storer
	stripped []string
	once     sync.Once
}

func (e *extensionSafeStorer) Config() (*config.Config, error) {
	cfg, err := e.Storer.Config()
	if err != nil {
		return nil, err
	}
	if cfg.Raw != nil && cfg.Raw.HasSection("extensions") {
		e.once.Do(func() {
			for _, opt := range cfg.Raw.Section("extensions").Options {
				e.stripped = append(e.stripped, opt.Key+"="+opt.Value)
			}
		})
		cfg.Raw = cfg.Raw.RemoveSection("extensions")
		cfg.Extensions.ObjectFormat = ""
	}
	return cfg, nil
}

// plainOpenTolerant opens a git repo like git.PlainOpen but falls back to
// stripping unsupported extensions from the in-memory config when go-git
// rejects them. The on-disk .git/config is never modified.
func plainOpenTolerant(path string) (*git.Repository, error) {
	repo, err := git.PlainOpen(path)
	if err == nil {
		return repo, nil
	}
	if !errors.Is(err, git.ErrUnsupportedExtensionRepositoryFormatVersion) &&
		!errors.Is(err, git.ErrUnknownExtension) {
		return nil, err
	}

	// Replicate PlainOpenWithOptions logic with extension-safe storer.
	// dotGitToOSFilesystems is unexported, so we resolve .git ourselves.
	dot, wt, resolveErr := resolveGitDirFS(path)
	if resolveErr != nil {
		return nil, fmt.Errorf("open repo %s (extension-tolerant): %w", path, resolveErr)
	}

	s := filesystem.NewStorage(dot, cache.NewObjectLRUDefault())
	safe := &extensionSafeStorer{Storer: s}

	repo, err = git.Open(safe, wt)
	if err != nil {
		return nil, fmt.Errorf("open repo %s (extension-tolerant): %w", path, err)
	}
	slog.Warn("opened git repo bypassing unsupported extensions",
		"path", path, "stripped", safe.stripped)
	return repo, nil
}

// resolveGitDirFS replicates the essential logic of go-git's unexported
// dotGitToOSFilesystems: given a repo path, return the .git filesystem and
// the worktree filesystem. Handles normal repos (.git is a dir), bare repos
// (path itself is the git dir), and worktrees (.git is a file with gitdir pointer).
func resolveGitDirFS(path string) (dot, wt billy.Filesystem, err error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}

	gitPath := filepath.Join(absPath, ".git")
	fi, err := os.Stat(gitPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Might be a bare repo — .git dir doesn't exist but path itself is git dir
			if _, headErr := os.Stat(filepath.Join(absPath, "HEAD")); headErr == nil {
				return osfs.New(absPath), nil, nil
			}
			return nil, nil, fmt.Errorf("not a git repository: %s", absPath)
		}
		return nil, nil, err
	}

	wtFs := osfs.New(absPath)

	if fi.IsDir() {
		return osfs.New(gitPath), wtFs, nil
	}

	// .git is a file (worktree) — read gitdir pointer
	gitdir, err := readGitDirFile(gitPath, absPath)
	if err != nil {
		return nil, nil, err
	}
	return osfs.New(gitdir), wtFs, nil
}

// readGitDirFile reads a .git file that contains a gitdir pointer.
func readGitDirFile(gitFilePath, repoPath string) (string, error) {
	f, err := os.Open(gitFilePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	b, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}

	line := string(b)
	const prefix = "gitdir: "
	if !strings.HasPrefix(line, prefix) {
		return "", fmt.Errorf(".git file has no %s prefix", prefix)
	}

	gitdir := strings.Split(line[len(prefix):], "\n")[0]
	gitdir = strings.TrimSpace(gitdir)
	if filepath.IsAbs(gitdir) {
		return gitdir, nil
	}
	return filepath.Join(repoPath, gitdir), nil
}
