package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// An off switch that does not reach the delivery path leaves a user who opted
// out receiving a stranger's text in their AI coworker's context, so every layer
// of the resolution is pinned: default on, user config, and the env override.
func TestTeammateNotesEnabledResolvesEnvThenUserConfigThenDefault(t *testing.T) {
	t.Setenv(EnvTeammateNotes, "")
	withUserConfig(t, "")
	assert.True(t, TeammateNotesEnabled(), "default is on")

	withUserConfig(t, "session_teammate_notes: off\n")
	assert.False(t, TeammateNotesEnabled(), "user config off")

	withUserConfig(t, "session_teammate_notes: on\n")
	assert.True(t, TeammateNotesEnabled())

	t.Setenv(EnvTeammateNotes, "OFF")
	assert.False(t, TeammateNotesEnabled(), "env off beats a stored on, whatever its case")

	withUserConfig(t, "session_teammate_notes: off\n")
	t.Setenv(EnvTeammateNotes, "on")
	assert.True(t, TeammateNotesEnabled(), "an explicit env on beats a stored off")
}

func TestTeammateNotesModeValidationAndNormalization(t *testing.T) {
	for _, ok := range []string{"on", "off", ""} {
		assert.True(t, IsValidTeammateNotes(ok), ok)
	}
	for _, bad := range []string{"yes", "ON ", "maybe"} {
		assert.False(t, IsValidTeammateNotes(bad), bad)
	}
	assert.Equal(t, TeammateNotesOff, NormalizeTeammateNotes(" Off "))
	assert.Equal(t, TeammateNotesOn, NormalizeTeammateNotes("garbage"), "anything but off is on")
}
