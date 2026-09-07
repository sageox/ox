package main

import (
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/skillmanager"
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

	migration, err := planLegacyMigration(gitRoot)
	if err != nil {
		return WarningCheck("Legacy ox files", "cannot inspect tracked ox files", err.Error())
	}
	if migration.Empty() {
		if n := len(migration.preserved); n > 0 {
			return PassedCheck("Legacy ox files", fmt.Sprintf("none tracked (%d user-edited file(s) left in place)", n))
		}
		return PassedCheck("Legacy ox files", "none tracked")
	}

	summary := fmt.Sprintf("%d ox-managed file(s) still tracked in git", len(migration.uncache)+len(migration.remove))
	if !fix {
		return FailedCheck("Legacy ox files", summary,
			"Run `ox doctor --fix` to untrack them in one revertable commit")
	}

	// Tier 1 — always safe, no index change: make sure the ignore rules exist
	// before anything is untracked, so the paths are ignored the moment they leave
	// the index rather than appearing as untracked noise in between.
	if _, err := ensureScopedIgnoreFiles(gitRoot); err != nil {
		return WarningCheck("Legacy ox files", summary, fmt.Sprintf("could not write ox ignore rules: %v", err))
	}

	// Tier 2 — guarded. Every blocker here is a way the single commit ox writes
	// could damage something; standing down is always recoverable.
	if reason := migrationBlocker(gitRoot); reason != "" {
		return WarningCheck("Legacy ox files",
			fmt.Sprintf("%s; deferred because %s", summary, reason),
			"Ignore rules are in place. The untrack commit will run on the next `ox doctor --fix`")
	}

	// Retire superseded stamped command files here, inside the guarded path, now
	// that every blocker has cleared.
	if _, targets, loadErr := skillmanager.LoadDesired(gitRoot); loadErr == nil {
		retireLegacyClaudeCommands(gitRoot, targets)
	}

	if err := migration.Apply(); err != nil {
		return FailedCheck("Legacy ox files", summary,
			fmt.Sprintf("Untrack failed and the repository was left unchanged: %v", err))
	}

	detail := fmt.Sprintf("Committed as %q — revert it if you disagree", MigrationCommitSubject)
	if n := len(migration.preserved); n > 0 {
		detail += fmt.Sprintf("; %d user-edited file(s) left tracked: %s", n, strings.Join(migration.preserved, ", "))
	}
	result := PassedCheck("Legacy ox files",
		fmt.Sprintf("untracked %d file(s) in one commit", len(migration.uncache)+len(migration.remove)))
	result.detail = detail
	return result
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
