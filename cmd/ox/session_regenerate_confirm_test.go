package main

import (
	"errors"
	"testing"

	"github.com/sageox/ox/internal/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegenerateAllSessionsArtifacts_Confirmation covers the gate on a bulk
// rewrite: taking the default silently printed "Canceled." and exited 0, which
// is indistinguishable from a deliberate decline.
func TestRegenerateAllSessionsArtifacts_Confirmation(t *testing.T) {
	name := recentSessionName("OxRgA1")

	t.Run("unanswered prompt errors", func(t *testing.T) {
		store, root := localStoreWithSessions(t, name)

		var err error
		silenceStdout(t, func() {
			withStdin(t, "", func() {
				err = regenerateAllSessionsArtifacts(store, root, false)
			})
		})

		assert.True(t, errors.Is(err, cli.ErrConfirmationRequired), "got %v", err)
	})

	t.Run("explicit no is an ordinary cancel", func(t *testing.T) {
		store, root := localStoreWithSessions(t, name)

		var err error
		silenceStdout(t, func() {
			withStdin(t, "n\n", func() {
				err = regenerateAllSessionsArtifacts(store, root, false)
			})
		})

		assert.NoError(t, err)
	})

	t.Run("force skips the prompt", func(t *testing.T) {
		store, root := localStoreWithSessions(t, name)

		var err error
		silenceStdout(t, func() {
			withStdin(t, "", func() {
				err = regenerateAllSessionsArtifacts(store, root, true)
			})
		})

		assert.NoError(t, err, "--force must not need stdin")
	})

	t.Run("global yes skips the prompt", func(t *testing.T) {
		store, root := localStoreWithSessions(t, name)

		cli.SetAssumeYes(true)
		t.Cleanup(func() { cli.SetAssumeYes(false) })

		var err error
		silenceStdout(t, func() {
			withStdin(t, "", func() {
				err = regenerateAllSessionsArtifacts(store, root, false)
			})
		})

		assert.NoError(t, err)
	})

	t.Run("no sessions short-circuits before the prompt", func(t *testing.T) {
		store, root := localStoreWithSessions(t)

		var err error
		silenceStdout(t, func() {
			withStdin(t, "", func() {
				err = regenerateAllSessionsArtifacts(store, root, false)
			})
		})

		require.NoError(t, err, "an empty store must not fail just because nobody is attached")
	})
}
