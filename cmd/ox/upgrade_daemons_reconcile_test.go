//go:build !short

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/skillmanager"
)

// TestReconcileKnownReposAfterUpgrade_OnlyTouchesRepositoriesThatSelectedOx.
//
// This runs from `ox upgrade`, which is the ONE process holding the new catalog
// compiled in — a daemon delegated the same work would project the OLD one. That
// makes its blast radius worth pinning: it must skip any repository that never
// selected ox, or an upgrade would install vendor files into every checkout the
// user happens to have opened.
func TestReconcileKnownReposAfterUpgrade_OnlyTouchesRepositoriesThatSelectedOx(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("HOME", home)

	unselected := migrationRepo(t)
	// Remove any recorded selection so the repo looks like one ox never initialized.
	_ = os.Remove(skillmanager.LockPath(unselected))
	_ = os.Remove(skillmanager.StatePath(unselected))
	skillmanager.RememberRepo(unselected)

	updated := reconcileKnownReposAfterUpgrade()

	if updated != 0 {
		t.Errorf("upgrade reconciled %d unselected repositories", updated)
	}
	for _, dir := range []string{".agents", ".factory"} {
		if _, err := os.Stat(filepath.Join(unselected, dir)); err == nil {
			t.Errorf("upgrade created %s/ in a repository that never selected ox", dir)
		}
	}
}

// TestReconcileKnownReposAfterUpgrade_RestoresDriftedSkills is the point of the
// function: a repository nobody opens should not stay broken. Every repo also
// self-heals at its next `ox agent prime`; this closes the gap for the ones
// nobody primes.
func TestReconcileKnownReposAfterUpgrade_RestoresDriftedSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("HOME", home)

	root := migrationRepo(t)
	if _, err := reconcileCommittedSkills(root); err != nil {
		t.Fatalf("seed selection: %v", err)
	}
	onramp := filepath.Join(root, ".claude", "skills", skillmanager.CommittedOnRamp, "SKILL.md")
	if _, err := os.Stat(onramp); err != nil {
		t.Fatalf("precondition: the on-ramp should be installed: %v", err)
	}

	// Drift: something removed the managed tree (a bad merge, a stray rm -rf).
	if err := os.RemoveAll(filepath.Join(root, ".claude", "skills")); err != nil {
		t.Fatalf("simulate drift: %v", err)
	}
	skillmanager.RememberRepo(root)

	if updated := reconcileKnownReposAfterUpgrade(); updated == 0 {
		t.Error("upgrade reported no work for a repository whose skills had been deleted")
	}
	if _, err := os.Stat(onramp); err != nil {
		t.Errorf("upgrade did not restore the drifted skill tree: %v", err)
	}
}

// TestRetireStaleDaemonsAfterUpgrade_NeverFailsAnUpgradeThatSucceeded: the
// binary on disk has already been replaced by the time this runs. Returning an
// error — or panicking when no daemon is running — would report a successful
// upgrade as a failure.
func TestRetireStaleDaemonsAfterUpgrade_NeverFailsAnUpgradeThatSucceeded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("HOME", home)

	if got := retireStaleDaemonsAfterUpgrade(); got < 0 {
		t.Errorf("negative daemon count %d", got)
	}
}
