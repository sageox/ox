package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestInitTracker_RollbackNeverDeletesAPreExistingIgnoreFile is the distinction
// that decides whether a failed `ox init` destroys the user's rules.
//
// A .gitignore ox CREATED must be removed on rollback. One that ALREADY EXISTED
// must be restored to its previous bytes — never deleted. Calling
// trackCreatedFile on a file the user already had would make rollback delete
// every rule they wrote, from a command that was only supposed to add a block.
//
// The snapshot ordering is the other half: trackModifiedFile captures bytes at
// call time, so snapshotting AFTER the write would record the already-modified
// content and "restore" ox's block into the user's file permanently.
func TestInitTracker_RollbackNeverDeletesAPreExistingIgnoreFile(t *testing.T) {
	repo := t.TempDir()
	claude := filepath.Join(repo, ".claude")
	require.NoError(t, os.MkdirAll(claude, 0o755))

	userRules := "# my own rules\nsettings.local.json\n"
	existing := filepath.Join(claude, ".gitignore")
	require.NoError(t, os.WriteFile(existing, []byte(userRules), 0o644))

	tracker := newInitTracker(repo)

	// Snapshot BEFORE writing, exactly as init does.
	tracker.trackModifiedFile(existing)

	written, err := ensureScopedIgnoreFiles(repo)
	require.NoError(t, err)
	require.Len(t, written, 1, "only .claude exists, so only it should be written")
	require.False(t, written[0].Created, "the file already existed; reporting it as created would make rollback delete it")

	// ox's block is in the file now.
	after, err := os.ReadFile(existing)
	require.NoError(t, err)
	require.Contains(t, string(after), "skills/ox-cli-*/")
	require.Contains(t, string(after), "settings.local.json", "ox rewrote the user's rules instead of appending its block")

	tracker.rollback(true)

	restored, err := os.ReadFile(existing)
	require.NoError(t, err, "rollback DELETED a .gitignore the user already had")
	require.Equal(t, userRules, string(restored), "rollback did not restore the user's original rules")
}

// TestInitTracker_RollbackRemovesAnIgnoreFileOxCreated is the mirror case: a file
// ox brought into existence must not survive a failed init, or the repository is
// left with vendor footprint from a command that failed.
func TestInitTracker_RollbackRemovesAnIgnoreFileOxCreated(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".claude"), 0o755))

	tracker := newInitTracker(repo)
	written, err := ensureScopedIgnoreFiles(repo)
	require.NoError(t, err)
	require.Len(t, written, 1)
	require.True(t, written[0].Created, "a file that did not exist must be reported as created")

	created := filepath.Join(repo, written[0].Rel)
	tracker.trackCreatedFile(created)
	tracker.rollback(true)

	_, err = os.Stat(created)
	require.True(t, os.IsNotExist(err), "a failed init left behind an ignore file ox created")
}

// TestScopedIgnoreFiles_AppendsWithoutReorderingUserRules: gitignore ordering is
// semantically significant (a later negation overrides an earlier rule), so ox
// must append its block and touch nothing else.
func TestScopedIgnoreFiles_AppendsWithoutReorderingUserRules(t *testing.T) {
	repo := t.TempDir()
	claude := filepath.Join(repo, ".claude")
	require.NoError(t, os.MkdirAll(claude, 0o755))
	original := "a.txt\n!b/keep.txt\nb/\n"
	require.NoError(t, os.WriteFile(filepath.Join(claude, ".gitignore"), []byte(original), 0o644))

	_, err := ensureScopedIgnoreFiles(repo)
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(claude, ".gitignore"))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(got), original),
		"ox reordered or rewrote the user's rules instead of appending:\n%s", got)
}
