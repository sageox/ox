package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/skillmanager"
)

const CheckSlugTeamSuffixShadow = "team-suffix-shadow"

// checkTeamSuffixShadow finds hand-authored skills whose name collides with the
// Team Context namespace, and therefore stopped reaching git.
//
// The hazard is created by the namespace itself and cannot be designed away. Team
// Skills are hidden from a customer's pull requests by a stable glob —
// `skills/*-team/` — and a stable glob is what keeps the committed .gitignore from
// churning every time a team adds a skill. But a glob cannot tell ox's
// `fork-scout-team` from somebody's own `notify-team`, so a skill that happens to
// end in "-team" is silently untracked from the moment ox writes the ignore rule.
//
// Silently is the word that matters. The file is still on disk and still works
// locally, so nothing looks wrong — it simply never reaches a teammate, and the
// author finds out when somebody asks why the skill they wrote is missing.
//
// It is a WARNING, never a fix. Both remedies belong to the human: rename the
// skill, or force it into git. Ox renaming somebody's skill directory, or staging
// a file into their index, would each be worse than the problem.
func checkTeamSuffixShadow(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("team-namespace collisions", "not in git repo", "")
	}
	return checkTeamSuffixShadowIn(gitRoot, fix)
}

// checkTeamSuffixShadowIn is the check with its repository root passed in, matching
// checkOxIgnoreRulesIn: it keeps the tests off os.Chdir, which is process-global
// and therefore incompatible with t.Parallel.
func checkTeamSuffixShadowIn(gitRoot string, _ bool) checkResult {
	roots, err := skillTargetRoots(gitRoot)
	if err != nil || len(roots) == 0 {
		return SkippedCheck("team-namespace collisions", "no skills directory selected", "")
	}

	var shadowed []string
	for _, root := range roots {
		entries, readErr := os.ReadDir(filepath.Join(gitRoot, filepath.FromSlash(root)))
		if readErr != nil {
			continue // an unreadable root is the AI-coworker-assets check's business
		}
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasSuffix(entry.Name(), skillmanager.TeamSuffix) {
				continue
			}
			manifest, manifestErr := os.ReadFile(
				filepath.Join(gitRoot, filepath.FromSlash(root), entry.Name(), skills.SkillFileName))
			if manifestErr != nil {
				continue // no readable manifest means no skill to report
			}
			if skillmanager.TeamSkillStamp.Verifies(manifest) {
				continue // ox's own projection, correctly hidden
			}
			// ASK git, do not infer. The name and the ignore block together look
			// like enough, and they are not: after the author follows this check's
			// own advice and runs `git add -f`, the file is tracked and no longer
			// ignored — but a name-based check would keep warning about it forever,
			// which teaches people to ignore the checker. The same inference also
			// fires in a root that has no ignore block at all.
			//
			// Deliberately WITHOUT --no-index, the opposite of gitPathIsIgnored:
			// there the question is "do the rules cover this path" during a
			// migration that has not untracked it yet, here it is "is this file
			// actually missing from git right now", and a tracked path must answer
			// no.
			rel := root + "/" + entry.Name() + "/" + skills.SkillFileName
			if !gitPathIsIgnoredRespectingIndex(gitRoot, rel) {
				continue
			}
			// Sanitized here, not at render time. A skill directory name is
			// repository-controlled and this string goes straight to a terminal, so
			// an embedded escape could hide the rest of the diagnostic — the same
			// hazard sanitizeCell exists for in `ox skills list`.
			shadowed = append(shadowed, sanitizeCell(root+"/"+entry.Name()))
		}
	}
	if len(shadowed) == 0 {
		return PassedCheck("team-namespace collisions", "no hand-authored skill is hidden by the team namespace")
	}
	sort.Strings(shadowed)
	return WarningCheck("team-namespace collisions",
		fmt.Sprintf("git ignores %d hand-authored skill(s) whose name ends in %q: %s",
			len(shadowed), skillmanager.TeamSuffix, strings.Join(shadowed, ", ")),
		fmt.Sprintf("Rename each one so it does not end in %q, or run `git add -f <path>` to commit it deliberately",
			skillmanager.TeamSuffix))
}

// gitPathIsIgnoredRespectingIndex reports whether git currently ignores rel.
//
// A tracked path answers false however the ignore rules read, which is the whole
// point here and the reason this cannot share gitPathIsIgnored's --no-index.
func gitPathIsIgnoredRespectingIndex(repoRoot, rel string) bool {
	cmd := exec.Command("git", "check-ignore", "-q", "--", rel)
	cmd.Dir = repoRoot
	return cmd.Run() == nil
}

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugTeamSuffixShadow,
		Name:        "team-namespace collisions",
		Category:    "Integration",
		FixLevel:    FixLevelCheckOnly,
		Description: "Warns when a hand-authored skill's name is hidden by the Team Context namespace",
		Run:         checkTeamSuffixShadow,
	})
}
