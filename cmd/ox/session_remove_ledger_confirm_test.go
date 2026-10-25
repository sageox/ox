package main

import (
	"errors"
	"testing"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A ledger removal is the most consequential branch of `ox session remove`: it
// affects every coworker and cannot be undone. Taking the default silently
// printed "Canceled." and exited 0, indistinguishable from a real decline.
func TestRemoveSessionByPattern_LedgerConfirmation(t *testing.T) {
	const sessionName = "2026-01-01T00-00-testuser-OxLdg1"

	newFixture := func(t *testing.T) (*session.Store, *draftLedgerFixture) {
		t.Helper()
		setTestCfg(t)
		f := newDraftLedgerFixture(t)
		t.Chdir(f.projectRoot)

		finalizedLedgerSession(t, f.ledgerPath, sessionName)
		runGit(t, f.ledgerPath, "add", "--sparse", "--", "sessions/"+sessionName+"/meta.json")
		runGit(t, f.ledgerPath, "commit", "--no-verify", "-m", "session: "+sessionName)
		f.push(t)

		// the "local" store is the session cache, never the ledger — passing
		// the ledger would route down the local-delete branch instead
		localStore, err := session.NewStore(t.TempDir())
		require.NoError(t, err)
		return localStore, f
	}

	t.Run("unanswered prompt removes nothing from the ledger", func(t *testing.T) {
		store, f := newFixture(t)

		var err error
		silenceStdout(t, func() {
			withStdin(t, "", func() {
				err = removeSessionByPattern(store, sessionName, false)
			})
		})

		assert.True(t, errors.Is(err, cli.ErrConfirmationRequired), "got %v", err)
		assert.Contains(t, remoteTree(t, f.barePath), "sessions/"+sessionName+"/meta.json",
			"a shared-ledger deletion must never happen on a silent default")
	})

	t.Run("declining removes nothing", func(t *testing.T) {
		store, f := newFixture(t)

		var err error
		silenceStdout(t, func() {
			withStdin(t, "n\n", func() {
				err = removeSessionByPattern(store, sessionName, false)
			})
		})

		require.NoError(t, err)
		assert.Contains(t, remoteTree(t, f.barePath), "sessions/"+sessionName+"/meta.json")
	})

	t.Run("an explicit yes removes it", func(t *testing.T) {
		store, f := newFixture(t)

		var err error
		silenceStdout(t, func() {
			withStdin(t, "y\n", func() {
				err = removeSessionByPattern(store, sessionName, false)
			})
		})

		require.NoError(t, err)
		assert.NotContains(t, remoteTree(t, f.barePath), "sessions/"+sessionName+"/meta.json",
			"positive control: an explicit yes must actually remove")
	})
}
