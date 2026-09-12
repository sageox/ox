package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recentSessionName builds a session directory name inside the store's default
// 7-day listing window. ListSessions filters on the timestamp parsed from the
// directory NAME, so a hardcoded past date is invisible to it.
func recentSessionName(suffix string) string {
	return time.Now().UTC().Format("2006-01-02T15-04") + "-testuser-" + suffix
}

// localStoreWithSessions builds a session store holding n sessions, so the
// confirmation prompt is reached instead of the "nothing to remove" early exit.
func localStoreWithSessions(t *testing.T, names ...string) (*session.Store, string) {
	t.Helper()

	root := t.TempDir()
	store, err := session.NewStore(root)
	require.NoError(t, err)

	for _, name := range names {
		writeSessionFiles(t, root, name)
	}
	sessions, err := store.ListSessions()
	require.NoError(t, err)
	require.Len(t, sessions, len(names), "fixture must be visible to the store")

	return store, root
}

func sessionDirExists(t *testing.T, root, name string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(root, "sessions", name))
	return err == nil
}

// TestRemoveAllSessions_UnansweredPromptRemovesNothing gates the silent
// no-op: before, an unattended run printed "Canceled." and exited 0, which is
// indistinguishable from a deliberate decline.
func TestRemoveAllSessions_UnansweredPromptRemovesNothing(t *testing.T) {
	name := recentSessionName("OxRmA1")
	store, root := localStoreWithSessions(t, name)

	var err error
	silenceStdout(t, func() {
		withStdin(t, "", func() {
			err = removeAllSessions(store, false)
		})
	})

	assert.True(t, errors.Is(err, cli.ErrConfirmationRequired),
		"want ErrConfirmationRequired, got %v", err)
	assert.True(t, sessionDirExists(t, root, name), "nothing may be removed when nobody answered")
}

// TestRemoveAllSessions_DeclineIsStillAnOrdinaryCancel pins that a real "no"
// remains a successful, quiet cancel rather than becoming an error.
func TestRemoveAllSessions_DeclineIsStillAnOrdinaryCancel(t *testing.T) {
	name := recentSessionName("OxRmA2")
	store, root := localStoreWithSessions(t, name)

	var err error
	silenceStdout(t, func() {
		withStdin(t, "n\n", func() {
			err = removeAllSessions(store, false)
		})
	})

	require.NoError(t, err)
	assert.True(t, sessionDirExists(t, root, name), "declining must leave sessions in place")
}

// TestRemoveAllSessions_PipedYesRemoves proves a scripted answer still works.
func TestRemoveAllSessions_PipedYesRemoves(t *testing.T) {
	name := recentSessionName("OxRmA3")
	store, root := localStoreWithSessions(t, name)

	var err error
	silenceStdout(t, func() {
		withStdin(t, "y\n", func() {
			err = removeAllSessions(store, false)
		})
	})

	require.NoError(t, err)
	assert.False(t, sessionDirExists(t, root, name), "a piped yes must actually remove")
}

// TestRemoveAllSessions_ForceSkipsThePrompt covers the --force short-circuit,
// which now runs through the same helper rather than an outer if.
func TestRemoveAllSessions_ForceSkipsThePrompt(t *testing.T) {
	name := recentSessionName("OxRmA4")
	store, root := localStoreWithSessions(t, name)

	var err error
	silenceStdout(t, func() {
		withStdin(t, "", func() {
			err = removeAllSessions(store, true)
		})
	})

	require.NoError(t, err)
	assert.False(t, sessionDirExists(t, root, name), "--force must remove without reading stdin")
}

// TestRemoveAllSessions_GlobalYesSkipsThePrompt covers the --yes path.
func TestRemoveAllSessions_GlobalYesSkipsThePrompt(t *testing.T) {
	name := recentSessionName("OxRmA5")
	store, root := localStoreWithSessions(t, name)

	cli.SetAssumeYes(true)
	t.Cleanup(func() { cli.SetAssumeYes(false) })

	var err error
	silenceStdout(t, func() {
		withStdin(t, "", func() {
			err = removeAllSessions(store, false)
		})
	})

	require.NoError(t, err)
	assert.False(t, sessionDirExists(t, root, name), "--yes must satisfy this prompt")
}

// TestRemoveAllSessions_NoSessionsNeverPrompts keeps the early exit ahead of
// the prompt: an empty store must not fail just because nobody is attached.
func TestRemoveAllSessions_NoSessionsNeverPrompts(t *testing.T) {
	store, _ := localStoreWithSessions(t)

	var err error
	silenceStdout(t, func() {
		withStdin(t, "", func() {
			err = removeAllSessions(store, false)
		})
	})

	assert.NoError(t, err, "an empty store must short-circuit before the prompt")
}

// TestRemoveSessionByPattern_UnansweredPromptRemovesNothing covers the
// local-only single-match branch of the pattern path.
func TestRemoveSessionByPattern_UnansweredPromptRemovesNothing(t *testing.T) {
	setTestCfg(t)
	name := recentSessionName("OxRmP1")
	store, root := localStoreWithSessions(t, name)
	t.Chdir(t.TempDir())

	var err error
	silenceStdout(t, func() {
		withStdin(t, "", func() {
			err = removeSessionByPattern(store, name, false)
		})
	})

	assert.True(t, errors.Is(err, cli.ErrConfirmationRequired),
		"want ErrConfirmationRequired, got %v", err)
	assert.True(t, sessionDirExists(t, root, name), "nothing may be removed when nobody answered")
}

// TestRemoveSessionByPattern_DeclineRemovesNothing pins the ordinary decline.
func TestRemoveSessionByPattern_DeclineRemovesNothing(t *testing.T) {
	setTestCfg(t)
	name := recentSessionName("OxRmP2")
	store, root := localStoreWithSessions(t, name)
	t.Chdir(t.TempDir())

	var err error
	silenceStdout(t, func() {
		withStdin(t, "n\n", func() {
			err = removeSessionByPattern(store, name, false)
		})
	})

	require.NoError(t, err)
	assert.True(t, sessionDirExists(t, root, name))
}

// TestRemoveSessionByPattern_PipedYesRemoves is the positive control.
func TestRemoveSessionByPattern_PipedYesRemoves(t *testing.T) {
	setTestCfg(t)
	name := recentSessionName("OxRmP3")
	store, root := localStoreWithSessions(t, name)
	t.Chdir(t.TempDir())

	var err error
	silenceStdout(t, func() {
		withStdin(t, "y\n", func() {
			err = removeSessionByPattern(store, name, false)
		})
	})

	require.NoError(t, err)
	assert.False(t, sessionDirExists(t, root, name))
}
