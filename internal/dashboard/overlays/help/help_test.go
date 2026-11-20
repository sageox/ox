package help

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestViewRendersEachBindingFromItsOwnHelp keeps the rows tied to the bindings
// the caller passes. Failure prevented: the modal prints hand-written key text
// that drifts from what the key actually does once a binding is relabeled.
func TestViewRendersEachBindingFromItsOwnHelp(t *testing.T) {
	overlay := New([]Section{
		{Title: "Global", Bindings: []key.Binding{
			key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
			key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		}},
		{Title: "Navigation", Bindings: []key.Binding{
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
		}},
	})

	view := ansi.Strip(overlay.View(80, 24))

	for _, want := range []string{"Global", "Navigation", "r", "refresh", "q", "quit", "esc", "close"} {
		assert.Contains(t, view, want)
	}
	assert.Less(t, strings.Index(view, "Global"), strings.Index(view, "Navigation"), "sections render in the order given")
}

// TestViewEndsAtTheLastRow keeps the modal as short as its content. Failure
// prevented: the blank separator after the last section adds an empty row
// inside the box on every open, pushing a full modal past a small terminal.
func TestViewEndsAtTheLastRow(t *testing.T) {
	overlay := New([]Section{{Title: "Only", Bindings: []key.Binding{
		key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "last row")),
	}}})

	var boxRows []string
	for _, line := range strings.Split(ansi.Strip(overlay.View(80, 24)), "\n") {
		if strings.TrimSpace(line) != "" {
			boxRows = append(boxRows, strings.TrimSpace(line))
		}
	}
	require.GreaterOrEqual(t, len(boxRows), 2, "the modal rendered no box")
	assert.Contains(t, boxRows[len(boxRows)-2], "last row", "the row before the bottom border is the last binding")
}

// TestViewFitsANarrowTerminal keeps the modal inside the terminal's width.
func TestViewFitsANarrowTerminal(t *testing.T) {
	overlay := New([]Section{{Title: "Global", Bindings: []key.Binding{
		key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open in browser")),
	}}})

	for _, line := range strings.Split(ansi.Strip(overlay.View(30, 24)), "\n") {
		assert.LessOrEqual(t, ansi.StringWidth(line), 30, "line %q is wider than the terminal", line)
	}
}
