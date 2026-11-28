package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: `ox config set` or `ox config unset` meeting a config file
// it could not parse treated the error as an empty config and saved that,
// silently dropping every other setting in the file. No t.Parallel here: these
// tests call t.Setenv.

func TestConfigSetAndUnset_LeaveAnUnparsableUserConfigAlone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func() error
	}{
		{"set", func() error { return SetConfigValue("statusline", "off", ConfigLevelUser, "") }},
		{"unset", func() error { return UnsetConfigValue("tips", ConfigLevelUser, "") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			t.Setenv("OX_USER_CONFIG", path)
			// a hand edit that left the file unparsable; telemetry_enabled must survive
			original := "telemetry_enabled: false\ntips_enabled: [off\n"
			require.NoError(t, os.WriteFile(path, []byte(original), 0o600))

			require.Error(t, tc.change(), "a config ox cannot read must not be replaced")

			got, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, original, string(got))
		})
	}
}

func TestUnsetConfigValue_TeamLevelConfigEdges(t *testing.T) {
	setup := func(t *testing.T) (gitRoot, teamConfigPath string) {
		t.Helper()
		gitRoot = t.TempDir()
		sageoxDir := filepath.Join(gitRoot, ".sageox")
		require.NoError(t, os.MkdirAll(sageoxDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(`{"team_id": "team-test"}`), 0o644))
		teamDir := t.TempDir()
		require.NoError(t, config.SaveLocalConfig(gitRoot, &config.LocalConfig{
			TeamContexts: []config.TeamContext{{TeamID: "team-test", TeamName: "Test", Path: teamDir}},
		}))
		return gitRoot, filepath.Join(teamDir, "config.toml")
	}

	t.Run("an unparsable config.toml is left alone", func(t *testing.T) {
		gitRoot, teamConfigPath := setup(t)
		original := "session_recording = \"auto\"\nsession_publishing = [\n"
		require.NoError(t, os.WriteFile(teamConfigPath, []byte(original), 0o644))

		require.Error(t, UnsetConfigValue("session_publishing", ConfigLevelTeam, gitRoot))

		got, err := os.ReadFile(teamConfigPath)
		require.NoError(t, err)
		assert.Equal(t, original, string(got), "the team's other settings must survive")
	})

	// LoadTeamConfig reports a missing file as (nil, nil); unset used to
	// dereference that nil config and panic.
	t.Run("a team with no config.toml has nothing to clear", func(t *testing.T) {
		gitRoot, teamConfigPath := setup(t)

		require.NoError(t, UnsetConfigValue("session_recording", ConfigLevelTeam, gitRoot))
		assert.NoFileExists(t, teamConfigPath, "clearing nothing must not create a team config")
	})
}
