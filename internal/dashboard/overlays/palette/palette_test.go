package palette

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type selectedMsg struct{ label string }

func TestOverlayFiltersNavigatesAndSelectsVisibleItem(t *testing.T) {
	o := New([]Item{
		{Label: "Alpha session", Sub: "sessions", Cmd: func() tea.Msg { return selectedMsg{"alpha"} }},
		{Label: "Beta workspace", Sub: "workspaces", Cmd: func() tea.Msg { return selectedMsg{"beta"} }},
		{Label: "Gamma session", Sub: "sessions", Cmd: func() tea.Msg { return selectedMsg{"gamma"} }},
	})

	for _, r := range "session" {
		updated, _, consumed := o.Update(keyPress(string(r)))
		require.True(t, consumed)
		o = updated.(*Overlay)
	}
	assert.Len(t, o.filtered(), 2, "filtering by category must retain both sessions")

	updated, _, consumed := o.Update(keyPress("j"))
	require.True(t, consumed)
	o = updated.(*Overlay)
	assert.Equal(t, 1, o.cursor)

	closed, cmd, consumed := o.Update(keyPress("enter"))
	assert.True(t, consumed)
	assert.Nil(t, closed, "selection closes the palette")
	require.NotNil(t, cmd)
	assert.Equal(t, selectedMsg{"gamma"}, cmd())
}

func TestOverlayQueryChangesResetCursorAndCancellationRunsNoCommand(t *testing.T) {
	o := New([]Item{{Label: "one"}, {Label: "two"}})
	o.cursor = 1

	updated, cmd, consumed := o.Update(keyPress("x"))
	require.True(t, consumed)
	assert.Nil(t, cmd)
	o = updated.(*Overlay)
	assert.Zero(t, o.cursor)
	assert.Equal(t, "x", o.query)

	updated, _, _ = o.Update(keyPress("backspace"))
	o = updated.(*Overlay)
	assert.Empty(t, o.query)

	closed, cmd, consumed := o.Update(keyPress("esc"))
	assert.True(t, consumed)
	assert.Nil(t, closed)
	assert.Nil(t, cmd, "cancel must never execute the highlighted action")
}

func TestOverlayDeclinesNonKeyboardMessages(t *testing.T) {
	o := New(nil)
	updated, cmd, consumed := o.Update(struct{}{})
	assert.False(t, consumed)
	assert.Same(t, o, updated)
	assert.Nil(t, cmd)
}

func keyPress(name string) tea.KeyPressMsg {
	switch name {
	case "enter":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	case "esc":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})
	case "backspace":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace})
	default:
		r := []rune(name)
		return tea.KeyPressMsg(tea.Key{Text: name, Code: r[0]})
	}
}
