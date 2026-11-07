package entries

import (
	"regexp"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/sageox/ox/internal/uicatalog"
	"github.com/stretchr/testify/require"
)

// The modal reference demonstrates a command palette, so every row is a command
// a coworker may copy into a terminal.
// Failure prevented: the reference palette advertises a command that does not
// exist, and the design catalog teaches the wrong invocation.
func TestModalReferenceListsRealCommands(t *testing.T) {
	entry, ok := uicatalog.Get("modal")
	require.True(t, ok)
	require.Equal(t, uicatalog.FamilyLayout, entry.Family)

	text := ansi.Strip(entry.Render())
	for _, command := range []string{
		"ox sync",
		"ox sync --read-only",
		"ox daemon logs",
		"ox status",
	} {
		// the command must end its row, so "ox synch" cannot pass for "ox sync"
		// and the "ox sync --read-only" row cannot stand in for "ox sync"
		row := regexp.MustCompile(`\s` + regexp.QuoteMeta(command) + `\s*│`)
		require.Regexp(t, row, text, "palette must list %q", command)
	}
}
