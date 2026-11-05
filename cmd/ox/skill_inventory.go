package main

import (
	"fmt"
	"log/slog"

	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/version"
)

// reconcileSkillInventoryIfStale brings a project's managed skill files back in
// line with the catalog and current project approvals, and does nothing at all
// when they already match.
//
// This is the mechanism that makes locally-materialized, gitignored skills
// correct without a shared on-disk store. Staleness in a skill file is only ever
// observable at the moment an agent READS it, which is session start, which
// already runs `ox agent prime`. Reconciling here collapses the observable
// staleness window to zero for any agent whose SessionStart hook completes
// before it scans its skill directory.
//
// Cost discipline matters because this runs on every session start:
//   - the healthy path reads inventory state and approvals, then compares revisions;
//   - skills.Digest() is memoized per process;
//   - a full plan (which reads and digests every managed file across every
//     target) is built only after a mismatch is already proven.
//
// Fail closed if policy cannot be read or a stale inventory cannot be reconciled:
// continuing could expose executable content whose approval has been revoked.
// It also returns the team skills the reconcile DECLINED to materialize, so
// prime can say so once. A withheld skill leaves the repository looking exactly
// as it would if nobody had authored it, and the team member who wrote it has no
// way to discover that from their own machine.
//
// Reported on CHANGE, not every session: the withheld set can only shift when
// the catalog revision does, and the fast path proves that has not happened
// without building a plan. Re-emitting a line into every prime forever would
// spend context on an unchanged fact — the cost this whole path exists to avoid.
func reconcileSkillInventoryIfStale(projectRoot string) (changed int, withheld []skillmanager.TeamSkillDecision, err error) {
	if projectRoot == "" {
		return 0, nil, nil
	}
	revision, oxVersion, selected := skillmanager.InstalledSource(projectRoot)
	if selected {
		// Record this checkout so `ox upgrade` can reconcile it later without
		// waiting for someone to open a session here. Cheap and best-effort.
		skillmanager.RememberRepo(projectRoot)
	}
	if !selected {
		// A damaged lockfile also reads as "nothing selected", yet the files it
		// managed may include runnable content whose approval was revoked.
		if err := skillmanager.CheckInstalledState(projectRoot); err != nil {
			return 0, nil, fmt.Errorf("verify skill inventory state (run `ox doctor` to repair): %w", err)
		}
		// No targets recorded: this project has never selected an AI coworker with
		// native skills, or has never been initialized. `ox init` owns first
		// install; prime must not silently install into a repo that never asked.
		return 0, nil, nil
	}
	// Must be the SAME value the planner records, hence one shared function.
	// Comparing against the built-in catalog digest alone was the bug: once a
	// team context existed the planner recorded a team component too, the two
	// could never be equal, and this fast path ran a full plan on every single
	// session start in every repo with a team.
	wantRevision, err := skillmanager.ExpectedRevision(projectRoot)
	if err != nil {
		return 0, nil, fmt.Errorf("verify skill inventory policy: %w", err)
	}
	if revision == wantRevision && oxVersion == version.Version {
		return 0, nil, nil // the common path
	}

	plan, err := reconcileCommittedSkillsNonBlocking(projectRoot)
	if err != nil {
		// Lock contention is not proof that the other process has removed revoked
		// content yet. The caller must retry rather than start with stale policy.
		return 0, nil, fmt.Errorf("reconcile skill inventory before prime: %w", err)
	}
	if plan == nil {
		return 0, nil, fmt.Errorf("skill inventory reconciliation returned no plan")
	}
	if len(plan.Warnings) > 0 {
		// Apply refuses a plan with warnings (a newer ox owns this install, or the
		// lockfile schema is newer) and writes nothing, so no revision is recorded
		// and this path runs again next session. The planner still carries the team
		// decisions on that plan, but none of them was acted on: reporting them
		// would repeat the same line at every session start, and would blame a
		// held-back skill for what is really a binary too old to touch the repo.
		return 0, nil, fmt.Errorf("skill inventory reconciliation refused: %v", plan.Warnings)
	}
	if installed, _, _ := skillmanager.InstalledSource(projectRoot); installed != wantRevision {
		return 0, nil, fmt.Errorf("skill inventory policy is not reconciled; run `ox doctor` and retry `ox agent prime`")
	}
	changed = len(plan.Creates) + len(plan.Updates) + len(plan.Removes)
	withheld = plan.WithheldTeamSkills()
	if changed > 0 || len(withheld) > 0 {
		slog.Info("skills: reconciled inventory at prime",
			"changed", changed, "withheld", len(withheld), "revision", wantRevision, "version", version.Version)
	}
	return changed, withheld, nil
}
