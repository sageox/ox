package skillmanager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The known-repos registry is what lets `ox upgrade` reach a checkout nobody has
// opened. It is written from the session hot path, so every failure mode has to
// degrade to "do nothing" rather than to an error a session would surface.

// TestRememberRepo_CorruptRegistryIsReplacedNotFatal: a truncated or hand-edited
// registry must not permanently stop ox from recording repositories. Returning an
// error here would surface on the session hot path for a file the user never
// knew existed.
func TestRememberRepo_CorruptRegistryIsReplacedNotFatal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("HOME", home)

	path := knownReposPath()
	if path == "" {
		t.Skip("no data dir resolvable in this environment")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json at all"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	repo := t.TempDir()
	RememberRepo(repo) // must not panic

	got := KnownRepos()
	var found bool
	for _, r := range got {
		if r == repo {
			found = true
		}
	}
	if !found {
		t.Errorf("a corrupt registry permanently blocked recording; got %v", got)
	}
}

// TestRememberRepo_IsIdempotent: called at every session start, so a repeat must
// not grow the file without bound.
func TestRememberRepo_IsIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("HOME", home)

	repo := t.TempDir()
	for i := 0; i < 5; i++ {
		RememberRepo(repo)
	}

	count := 0
	for _, r := range KnownRepos() {
		if r == repo {
			count++
		}
	}
	if count != 1 {
		t.Errorf("repository recorded %d times, want 1", count)
	}
}

// TestRememberRepo_EmptyPathIsANoOp guards against recording the process cwd or
// the filesystem root when a caller could not resolve a repository.
func TestRememberRepo_EmptyPathIsANoOp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("HOME", home)

	RememberRepo("")

	if got := KnownRepos(); len(got) != 0 {
		t.Errorf("an empty repo path was recorded: %v", got)
	}
}

// TestKnownRepos_MissingRegistryIsEmptyNotAnError: the first run on a new machine
// has no registry at all.
func TestKnownRepos_MissingRegistryIsEmptyNotAnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("HOME", home)

	if got := KnownRepos(); len(got) != 0 {
		t.Errorf("a machine with no registry reported repositories: %v", got)
	}
}

// TestKnownRepos_SurvivesAWellFormedButUnexpectedShape: the file is on disk and
// a future ox may add fields. Reading one written by a different version must not
// wipe the user's list or fail.
func TestKnownRepos_SurvivesAWellFormedButUnexpectedShape(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("HOME", home)

	path := knownReposPath()
	if path == "" {
		t.Skip("no data dir resolvable in this environment")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	blob, err := json.Marshal(map[string]any{"repos": []map[string]any{}, "future_field": 7})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	repo := t.TempDir()
	RememberRepo(repo)
	if len(KnownRepos()) != 1 {
		t.Errorf("an unfamiliar but valid registry shape was not handled: %v", KnownRepos())
	}
}

// TestKnownRepos_PrunesDeletedCheckoutsButKeepsLiveOnes.
//
// The registry is what lets `ox upgrade` reach a checkout nobody has opened, so
// it accumulates entries forever unless it prunes. A deleted worktree — normal in
// a Conductor workflow, where workspaces are created and destroyed constantly —
// must drop out, and a live one must never be pruned alongside it.
func TestKnownRepos_PrunesDeletedCheckoutsButKeepsLiveOnes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("HOME", home)

	live := t.TempDir()
	gone := t.TempDir()
	RememberRepo(live)
	RememberRepo(gone)
	if got := KnownRepos(); len(got) != 2 {
		t.Fatalf("precondition: expected both repositories recorded, got %v", got)
	}

	if err := os.RemoveAll(gone); err != nil {
		t.Fatalf("remove: %v", err)
	}

	got := KnownRepos()
	if len(got) != 1 || got[0] != live {
		t.Errorf("pruning did not keep exactly the live checkout: %v", got)
	}

	// And the prune must be PERSISTED, not recomputed on every read — otherwise the
	// file grows without bound across a machine's lifetime.
	data, err := os.ReadFile(knownReposPath())
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	if strings.Contains(string(data), gone) {
		t.Errorf("the deleted checkout is still on disk after a prune:\n%s", data)
	}
}

// TestKnownRepos_AFileWhereACheckoutWasIsNotAliveEither: a path that exists but
// is not a directory is not a checkout. Treating it as one would have `ox upgrade`
// try to reconcile a regular file.
func TestKnownRepos_AFileWhereACheckoutWasIsNotAliveEither(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("HOME", home)

	repo := t.TempDir()
	RememberRepo(repo)
	if err := os.RemoveAll(repo); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.WriteFile(repo, []byte("not a checkout anymore\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := KnownRepos(); len(got) != 0 {
		t.Errorf("a regular file was reported as a live checkout: %v", got)
	}
}

// TestRememberRepo_DoesNotRewriteTheRegistryOnEverySessionStart.
//
// This runs at every `ox agent prime`. Rewriting the file each time would mean
// constant disk churn on a machine with many repositories and many sessions, for
// an entry that has not changed. It refreshes at most daily.
func TestRememberRepo_DoesNotRewriteTheRegistryOnEverySessionStart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("HOME", home)

	repo := t.TempDir()
	RememberRepo(repo)
	first, err := os.ReadFile(knownReposPath())
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	for i := 0; i < 5; i++ {
		RememberRepo(repo)
	}

	again, err := os.ReadFile(knownReposPath())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(first) != string(again) {
		t.Errorf("the registry was rewritten by a repeat call:\nfirst %s\nagain %s", first, again)
	}
}
