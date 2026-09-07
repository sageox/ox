package main

import (
	"errors"
	"log/slog"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/version"
)

// reconcileSkillInventoryIfStale brings a project's managed skill files back in
// line with the catalog compiled into the running binary, and does nothing at
// all when they already match.
//
// This is the mechanism that makes locally-materialized, gitignored skills
// correct without a shared on-disk store. Staleness in a skill file is only ever
// observable at the moment an agent READS it, which is session start, which
// already runs `ox agent prime`. Reconciling here collapses the observable
// staleness window to zero for any agent whose SessionStart hook completes
// before it scans its skill directory.
//
// Cost discipline matters because this runs on every session start:
//   - the healthy path is one lockfile read plus two string comparisons;
//   - skills.Digest() is memoized per process;
//   - a full plan (which reads and digests every managed file across every
//     target) is built only after a mismatch is already proven.
//
// Best-effort by construction. Every failure path returns without disturbing the
// session: a project that cannot reconcile still primes, because a stale
// playbook is a degraded session while a failed prime is no session at all.
func reconcileSkillInventoryIfStale(projectRoot string) (changed int) {
	if projectRoot == "" {
		return 0
	}
	revision, oxVersion, selected := skillmanager.InstalledSource(projectRoot)
	if selected {
		// Record this checkout so `ox upgrade` can reconcile it later without
		// waiting for someone to open a session here. Cheap and best-effort.
		skillmanager.RememberRepo(projectRoot)
	}
	if !selected {
		// No targets recorded: this project has never selected an AI coworker with
		// native skills, or has never been initialized. `ox init` owns first
		// install; prime must not silently install into a repo that never asked.
		return 0
	}
	// SELF-HEAL, before the staleness compare.
	//
	// The compare short-circuits when the recorded revision matches, so a
	// repository whose skills are already current never reaches Apply — and Apply
	// is where the ignore rule gets written. A repository materialized by a build
	// that predates that invariant is therefore stuck: reserved-prefix files on
	// disk, no rule hiding them, and nothing on the session path that would ever
	// notice. That state has been observed in real checkouts, one `git add -A`
	// away from committing the vendor rename.
	//
	// Existence-gated, so it never creates an agent directory the project does not
	// already use, and idempotent, so the common case is three Lstats and no write.
	if _, err := skillmanager.EnsureScopedIgnoreFiles(projectRoot); err != nil {
		slog.Debug("skills: could not write ox ignore rules at prime", "error", err)
	}

	wantRevision, err := skills.Digest()
	if err != nil {
		slog.Debug("skills: catalog digest unavailable at prime", "error", err)
		return 0
	}
	if revision == wantRevision && oxVersion == version.Version {
		return 0 // the common path
	}

	plan, err := reconcileCommittedSkillsNonBlocking(projectRoot)
	if err != nil {
		if errors.Is(err, skillmanager.ErrApplyInProgress) {
			// Another ox process holds the apply lock and is doing this exact work.
			// Skipping is the correct outcome, not a degraded one.
			slog.Debug("skills: reconcile already in progress, skipping at prime")
			return 0
		}
		slog.Debug("skills: prime reconcile failed", "error", err)
		return 0
	}
	if plan == nil {
		return 0
	}
	changed = len(plan.Creates) + len(plan.Updates) + len(plan.Removes)
	if changed > 0 {
		slog.Info("skills: reconciled inventory at prime",
			"changed", changed, "revision", wantRevision, "version", version.Version)
	}
	return changed
}
