package cli

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withPipedStdin swaps os.Stdin for a pipe, writes content to it, closes the
// write end (so a reader sees EOF after content, or immediately for ""), and
// restores the original os.Stdin on cleanup.
func withPipedStdin(t *testing.T, content string) {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)

	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig })

	if content != "" {
		_, err = w.WriteString(content)
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
}

// TestSelectOneRequired_NoOneHome is the red-first proof for fix H (contract
// D8/H item 3): before selectOneCore existed, a non-TTY SelectOne call with
// nothing piped into stdin (the shape of `ox init` run under CI, piped, or
// inside an agent harness) returned (defaultIdx, nil) — a *successful*
// selection indistinguishable from a human explicitly accepting the default.
// That let `ox init` silently bind a repo to whichever team happened to sort
// first, with no error and no warning. SelectOneRequired must refuse instead.
func TestSelectOneRequired_NoOneHome(t *testing.T) {
	origInteractive := noInteractive
	SetNoInteractive(true)
	t.Cleanup(func() { noInteractive = origInteractive })

	withPipedStdin(t, "") // stdin closed with nothing written: EOF immediately

	idx, err := SelectOneRequired("Team:", []string{"Acme", "Personal"}, 0)
	require.ErrorIs(t, err, ErrNoInteractiveInput, "must report that no one was there to choose, not guess")
	assert.Equal(t, -1, idx)
}

// TestSelectOne_NoOneHome_PreservesLegacyBehavior proves the fix did not
// change SelectOne's existing contract out from under its other callers
// (invite.go, uninstall.go, login.go, doctor_sageox.go, and two other call
// sites in init.go) — they still get a silent (defaultIdx, nil) when stdin
// is empty, exactly as before this change.
func TestSelectOne_NoOneHome_PreservesLegacyBehavior(t *testing.T) {
	origInteractive := noInteractive
	SetNoInteractive(true)
	t.Cleanup(func() { noInteractive = origInteractive })

	withPipedStdin(t, "")

	idx, err := SelectOne("Team:", []string{"Acme", "Personal"}, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, idx, "legacy SelectOne must keep silently returning defaultIdx on EOF")
}

// TestSelectOneRequired_ExplicitBlankEnter proves a human who was actually
// there and pressed Enter to accept the default is NOT confused with "no one
// home" — the read succeeded (got a real, if empty, line) so the choice is
// explicit and SelectOneRequired must succeed.
func TestSelectOneRequired_ExplicitBlankEnter(t *testing.T) {
	origInteractive := noInteractive
	SetNoInteractive(true)
	t.Cleanup(func() { noInteractive = origInteractive })

	withPipedStdin(t, "\n")

	idx, err := SelectOneRequired("Team:", []string{"Acme", "Personal"}, 0)
	require.NoError(t, err)
	assert.Equal(t, 0, idx)
}

// TestSelectOneRequired_ExplicitChoice proves a real typed selection works
// identically to SelectOne.
func TestSelectOneRequired_ExplicitChoice(t *testing.T) {
	origInteractive := noInteractive
	SetNoInteractive(true)
	t.Cleanup(func() { noInteractive = origInteractive })

	withPipedStdin(t, "2\n")

	idx, err := SelectOneRequired("Team:", []string{"Acme", "Personal"}, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, idx)
}
