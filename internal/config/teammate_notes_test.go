package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// session.teammate_notes is a consent switch, so an "off" at any layer wins:
// a repo can opt out over a user's "on", and nothing a repo commits can turn
// notes back on for a user who opted out everywhere.
func TestTeammateNotesEnabledOffAtAnyLayerWins(t *testing.T) {
	tests := []struct {
		name   string
		env    string
		user   string
		repo   string
		want   bool
		source TeammateNotesSource
	}{
		{name: "nothing set: default on", want: true, source: TeammateNotesSourceDefault},
		{name: "user off", user: "off", want: false, source: TeammateNotesSourceUser},
		{name: "repo off, user unset", repo: "off", want: false, source: TeammateNotesSourceRepo},
		{name: "repo off beats user on", user: "on", repo: "off", want: false, source: TeammateNotesSourceRepo},
		{name: "repo on cannot re-enable a user off", user: "off", repo: "on", want: false, source: TeammateNotesSourceUser},
		{name: "user and repo on", user: "on", repo: "on", want: true, source: TeammateNotesSourceUser},
		{name: "env off beats user and repo on", env: "off", user: "on", repo: "on", want: false, source: TeammateNotesSourceEnv},
		{name: "env on cannot re-enable a user off", env: "on", user: "off", want: false, source: TeammateNotesSourceUser},
		{name: "env on cannot re-enable a repo off", env: "on", repo: "off", want: false, source: TeammateNotesSourceRepo},
		{name: "misspelled env is not an on", env: "of", user: "off", want: false, source: TeammateNotesSourceUser},
		{name: "env uppercase OFF", env: "OFF", want: false, source: TeammateNotesSourceEnv},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userCfgPath := filepath.Join(t.TempDir(), "config.yaml")
			body := ""
			if tt.user != "" {
				body = "session_teammate_notes: \"" + tt.user + "\"\n"
			}
			require.NoError(t, os.WriteFile(userCfgPath, []byte(body), 0o600))
			t.Setenv("OX_USER_CONFIG", userCfgPath)
			t.Setenv(EnvTeammateNotes, tt.env)
			root := CreateInitializedProjectWithConfig(t, &ProjectConfig{SessionTeammateNotes: tt.repo})

			assert.Equal(t, tt.want, TeammateNotesEnabled(root))
			got := ResolveTeammateNotes(root)
			assert.Equal(t, tt.source, got.Source)
			if tt.want {
				assert.Equal(t, TeammateNotesOn, got.Mode)
			} else {
				assert.Equal(t, TeammateNotesOff, got.Mode)
			}
		})
	}
}

// A repo's opt-out stays in that repo: another checkout on the same machine
// still receives notes.
func TestTeammateNotesRepoOffIsScopedToThatRepo(t *testing.T) {
	userCfgPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(userCfgPath, []byte("session_teammate_notes: \"on\"\n"), 0o600))
	t.Setenv("OX_USER_CONFIG", userCfgPath)
	t.Setenv(EnvTeammateNotes, "")

	optedOut := CreateInitializedProjectWithConfig(t, &ProjectConfig{SessionTeammateNotes: TeammateNotesOff})
	other := CreateInitializedProjectWithConfig(t, &ProjectConfig{})

	assert.False(t, TeammateNotesEnabled(optedOut))
	assert.True(t, TeammateNotesEnabled(other))
	assert.True(t, TeammateNotesEnabled(""), "outside any repo only the user layer counts")
}
