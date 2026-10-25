package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/paths"
)

// Failure prevented: `ox import` during a team's first clone picked the clone's
// staging directory as "the only team" and wrote data/ into it, losing the
// import when the staging directory was renamed or removed.
func TestAutoDiscoverSingleTeam_IgnoresCloneStagingDirectories(t *testing.T) {
	noInputCLIEnv(t)
	const ep = "http://127.0.0.1:9"
	teamsDir := paths.TeamsDataDir(ep)
	require.NoError(t, os.MkdirAll(filepath.Join(teamsDir, ".team_one.tmp-1"), 0o755))

	assert.Nil(t, autoDiscoverSingleTeam(ep), "a staging directory alone is not a team")

	require.NoError(t, os.MkdirAll(filepath.Join(teamsDir, "team_one"), 0o755))
	got := autoDiscoverSingleTeam(ep)
	require.NotNil(t, got, "the real team is still the single team")
	assert.Equal(t, "team_one", got.TeamID)
}
