package skillmanager

import (
	"os"
	"path/filepath"
	"testing"
)

// TestKnownRepos_RecordsAndPrunes closes the gap that was the strongest argument
// for symlinking a central store into every repository.
//
// ox had no way to enumerate the checkouts a user has initialized: the ledger
// directories are keyed by repo id with no path mapping, and the daemon registry
// tracks only running daemons in a runtime dir that does not survive a reboot.
// With nothing to enumerate, nothing could push an update — which is precisely
// what a symlink would have made unnecessary.
//
// Failure prevented: a repository nobody opens for a week runs a week-old
// playbook, with no mechanism able to notice.
func TestKnownRepos_RecordsAndPrunes(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	alive := t.TempDir()
	gone := filepath.Join(t.TempDir(), "deleted-checkout")
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	RememberRepo(alive)
	RememberRepo(gone)

	got := KnownRepos()
	if len(got) != 2 {
		t.Fatalf("expected both repos recorded, got %v", got)
	}

	// A checkout the user deleted must not be remembered forever, or the list
	// grows without bound and every upgrade walks paths that no longer exist.
	if err := os.RemoveAll(gone); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got = KnownRepos()
	if len(got) != 1 || got[0] != alive {
		t.Errorf("a deleted checkout was not pruned: %v", got)
	}
}

// TestKnownRepos_RememberIsIdempotent: this runs at every session start, so a
// naive implementation would append a duplicate entry per session.
func TestKnownRepos_RememberIsIdempotent(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()

	for i := 0; i < 5; i++ {
		RememberRepo(root)
	}
	if got := KnownRepos(); len(got) != 1 {
		t.Errorf("repeated RememberRepo produced %d entries, want 1: %v", len(got), got)
	}
}

// TestKnownRepos_CorruptCacheIsRebuilt: this is derived, machine-local state.
// Treating a corrupt file as fatal would wedge upgrades on that machine.
func TestKnownRepos_CorruptCacheIsRebuilt(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	RememberRepo(root)

	if err := os.WriteFile(knownReposPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt cache: %v", err)
	}
	if got := KnownRepos(); len(got) != 0 {
		t.Errorf("a corrupt cache should read as empty, got %v", got)
	}
	RememberRepo(root)
	if got := KnownRepos(); len(got) != 1 {
		t.Errorf("the cache did not rebuild after corruption: %v", got)
	}
}
