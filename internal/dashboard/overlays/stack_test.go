package overlays

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stackTestOverlay struct{ id OverlayID }

func (o stackTestOverlay) ID() OverlayID { return o.id }

func (o stackTestOverlay) Update(tea.Msg) (Overlay, tea.Cmd, bool) {
	return o, nil, false
}

func (o stackTestOverlay) View(int, int) string { return "" }

func TestStackIsLIFOAndSafeWhenEmpty(t *testing.T) {
	var stack Stack
	assert.True(t, stack.IsEmpty())
	assert.Nil(t, stack.Top())
	assert.Nil(t, stack.Pop())

	stack.Push(stackTestOverlay{id: OverlayHelp})
	stack.Push(stackTestOverlay{id: OverlayConfirm})
	require.Equal(t, 2, stack.Len())
	require.NotNil(t, stack.Top())
	assert.Equal(t, OverlayConfirm, stack.Top().ID())

	assert.Equal(t, OverlayConfirm, stack.Pop().ID())
	assert.Equal(t, OverlayHelp, stack.Pop().ID())
	assert.True(t, stack.IsEmpty())
}
