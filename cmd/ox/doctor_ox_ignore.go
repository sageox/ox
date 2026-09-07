package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// checkOxIgnoreRules keeps the ox-managed ignore block present and effective.
//
// It verifies with `git check-ignore` rather than by searching the file for a
// pattern, because only git knows the answer: a later negation, a nested ignore
// file, or a different pattern spelling all change the outcome, and a string
// search sees none of them.
func checkOxIgnoreRules(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("ox ignore rules", "not in git repo", "")
	}

	// A representative reserved path per agent directory that ox actually uses.
	probes := map[string]string{
		".claude":  ".claude/skills/ox-cli-probe/SKILL.md",
		".agents":  ".agents/skills/ox-cli-probe/SKILL.md",
		".factory": ".factory/rules/ox-cli.md",
	}

	var broken []string
	for _, f := range scopedIgnoreFiles() {
		probe, ok := probes[f.dir]
		if !ok {
			continue
		}
		if !dirExists(filepath.Join(gitRoot, f.dir)) {
			continue
		}
		if !gitPathIsIgnored(gitRoot, probe) {
			broken = append(broken, f.dir)
		}
	}
	if len(broken) == 0 {
		return PassedCheck("ox ignore rules", "ox-managed files are ignored")
	}

	summary := fmt.Sprintf("ox files are not ignored in %s", strings.Join(broken, ", "))
	if !fix {
		return FailedCheck("ox ignore rules", summary, "Run `ox doctor --fix` to restore the ox-managed ignore block")
	}
	if _, err := ensureScopedIgnoreFiles(gitRoot); err != nil {
		return FailedCheck("ox ignore rules", summary, fmt.Sprintf("Fix failed: %v", err))
	}

	var stillBroken []string
	for _, dir := range broken {
		if !gitPathIsIgnored(gitRoot, probes[dir]) {
			stillBroken = append(stillBroken, dir)
		}
	}
	if len(stillBroken) > 0 {
		// The block is present but something else overrides it — most often a
		// negation the user wrote. ox reports that; it does not fight the user's
		// own rules.
		return WarningCheck("ox ignore rules",
			fmt.Sprintf("ox files are still not ignored in %s", strings.Join(stillBroken, ", ")),
			"A rule in your own .gitignore may be overriding the ox-managed block; ox will not edit your rules")
	}
	return PassedCheck("ox ignore rules", "restored the ox-managed ignore block")
}

// checkOxFilesNotTracked reports reserved-prefix paths that are in the index.
//
// Reported, never auto-fixed: removing a path from the index is a commit-shaped
// act, and the one commit ox is willing to write is the guarded migration.
func checkOxFilesNotTracked(fix bool) checkResult {
	_ = fix
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("ox files untracked", "not in git repo", "")
	}
	migration, err := planLegacyMigration(gitRoot)
	if err != nil {
		return WarningCheck("ox files untracked", "cannot inspect tracked ox files", err.Error())
	}
	n := len(migration.uncache) + len(migration.remove)
	if n == 0 {
		return PassedCheck("ox files untracked", "no ox-managed files are tracked")
	}
	return WarningCheck("ox files untracked",
		fmt.Sprintf("%d ox-managed file(s) are tracked in git", n),
		"Run `ox doctor --fix` — the `Legacy ox files` check untracks them in one revertable commit")
}

func gitPathIsIgnored(repoRoot, rel string) bool {
	// --no-index matters: without it git reports a TRACKED path as not-ignored no
	// matter what the rules say, so during the migration window — exactly when this
	// check is most useful — it would report the rules broken.
	cmd := exec.Command("git", "check-ignore", "--no-index", "-q", "--", rel)
	cmd.Dir = repoRoot
	return cmd.Run() == nil
}

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugOxIgnoreRules,
		Name:        "ox ignore rules",
		Category:    "Integration",
		FixLevel:    FixLevelAuto,
		Description: "Keeps ox-managed skills and rules out of git",
		Run:         checkOxIgnoreRules,
	})
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugOxFilesUntracked,
		Name:        "ox files untracked",
		Category:    "Integration",
		FixLevel:    FixLevelCheckOnly,
		Description: "Reports ox-managed files still tracked in git",
		Run:         checkOxFilesNotTracked,
	})
}
