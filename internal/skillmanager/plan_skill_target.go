package skillmanager

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// planSkillTarget plans file actions for one target whose adapter declares
// the Agent Skills v1 layout: for every catalog-selected skill it decides
// create / update / preserve / conflict for each file and records ownership
// in next.ManagedFiles. Extracted verbatim from planWithCatalogs; target.Key
// stands in for the loop-local "key" the original shared with its caller.
func planSkillTarget(repoRoot string, target adapterprotocol.SkillTarget, selectedSkills []skills.Skill, oldFiles map[string]managedFile, journalFiles map[string]journalAction, desiredPaths map[string]struct{}, plan *ReconcilePlan, next *lockFile) error {
	if target.Format != adapterprotocol.SkillFormatAgentSkillsV1 {
		return fmt.Errorf("unsupported inventory target format %q", target.Format)
	}
	for _, skill := range selectedSkills {
		plan.DesiredFileCount += len(skill.Files)
		skillRoot := filepath.ToSlash(filepath.Join(target.Root, skill.Name))
		// Defense in depth. A skill name reaches here from a team-context
		// repository any teammate can push to, and it becomes a PATH:
		// filepath.Join Cleans, so `..` segments walk out of a skills root that
		// is only two segments deep, and the reserved-prefix ownership check
		// below then rubber-stamps the escape because the name still carries the
		// prefix. teamdocs.ValidTeamSkillName rejects such names at discovery;
		// this is the backstop for any future source that builds names another
		// way. Note the containment is against the TARGET root — ensureWithin's
		// other callers only bound things to repoRoot, and every payload that
		// matters (.claude/settings.json, .sageox/team-skills.approvals.json)
		// is comfortably INSIDE the repo.
		//
		// Skip the one skill rather than failing: a hostile name must not take
		// the rest of the catalog down with it, and plan.Warnings is not the
		// lever here — Apply treats any warning as "do nothing at all", so one
		// bad name would freeze every skill in the repository.
		rootPath := filepath.FromSlash(target.Root)
		skillRootPath := filepath.FromSlash(skillRoot)
		relSkill, relErr := filepath.Rel(rootPath, skillRootPath)
		if skill.Name == "" || relErr != nil || relSkill == "." || filepath.Dir(relSkill) != "." ||
			filepath.IsAbs(relSkill) || ensureWithin(rootPath, skillRootPath) != nil {
			// Logs the JOINED path, not the raw name: where the write would have
			// landed is the actionable fact, and it is the thing a responder
			// greps for after the fact.
			slog.Warn("skills: refusing skill whose name escapes its target root",
				"target", target.Key, "root", target.Root, "skill_root", skillRoot)
			continue
		}
		invalidFile := ""
		hasInvalidFile := false
		for _, file := range skill.Files {
			filePath := filepath.FromSlash(file.Path)
			joined := filepath.Join(skillRootPath, filePath)
			relFile, fileErr := filepath.Rel(skillRootPath, joined)
			if file.Path == "" || filepath.IsAbs(filePath) || fileErr != nil || relFile == "." ||
				ensureWithin(skillRootPath, joined) != nil {
				invalidFile = file.Path
				hasInvalidFile = true
				break
			}
		}
		if hasInvalidFile {
			slog.Warn("skills: refusing skill whose file escapes its skill root",
				"target", target.Key, "skill_root", skillRoot, "file", invalidFile)
			continue
		}
		migrationOwned := false
		skillPath := filepath.ToSlash(filepath.Join(skillRoot, skills.SkillFileName))
		if _, locked := oldFiles[skillPath]; !locked {
			if data, readErr := readRepoFile(repoRoot, skillPath); readErr == nil {
				migrationOwned = validLegacyStamp(data)
				if action, ok := journalFiles[skillPath]; ok {
					actualDigest := digestBytes(data)
					migrationOwned = migrationOwned || actualDigest == action.PreviousDigest || actualDigest == action.Digest
				}
				// This is the first-install reclaim, and it is the one place ox
				// takes a file on the strength of its NAME alone: it runs only when
				// the skill is absent from the lockfile, i.e. exactly when ox holds
				// no recorded claim on what is sitting there.
				//
				// A PREFIXED name is ox's by contract, so an unrecorded copy on disk
				// is reclaimed rather than preserved. This is the 0.15.0 inversion:
				// preserve-on-edit was right while these files were TRACKED (an edit
				// showed up in git diff, so it was visible and plausibly deliberate),
				// and becomes harmful once they are gitignored — a preserved edit is
				// then permanent silent drift that no teammate can see and ox can
				// never repair.
				//
				// The predicate is IsReclaimableName, never catalog membership. An
				// unprefixed name such as "post-cutoff" carries no namespace signal
				// whatsoever — it names what the skill IS, and a repository may
				// already hold a hand-authored skill at that exact path. Such names
				// earn ownership only from the three recorded claims checked directly
				// above — lockfile digest, recovery journal, or legacy stamp.
				//
				// The reclaim keys on skill.Name — ox's OWN catalog name, which always
				// satisfies the predicate — so on a case-insensitive filesystem (macOS
				// APFS by default, and Windows) it would reclaim a directory that is
				// not actually named a reserved name. A user skill at `OX-CLI-Plan` is
				// the same directory on disk as `ox-cli-plan`, but it is theirs:
				// IsReclaimableName("OX-CLI-Plan") is false, and git's
				// `skills/ox-cli-*/` ignore glob is case-sensitive, so it was never
				// even ignored. Without this check ox overwrites their SKILL.md with
				// its own, reports no conflict, and the next commit ships ox's content
				// from their tracked file. Falling through leaves migrationOwned false,
				// which routes to the conflict-and-preserve branch below.
				if !migrationOwned && IsReclaimableName(skill.Name) &&
					!caseVariantDirOnDisk(repoRoot, skillRoot, skill.Name) {
					migrationOwned = true
				}
				if !migrationOwned {
					plan.addConflict(target.Key, skillPath, "existing skill is not managed by ox")
					for _, file := range skill.Files {
						path := filepath.ToSlash(filepath.Join(skillRoot, file.Path))
						desiredPaths[path] = struct{}{}
						plan.Preserves = append(plan.Preserves, path)
					}
					continue
				}
			} else if readErr != nil && !os.IsNotExist(readErr) {
				return readErr
			}
		}
		for _, file := range skill.Files {
			path := filepath.ToSlash(filepath.Join(skillRoot, file.Path))
			desiredPaths[path] = struct{}{}
			content := file.Content
			mode := desiredFileMode(file.Path)
			want := digestBytes(content)
			oldFile, locked := oldFiles[path]
			actual, actualMode, readErr := inspectRepoFile(repoRoot, path)
			if readErr != nil && !os.IsNotExist(readErr) {
				return readErr
			}
			if os.IsNotExist(readErr) {
				plan.Creates = append(plan.Creates, FileAction{TargetKey: target.Key, Path: path, Content: content, Mode: mode, Digest: want})
				next.ManagedFiles = append(next.ManagedFiles, managedFile{Target: target.Key, Path: path, Digest: want, Mode: modeString(mode)})
				continue
			}
			actualDigest := digestBytes(actual)
			owned := locked && actualDigest == oldFile.Digest
			if action, ok := journalFiles[path]; ok && (actualDigest == action.PreviousDigest || actualDigest == action.Digest) {
				owned = true
			}
			if !owned && migrationOwned && (file.Path == skills.SkillFileName || actualDigest == want) {
				owned = true
			}
			// The last ownership arm, and the one that decides whether a local edit
			// is REPAIRED or reported. Three things earn that, and a bare catalog
			// name is not among them.
			//
			// A prefixed name earns it outright: inside a namespace ox declared,
			// ox owns the bytes unconditionally, so experimenting with one of
			// these files is fine and expected — truth is restored, nothing is
			// reported. Customizing means forking to a name of your own OUTSIDE
			// the prefixes.
			//
			// This name-only grant is safe ONLY because the skill-root and file
			// containment checks at the top of this loop have already proved path
			// stays beneath target.Root. IsReclaimableName does not inspect a path;
			// never reuse this arm before those checks or without an equivalent.
			//
			// `locked` and `migrationOwned` earn it by RECORD: this exact file has
			// a lockfile digest, or the skill verified against a legacy stamp or
			// the recovery journal above. That is what keeps an unprefixed catalog
			// skill ox genuinely installed repairable — an edit to it is drift in
			// a gitignored file, exactly the case the 0.15.0 inversion is for.
			//
			// Nothing else may reach here, and the reason is a second door into
			// the reclaim's room. The reclaim runs only when SKILL.md is readable;
			// when it is ABSENT the whole block is skipped, so a directory ox has
			// no record of arrives here with migrationOwned false and `locked`
			// false. Keying on the catalog name alone would then overwrite a
			// hand-authored sibling — post-cutoff/references/jev.md written by a
			// human, taken because the folder shares a name with something ox
			// ships. It falls to conflict-and-preserve below instead. A first
			// install is unaffected: a file that does not exist routes to Creates
			// above and never reaches this arm at all.
			if !owned && (IsReclaimableName(skill.Name) || migrationOwned || locked) {
				owned = true
			}
			if !owned {
				plan.addConflict(target.Key, path, "current digest differs from the last installed digest")
				plan.Preserves = append(plan.Preserves, path)
				if locked {
					next.ManagedFiles = append(next.ManagedFiles, oldFile)
				}
				continue
			}
			if actualDigest != want || modeDrift(actualMode, mode) {
				plan.Updates = append(plan.Updates, FileAction{TargetKey: target.Key, Path: path, Content: content, Mode: mode, PreviousDigest: actualDigest, Digest: want})
			} else {
				plan.Preserves = append(plan.Preserves, path)
			}
			next.ManagedFiles = append(next.ManagedFiles, managedFile{Target: target.Key, Path: path, Digest: want, Mode: modeString(mode)})
		}
	}
	return nil
}
