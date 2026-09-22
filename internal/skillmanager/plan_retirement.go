package skillmanager

import (
	"os"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// retireManagedFiles decides preserve vs. remove for every previously
// managed file no longer in the desired set. Extracted verbatim from
// planWithCatalogs (ox-x1qk); oldManagedFiles stands in for old.ManagedFiles.
func retireManagedFiles(repoRoot string, targetByKey map[string]adapterprotocol.SkillTarget, oldManagedFiles []managedFile, journalFiles map[string]journalAction, desiredPaths map[string]struct{}, plan *ReconcilePlan, next *lockFile) error {
	for _, oldFile := range oldManagedFiles {
		if _, wanted := desiredPaths[oldFile.Path]; wanted {
			continue
		}
		// A team skill absent from desired state means one of two things, and the
		// difference is invisible from here: the team retired it, or ox could not
		// see the team checkout. Only the first justifies deletion. When the source
		// says it was blind, hold what we have — a stale skill is recoverable, a
		// deleted team library is not.
		if plan.teamIncomplete != "" && isTeamOwnedPath(targetByKey, oldFile) {
			plan.Preserves = append(plan.Preserves, oldFile.Path)
			next.ManagedFiles = append(next.ManagedFiles, oldFile)
			continue
		}
		actual, _, readErr := inspectRepoFile(repoRoot, oldFile.Path)
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			return readErr
		}
		actualDigest := digestBytes(actual)
		owned := actualDigest == oldFile.Digest
		if action, ok := journalFiles[oldFile.Path]; ok && (actualDigest == action.PreviousDigest || actualDigest == action.Digest) {
			owned = true
		}
		// Team Skills live in a reserved, gitignored namespace whose bytes ox
		// owns unconditionally. Apply the same contract during retirement that
		// the active-file path applies above: a local edit must not turn a
		// now-withheld executable into an unmanaged file that survives forever.
		// The incomplete-source guard above still wins, so a temporarily blind
		// Team Context never causes destructive cleanup.
		if isTeamOwnedPath(targetByKey, oldFile) {
			owned = true
		}
		if !owned {
			plan.addConflict(oldFile.Target, oldFile.Path, "retired managed file was modified and will be preserved")
			plan.Preserves = append(plan.Preserves, oldFile.Path)
			continue // relinquish ownership so uninstall/retirement can converge
		}
		plan.Removes = append(plan.Removes, FileAction{TargetKey: oldFile.Target, Path: oldFile.Path, PreviousDigest: actualDigest})
	}
	return nil
}

// sweepRetiredFiles appends removals that retireManagedFiles cannot see
// because they predate the lockfile (legacy inline stamps) or because local
// state is missing and the reserved Team Skill namespace on disk is the only
// remaining record. Extracted verbatim from planWithCatalogs (ox-x1qk).
func sweepRetiredFiles(repoRoot string, keys []string, targetByKey map[string]adapterprotocol.SkillTarget, desiredPaths map[string]struct{}, oldFiles map[string]managedFile, plan *ReconcilePlan) error {
	// Inline stamps are one-release migration evidence for retired skills that
	// predate the lockfile. Only their verified SKILL.md is attributable.
	for _, target := range targetByKey {
		retired, retiredErr := retiredLegacyFiles(repoRoot, target, desiredPaths, oldFiles)
		if retiredErr != nil {
			return retiredErr
		}
		plan.Removes = append(plan.Removes, retired...)
	}

	// Local state is disposable, so it cannot be the only inventory capable of
	// retiring a Team Skill. When Team Context discovery is authoritative, the
	// reserved namespace itself proves ownership: sweep regular files that exist
	// under sageox-team-* but are absent from desired state. If discovery is
	// incomplete, do nothing — an unseen source is not an authoritative deletion.
	if plan.teamIncomplete == "" {
		scheduled := make(map[string]struct{}, len(plan.Removes))
		for _, action := range plan.Removes {
			scheduled[action.Path] = struct{}{}
		}
		for _, key := range keys {
			orphaned, orphanErr := orphanedTeamFiles(repoRoot, targetByKey[key], desiredPaths, oldFiles, scheduled)
			if orphanErr != nil {
				return orphanErr
			}
			for _, action := range orphaned {
				scheduled[action.Path] = struct{}{}
			}
			plan.Removes = append(plan.Removes, orphaned...)
		}
	}
	return nil
}
