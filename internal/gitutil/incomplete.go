package gitutil

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// RepoState describes whether a repository has the complete commit graph
// needed for reachability queries (ahead/behind, merge-base, ancestry).
//
// A repo can be "incomplete" in two distinct ways:
//
//  1. Shallow — `git clone --depth N` creates `.git/shallow` listing the
//     commits whose parents have been truncated. Walking past them fails.
//     CI defaults (GitHub Actions actions/checkout@v5) still use this.
//
//  2. Partial — `git clone --filter=blob:none` (or tree:0) creates a
//     promisor remote that fetches objects lazily. The commit graph is
//     complete, but blob/tree reads may fault into the network mid-walk.
//     2026 CI providers (Buildkite, Depot, Blacksmith, Namespace) lean
//     toward this over shallow because it breaks fewer tools.
//
// Callers running history walks (rev-list --left-right --count,
// merge-base --is-ancestor, log --since) must treat `Incomplete()==true`
// results as opaque — render a sentinel ("—" / null), not zero counts.
// Zero would be a confident lie; the truth is "unknowable from this clone."
type RepoState struct {
	// Shallow reports whether the repo has a `.git/shallow` file
	// (resolved via `git rev-parse --git-common-dir`, so linked worktrees
	// inherit the main repo's shallow state).
	Shallow bool

	// Partial reports whether the repo has a promisor remote or
	// `extensions.partialClone` set. Object reads may require network.
	Partial bool

	// Reason is a short human-readable description suitable for UI/log
	// output, e.g. "shallow clone" or "partial clone (blob:none)".
	// Empty when neither Shallow nor Partial is true.
	Reason string
}

// Incomplete reports whether the repo lacks full history for reachability
// queries. Callers should branch on this before invoking divergence /
// ancestry git commands.
func (s RepoState) Incomplete() bool {
	return s.Shallow || s.Partial
}

// inspectTimeout caps the cost of detection. InspectRepo runs up to four
// fast git commands (rev-parse, three configs). On a healthy machine each
// returns in <50ms; the cap exists to bound pathological cases (NFS, FUSE).
const inspectTimeout = 5 * time.Second

// InspectRepo detects shallow and partial-clone state for repoPath.
//
// Worktree correctness: uses `git rev-parse --git-common-dir` so a linked
// worktree inherits its main repo's shallow status (`.git/shallow` lives
// on the common dir, not the worktree's gitdir). Conductor — ox's primary
// consumer — runs in linked worktrees, so this matters.
//
// Returns a zero RepoState (no error) when repoPath is not a git repo;
// callers can use IsGitRepo first if they need to distinguish.
func InspectRepo(repoPath string) (RepoState, error) {
	if repoPath == "" {
		return RepoState{}, errors.New("inspect repo: empty path")
	}
	if !IsGitRepo(repoPath) {
		return RepoState{}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), inspectTimeout)
	defer cancel()

	commonDir, err := runQuietGit(ctx, repoPath, "rev-parse", "--git-common-dir")
	if err != nil {
		return RepoState{}, err
	}
	// rev-parse may return a relative path; resolve against repoPath.
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(repoPath, commonDir)
	}

	state := RepoState{}

	if _, err := os.Stat(filepath.Join(commonDir, "shallow")); err == nil {
		state.Shallow = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return state, err
	}

	// Partial detection: prefer the promisor-remote signal because it
	// captures the runtime contract (network fetch may be needed). Fall
	// back to extensions.partialClone for repos that disabled the remote
	// but still have the extension recorded.
	if v, err := runQuietGit(ctx, repoPath, "config", "--get-regexp", `^remote\..*\.promisor$`); err == nil && v != "" {
		state.Partial = true
	} else if v, err := runQuietGit(ctx, repoPath, "config", "--get", "extensions.partialClone"); err == nil && v != "" {
		state.Partial = true
	}

	switch {
	case state.Shallow && state.Partial:
		state.Reason = "shallow + partial clone"
	case state.Shallow:
		state.Reason = "shallow clone"
	case state.Partial:
		state.Reason = "partial clone"
	}

	return state, nil
}

// runQuietGit is InspectRepo's helper — like RunGit but discards stderr
// from non-zero exits (`git config --get` returns exit 1 on missing keys,
// which is not an error condition for detection).
func runQuietGit(ctx context.Context, repoPath string, args ...string) (string, error) {
	cmdArgs := append([]string{"-C", repoPath}, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	cmd.Dir = repoPath
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil // discard
	err := cmd.Run()
	out := strings.TrimSpace(stdout.String())
	if err != nil {
		// `git config --get` exits 1 when the key is missing.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && len(args) >= 1 && args[0] == "config" {
			return "", nil
		}
		return out, err
	}
	return out, nil
}

// DeepenUntilAncestor attempts `git fetch --deepen <step>` in a loop until
// `git merge-base --is-ancestor <commit> <ref>` succeeds, or the cap is
// exhausted. Used by destructive ops (e.g. session redaction) that need a
// definitive ancestry answer in a shallow repo.
//
// Returns:
//   - (true, nil)  — ancestry confirmed (commit is reachable from ref).
//   - (false, nil) — ancestry definitively absent after full deepen, OR
//     not shallow and not an ancestor. Caller should treat as "no".
//   - (false, err) — fetch or merge-base error other than non-ancestor.
//
// Matches the pattern `git rebase --autosquash` has used since 2.39.
func DeepenUntilAncestor(ctx context.Context, repoPath, commit, ref string, step, maxIterations int) (bool, error) {
	if step <= 0 {
		step = 50
	}
	if maxIterations <= 0 {
		maxIterations = 4
	}

	check := func() (bool, error) {
		cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "merge-base", "--is-ancestor", commit, ref)
		err := cmd.Run()
		if err == nil {
			return true, nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// 1  = definitively not an ancestor (commits exist, no path)
			// 128 = unknown revision in current shallow view; treat as
			//       "not provably an ancestor here" so the deepen loop
			//       proceeds. If still 128 after full deepen, the commit
			//       truly doesn't exist upstream.
			code := exitErr.ExitCode()
			if code == 1 || code == 128 {
				return false, nil
			}
		}
		return false, err
	}

	if ok, err := check(); ok || err != nil {
		return ok, err
	}

	state, err := InspectRepo(repoPath)
	if err != nil || !state.Shallow {
		return false, err
	}

	for i := 0; i < maxIterations; i++ {
		if _, err := RunGit(ctx, repoPath, "fetch", "--deepen", strconv.Itoa(step)); err != nil {
			return false, err
		}
		ok, err := check()
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
		// Stop early if the deepen revealed full history.
		if state, _ := InspectRepo(repoPath); !state.Shallow {
			return false, nil
		}
	}
	return false, nil
}
