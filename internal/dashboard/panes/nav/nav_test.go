package nav

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/sageox/ox/internal/dashboard/app"
	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/dashboard/panes"
	"github.com/sageox/ox/internal/dashboard/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaneIssuesNavigationIntentsOnlyWhenFocused(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want tea.Msg
	}{
		{"up", "k", app.NavCursorUpMsg{}},
		{"down", "j", app.NavCursorDownMsg{}},
		{"expand", " ", app.NavExpandMsg{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := New()
			store := &state.Store{}
			ctx := panes.Context{Store: store, Focused: true}
			_, cmd := p.Update(keyPress(tc.key), ctx)
			require.NotNil(t, cmd)
			assert.IsType(t, tc.want, cmd())

			ctx.Focused = false
			_, cmd = p.Update(keyPress(tc.key), ctx)
			assert.Nil(t, cmd, "unfocused panes must not consume global navigation")
		})
	}
}

func TestPaneSelectionUsesCursorAndRejectsNonSelectableRows(t *testing.T) {
	target := &domain.InspectorTarget{Kind: domain.TargetSession}
	nodes := []domain.NavNode{
		{Kind: domain.NavNodeSection, Label: "Sessions"},
		{Kind: domain.NavNodeSession, Label: "one", Target: target},
		{Kind: domain.NavNodeHint, Label: "none"},
	}

	for _, tc := range []struct {
		name    string
		cursor  int
		wantCmd bool
	}{
		{"section", 0, false},
		{"target", 1, true},
		{"hint", 2, false},
		{"past end", 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := New()
			store := &state.Store{NavCursorPos: tc.cursor}
			ctx := panes.Context{Store: store, Focused: true, NavNodes: nodes}
			_, cmd := p.Update(keyPress("enter"), ctx)
			if !tc.wantCmd {
				assert.Nil(t, cmd)
				return
			}
			require.NotNil(t, cmd)
			msg, ok := cmd().(app.SelectionChangedMsg)
			require.True(t, ok)
			assert.Same(t, target, msg.Target)
		})
	}
}

func TestPaneScrollKeepsCursorInsideViewport(t *testing.T) {
	p := New()
	p.SetSize(panes.Rect{Height: 6}) // three visible rows after chrome
	store := &state.Store{NavCursorPos: 5}
	ctx := panes.Context{Store: store}

	p.adjustScroll(ctx)
	assert.Equal(t, 3, p.scrollTop)

	store.NavCursorPos = 1
	p.adjustScroll(ctx)
	assert.Equal(t, 1, p.scrollTop)

	store.NavCursorPos = -1
	p.adjustScroll(ctx)
	assert.Zero(t, p.scrollTop)
}

func keyPress(name string) tea.KeyPressMsg {
	switch name {
	case "enter":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	case " ":
		return tea.KeyPressMsg(tea.Key{Text: " ", Code: tea.KeySpace})
	default:
		r := []rune(name)
		return tea.KeyPressMsg(tea.Key{Text: name, Code: r[0]})
	}
}
