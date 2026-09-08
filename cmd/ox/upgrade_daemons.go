package main

import (
	"log/slog"

	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/skillmanager"
)

// retireStaleDaemonsAfterUpgrade stops daemons still running the previous ox
// binary so the next invocation starts them on the new one.
//
// The obvious alternative — signaling each running daemon to reconcile now —
// is actively WRONG, and the reason is worth writing down because it is easy to
// get backwards. A daemon that is already running is executing the OLD binary,
// with the OLD embedded skill catalog compiled into it. Telling it to reconcile
// would make it write the previous release's skills into the repository,
// confidently and on a timer. The upgrade would appear to propagate while
// actually pinning every repo to the version the user just left.
//
// (The installer's downgrade guard would catch the worst of it — it refuses to
// write when the recorded version is newer — but relying on a guard to undo a
// self-inflicted wrong action is not a design.)
//
// So the propagation story is: retire the stale daemons here, and let each
// repository reconcile at its next `ox agent prime`, which runs the new binary
// and compares the catalog revision itself. Daemons respawn on demand.
//
// Best-effort throughout: a failure to stop a daemon must never fail an upgrade
// that has already succeeded.
func retireStaleDaemonsAfterUpgrade() int {
	stopped, err := daemon.KillAllDaemons()
	if err != nil {
		slog.Debug("upgrade: could not retire stale daemons", "error", err)
		return 0
	}
	if len(stopped) > 0 {
		slog.Info("upgrade: retired stale daemons", "count", len(stopped))
	}
	return len(stopped)
}

// reconcileKnownReposAfterUpgrade brings every recorded checkout in line with the
// catalog THIS binary ships.
//
// It is safe to do here and nowhere else: `ox upgrade` runs the NEW binary, so
// the catalog it projects is the new one. The same work delegated to a running
// daemon would project the OLD catalog, because that process still holds the
// previous release compiled into it.
//
// Every repository also self-heals at its next `ox agent prime`. This just means
// a repo nobody opens for a week is not a week stale.
func reconcileKnownReposAfterUpgrade() int {
	var updated int
	for _, root := range skillmanager.KnownRepos() {
		_, _, selected := skillmanager.InstalledSource(root)
		if !selected {
			continue
		}
		// Deliberately NOT short-circuited on a matching recorded revision.
		//
		// The recorded revision says what ox last WROTE, not what is on disk now. A
		// bad merge, a stray rm -rf, or a branch switch can delete the whole managed
		// tree while the state file still reports it current — and this function is
		// the only thing that visits a repository nobody opens. Skipping on the
		// recorded value is exactly the short-circuit that left real checkouts
		// unhealed until prime was taught to look past it.
		//
		// The cost is one plan per known repository per upgrade. Apply detects a
		// no-op and returns without writing, so the common case stays cheap, and an
		// upgrade is rare.
		plan, err := reconcileCommittedSkillsNonBlocking(root)
		if err != nil || plan == nil {
			slog.Debug("upgrade: could not reconcile repo", "repo", root, "error", err)
			continue
		}
		if len(plan.Creates)+len(plan.Updates)+len(plan.Removes) > 0 {
			updated++
		}
	}
	return updated
}
