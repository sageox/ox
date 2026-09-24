package skillmanager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// trackedProbeTimeout bounds the one git call this file makes. It is generous
// because a cold index on a large repository is slow, and short enough that a
// wedged git never hangs `ox doctor`.
const trackedProbeTimeout = 10 * time.Second

// trackedSkillDirs returns the skill directories git already tracks, keyed by
// their repo-relative slash path (".claude/skills/notify-team").
//
// Why this exists: a Team Skill now installs under its own name plus a "-team"
// suffix, so the directory name no longer proves ox wrote it. "-team" is ordinary
// English, and someone may well have hand-authored `notify-team` before ox ever
// ran here. Ox needs a signal that a HUMAN put a directory there deliberately, and
// "git tracks this path" is the strongest one available: ox's own projections are
// gitignored and never staged, so a tracked skill directory is by construction
// somebody's committed work.
//
// It is one `git ls-files` for every skill root rather than one call per skill:
// the per-skill shape was measured into existence once already in the Team Rule
// projector, and a repository with thirty skills would pay thirty process spawns
// on every prime.
//
// A directory that is not a git repository yields an empty set and no error —
// nothing can be tracked there. Every other failure IS returned, so a plan is
// never built on a guess: treating "git broke" as "nothing is tracked" is exactly
// how a reconcile overwrites committed work and reports success.
func trackedSkillDirs(repoRoot string, targets []adapterprotocol.SkillTarget) (map[string]struct{}, error) {
	roots := make([]string, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if target.Format != adapterprotocol.SkillFormatAgentSkillsV1 || target.Root == "" {
			continue
		}
		root := filepath.ToSlash(target.Root)
		if _, dup := seen[root]; dup {
			continue
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	if len(roots) == 0 {
		return map[string]struct{}{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), trackedProbeTimeout)
	defer cancel()

	// Ask git where it is, rather than looking for a .git entry at repoRoot.
	//
	// A stat-based check answers "is repoRoot itself a repository root", and that
	// is the wrong question: a project root nested BELOW the work-tree root has no
	// .git of its own, so the check says "not a repository", the tracked set comes
	// back empty, and reconciliation overwrites committed skills believing nothing
	// is tracked. That is the precise fail-open this whole probe exists to prevent,
	// reintroduced by the gate in front of it.
	inside, err := insideWorkTree(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	if !inside {
		return map[string]struct{}{}, nil
	}
	// -z because a path may contain anything a filesystem allows, including a
	// newline, and git quotes such paths in its default output. Splitting quoted
	// output on "\n" would silently mis-parse exactly the adversarial names this
	// package already guards elsewhere.
	// Pathspecs and output are both relative to cmd.Dir, so a nested repoRoot
	// yields exactly the repo-root-relative keys this function promises.
	args := append([]string{"ls-files", "-z", "--"}, roots...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}

	dirs := map[string]struct{}{}
	for _, entry := range strings.Split(string(out), "\x00") {
		if entry == "" {
			continue
		}
		// <root>/<skill>/<file…> — the skill directory is the first two segments
		// past the root. A tracked file directly inside a skills root (no skill
		// directory of its own) belongs to nobody's skill and is ignored here.
		for _, root := range roots {
			prefix := root + "/"
			if !strings.HasPrefix(entry, prefix) {
				continue
			}
			rest := strings.TrimPrefix(entry, prefix)
			name, _, found := strings.Cut(rest, "/")
			if !found || name == "" {
				continue
			}
			dirs[prefix+name] = struct{}{}
			break
		}
	}
	return dirs, nil
}

// insideWorkTree reports whether dir sits inside a git work tree.
//
// "Not a repository" is an ANSWER — a plain directory has nothing tracked, and
// failing the plan there would break `ox` for anyone running it outside git for no
// safety gained. Every other failure is an ERROR, because "git broke" and "nothing
// is tracked" must never collapse into the same result: that collapse is how a
// reconcile deletes committed work and reports success.
//
// The two are told apart by git's own message rather than by exit status alone,
// since exit 128 also covers a corrupt index and an unreadable object store —
// states where treating the repository as empty would be actively dangerous.
func insideWorkTree(ctx context.Context, dir string) (bool, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = dir
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(out)) == "true", nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, fmt.Errorf("locate the git work tree for %s: %w", dir, ctxErr)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && strings.Contains(stderr.String(), "not a git repository") {
		return false, nil
	}
	return false, fmt.Errorf("locate the git work tree for %s: %w", dir, err)
}
