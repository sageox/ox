package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// Failure prevented: the stale-staging sweep deleted a coworker's directory
// that merely looked like staging (".team.tmp-notes") or belonged to a
// different target matched by a wildcard in the path.
func TestSweepStaleTeamCloneTemps_OnlyRemovesVerifiedOxStaging(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	repoPath := filepath.Join(parent, "team")

	// ox-owned, dead staging: literal name + digits + marker -> removed
	owned := filepath.Join(parent, ".team.tmp-456")
	writeFile(t, filepath.Join(owned, "partial"), "x")
	require.NoError(t, markTeamCloneStaging(owned))

	// look-alikes that must survive
	notes := filepath.Join(parent, ".team.tmp-notes")
	writeFile(t, filepath.Join(notes, "user.txt"), "keep")
	unmarked := filepath.Join(parent, ".team.tmp-123")
	writeFile(t, filepath.Join(unmarked, "user.txt"), "keep")
	otherTarget := filepath.Join(parent, ".team2.tmp-789")
	writeFile(t, filepath.Join(otherTarget, "partial"), "keep")
	require.NoError(t, markTeamCloneStaging(otherTarget))

	sweepStaleTeamCloneTemps(repoPath)

	assert.NoDirExists(t, owned, "verified ox staging for this target is swept")
	assert.NoFileExists(t, owned+teamCloneStagingMarkerSuffix)
	assert.FileExists(t, filepath.Join(notes, "user.txt"))
	assert.FileExists(t, filepath.Join(unmarked, "user.txt"), "unmarked directory is not proven to be ox's")
	assert.FileExists(t, filepath.Join(otherTarget, "partial"), "another target's staging is not ours")
}

// Failure prevented: a wildcard in the target name ("te*") was used as a glob
// and swept staging directories of other targets (".team.tmp-1").
func TestSweepStaleTeamCloneTemps_WildcardTargetNameIsLiteral(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("'*' is not a valid file name character on windows")
	}
	parent := t.TempDir()
	victim := filepath.Join(parent, ".team.tmp-1")
	writeFile(t, filepath.Join(victim, "partial"), "keep")
	require.NoError(t, markTeamCloneStaging(victim))

	sweepStaleTeamCloneTemps(filepath.Join(parent, "te*"))
	sweepStaleTeamCloneTemps(filepath.Join(parent, "t?am"))
	sweepStaleTeamCloneTemps(filepath.Join(parent, "[a-z]eam"))

	assert.FileExists(t, filepath.Join(victim, "partial"))
}

// A staging directory that is a symlink must never be followed or removed.
func TestSweepStaleTeamCloneTemps_SkipsSymlinkedStaging(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	parent := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "precious"), "keep")
	link := filepath.Join(parent, ".team.tmp-9")
	require.NoError(t, os.Symlink(outside, link))
	require.NoError(t, markTeamCloneStaging(link))

	sweepStaleTeamCloneTemps(filepath.Join(parent, "team"))

	assert.FileExists(t, filepath.Join(outside, "precious"))
}
