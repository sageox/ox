package main

import (
	"fmt"
	"strings"
)

// checkLegacyOxFiles is the one-time transition that takes ox's own files out of
// a customer's git history.
//
// It runs at FixLevelAuto rather than behind a confirmation prompt. A migration a
// human has to opt into is a migration that never happens: an existing project is
// initialized once and reconciled forever after, which is precisely how five
// orphaned command files survived in ox's own repository across releases with no
// release ever removing them.
//
// FixLevelAuto means human-initiated and foreground — it fires when someone runs
// `ox doctor`. It does NOT mean the daemon does this unattended; the daemon's
// reconcile check never touches the git index.
func checkLegacyOxFiles(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Legacy ox files", "not in git repo", "")
	}
	preflight := ""
	if fix {
		preflight = migrationBlocker(gitRoot)
	}
	result, _ := checkLegacyOxFilesWithPreflight(gitRoot, fix, preflight)
	return result
}

// checkLegacyOxFilesIn drives the check against an explicit repository root.
//
// The registered check resolves its root from the process working directory,
// which is untestable AND hazardous: a test that ran the cwd form once operated
// on the developer's own checkout and removed tracked files from it. Tests use
// this form; it computes the same preflight the cwd path does.
func checkLegacyOxFilesIn(gitRoot string, fix bool) checkResult {
	preflight := ""
	if fix {
		preflight = migrationBlocker(gitRoot)
	}
	result, _ := checkLegacyOxFilesWithPreflight(gitRoot, fix, preflight)
	return result
}

// checkLegacyOxFilesWithPreflight accepts the affected-path cleanliness result
// captured before Doctor runs its own skill reconcile. pending is true when a
// later check must not mutate the same committed footprint this run.
func checkLegacyOxFilesWithPreflight(gitRoot string, fix bool, preflight string) (checkResult, bool) {
	if gitRoot == "" {
		return SkippedCheck("Legacy ox files", "not in git repo", ""), false
	}
	migration, err := planLegacyMigration(gitRoot)
	if err != nil {
		return WarningCheck("Legacy ox files", "cannot inspect tracked ox files", err.Error()), true
	}
	if migration.Empty() {
		if n := len(migration.preserved); n > 0 {
			return PassedCheck("Legacy ox files", fmt.Sprintf("none tracked (%d user-edited file(s) left in place)", n)), false
		}
		return PassedCheck("Legacy ox files", "none tracked"), false
	}

	// adopt is real work too: a repository whose untrack already happened can still
	// have the committed on-ramp sitting untracked. Counting only uncache+remove
	// reported "0 ox-managed file(s) still tracked" while the check proceeded.
	summary := describeMigrationWork(migration)
	if !fix {
		return FailedCheck("Legacy ox files", summary,
			"Run `ox doctor --fix` to untrack them in one revertable commit"), true
	}

	// Guard before writing the ignore block: those files ride in the automatic
	// migration commit, so a pre-existing user edit must be detected before ox
	// changes the same working-tree path.
	if preflight != "" {
		return WarningCheck("Legacy ox files",
			fmt.Sprintf("%s; deferred because %s", summary, preflight),
			"The untrack commit will run on the next `ox doctor --fix`"), true
	}
	if reason := migrationRuntimeBlocker(gitRoot); reason != "" {
		return WarningCheck("Legacy ox files",
			fmt.Sprintf("%s; deferred because %s", summary, reason),
			"The untrack commit will run on the next `ox doctor --fix`"), true
	}

	if err := migration.Apply(); err != nil {
		return FailedCheck("Legacy ox files", summary,
			fmt.Sprintf("Untrack failed and the repository was left unchanged: %v", err)), true
	}

	detail := fmt.Sprintf("Committed as %q — revert it if you disagree", MigrationCommitSubject)
	if n := len(migration.preserved); n > 0 {
		detail += fmt.Sprintf("; %d user-edited file(s) left tracked: %s", n, strings.Join(migration.preserved, ", "))
	}
	result := PassedCheck("Legacy ox files",
		fmt.Sprintf("untracked %d file(s) in one commit", len(migration.uncache)+len(migration.remove)))
	result.detail = detail
	return result, false
}

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugLegacyOxFiles,
		Name:        "Legacy ox files",
		Category:    "Integration",
		FixLevel:    FixLevelAuto,
		Description: "Takes ox-managed skills, rules, and commands out of git tracking",
		Run:         checkLegacyOxFiles,
	})
}

// describeMigrationWork reports each kind of work separately, because they are
// not the same act: untracking removes files from git, while adopting ADDS the
// committed on-ramp to it. Collapsing them into one "still tracked" number
// reported zero for an adopt-only repository.
func describeMigrationWork(m *legacyMigration) string {
	var parts []string
	if n := len(m.uncache) + len(m.remove); n > 0 {
		parts = append(parts, fmt.Sprintf("%d ox-managed file(s) to untrack", n))
	}
	if n := len(m.adopt); n > 0 {
		parts = append(parts, fmt.Sprintf("%d committed file(s) to add", n))
	}
	if len(parts) == 0 {
		return "no ox-managed files tracked"
	}
	return strings.Join(parts, "; ")
}
