package nativeimport

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/session/claudesource"
)

// Membership says whether a session's working directories belong to the repo.
type Membership int

const (
	OtherRepo  Membership = iota // no directory is in this repo
	ThisRepo                     // every directory is in this repo
	MixedRepos                   // some are, some are not: never imported
)

// dirVerdict places one working directory relative to this repository.
type dirVerdict int

const (
	dirOther   dirVerdict = iota // another project's, or cannot be shown not to be
	dirMine                      // inside one of this repository's checkouts
	dirNeutral                   // a folder outside every project: no one's work
)

// Scope decides whether working directories belong to one repository: the one
// whose git common dir they resolve to. That covers every worktree, including
// worktrees that have since been deleted.
type Scope struct {
	commonDir string   // this repo's git common dir, resolved
	roots     []string // main worktree and every registered worktree, resolved
	cache     map[string]dirVerdict
}

// NewScope resolves repoRoot's git common dir and its worktrees.
func NewScope(repoRoot string) (*Scope, error) {
	out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return nil, fmt.Errorf("resolve git common dir: %w", err)
	}
	common := resolvePath(strings.TrimSpace(string(out)))
	s := &Scope{commonDir: common, cache: map[string]dirVerdict{}}
	if filepath.Base(common) == ".git" {
		s.roots = append(s.roots, filepath.Dir(common))
	}
	// Registered worktrees, including ones deleted without `git worktree remove`.
	entries, _ := os.ReadDir(filepath.Join(common, "worktrees"))
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(common, "worktrees", entry.Name(), "gitdir"))
		if err != nil {
			continue
		}
		if dotGit := strings.TrimSpace(string(data)); dotGit != "" {
			s.roots = append(s.roots, resolvePath(filepath.Dir(dotGit)))
		}
	}
	return s, nil
}

// Classify reports whether every directory belongs to this repository. A
// folder outside every project, such as the notes folder the Claude desktop
// app shares between sessions, counts for neither side: it is no one's work.
func (s *Scope) Classify(cwds []string) Membership {
	mine, other := 0, 0
	for _, dir := range cwds {
		switch s.place(dir) {
		case dirMine:
			mine++
		case dirOther:
			other++
		}
	}
	switch {
	case mine > 0 && other == 0:
		return ThisRepo
	case mine > 0:
		return MixedRepos
	default:
		return OtherRepo
	}
}

func (s *Scope) place(dir string) dirVerdict {
	if v, ok := s.cache[dir]; ok {
		return v
	}
	v := dirOther
	switch {
	case s.resolve(dir):
		v = dirMine
	// a cwd with ".." is cleaned before its links are resolved, which can land
	// somewhere other than the folder it names, so only a canonical one counts
	case filepath.IsAbs(dir) && filepath.Clean(dir) == dir && claudesource.OutsideEveryProject(resolvePath(dir), s.roots...):
		v = dirNeutral
	}
	s.cache[dir] = v
	return v
}

// resolve asks the directory itself when it still exists, because a nested
// repository under this one's tree is not this repository. A directory that
// no longer exists belongs here only if it was under one of our worktrees.
func (s *Scope) resolve(dir string) bool {
	if !filepath.IsAbs(dir) {
		return false
	}
	if _, err := os.Stat(dir); err == nil {
		common, ok := commonDirFor(dir)
		return ok && common == s.commonDir
	}
	clean := resolvePath(dir)
	for _, root := range s.roots {
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// commonDirFor walks up from dir to the nearest .git and returns its common
// dir, without running git: a main checkout's .git directory is its own common
// dir, and a worktree's .git file points at a gitdir whose commondir leads back.
func commonDirFor(dir string) (string, bool) {
	for current := resolvePath(dir); ; current = filepath.Dir(current) {
		dotGit := filepath.Join(current, ".git")
		info, err := os.Stat(dotGit)
		if err == nil {
			if info.IsDir() {
				return resolvePath(dotGit), true
			}
			return commonDirFromFile(dotGit, current)
		}
		if parent := filepath.Dir(current); parent == current {
			return "", false
		}
	}
}

func commonDirFromFile(dotGit, base string) (string, bool) {
	f, err := os.Open(dotGit)
	if err != nil {
		return "", false
	}
	defer f.Close()
	line, _ := bufio.NewReader(f).ReadString('\n')
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
	if !ok {
		return "", false
	}
	gitdir = strings.TrimSpace(gitdir)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(base, gitdir)
	}
	data, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		return resolvePath(gitdir), true // a submodule-style gitdir is its own common dir
	}
	common := strings.TrimSpace(string(data))
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitdir, common)
	}
	return resolvePath(common), true
}

func resolvePath(p string) string {
	p = filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	// A deleted directory cannot be resolved; resolve its nearest existing
	// ancestor so /tmp vs /private/tmp style aliases still compare equal.
	parent, rest := filepath.Dir(p), filepath.Base(p)
	if parent == p {
		return p
	}
	return filepath.Join(resolvePath(parent), rest)
}
