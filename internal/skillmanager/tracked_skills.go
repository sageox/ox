package skillmanager

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/gitutil"
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
	if !gitutil.IsGitRepo(repoRoot) {
		return map[string]struct{}{}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), trackedProbeTimeout)
	defer cancel()
	// -z because a path may contain anything a filesystem allows, including a
	// newline, and git quotes such paths in its default output. Splitting quoted
	// output on "\n" would silently mis-parse exactly the adversarial names this
	// package already guards elsewhere.
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
