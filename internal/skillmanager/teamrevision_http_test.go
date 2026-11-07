package skillmanager

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// An HTTP-materialized Team Context has no git HEAD; its snapshot marker is the
// revision, and a git checkout never consults one.
// Failure prevented: a constant "" revision let prime's fast path skip every
// re-plan, so skills the team edited or retired never left an ephemeral repo.
func TestTeamRevision_UsesHTTPSnapshotOnlyWithoutGit(t *testing.T) {
	t.Parallel()
	writeMarker := func(dir, value string) {
		marker := filepath.Join(dir, filepath.FromSlash(HTTPSnapshotFile))
		require.NoError(t, os.MkdirAll(filepath.Dir(marker), 0o755))
		require.NoError(t, os.WriteFile(marker, []byte(value+"\n"), 0o644))
	}

	plain := t.TempDir()
	require.Empty(t, teamRevision(plain), "no git and no snapshot means no team component")
	writeMarker(plain, "abc")
	require.Equal(t, "http:abc", teamRevision(plain))
	writeMarker(plain, "def")
	require.Equal(t, "http:def", teamRevision(plain), "a new snapshot must move the revision")

	checkout := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(checkout, ".git", "refs", "heads"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(checkout, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(checkout, ".git", "refs", "heads", "main"), []byte("0123abcd\n"), 0o644))
	writeMarker(checkout, "abc")
	require.Equal(t, "0123abcd", teamRevision(checkout), "a git checkout keys on its commit, never a stray marker")
}
