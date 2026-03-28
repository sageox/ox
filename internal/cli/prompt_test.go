package cli

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
)

// helper to create a KeyPressMsg for tests
func keyPress(code rune, mod tea.KeyMod, text string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Mod: mod, Text: text})
}

func TestSelectModel_Init(t *testing.T) {
	t.Parallel()

	m := selectModel{
		title:   "Choose:",
		options: []string{"a", "b", "c"},
	}

	cmd := m.Init()
	assert.Nil(t, cmd, "Init should return nil")
}

func TestSelectModel_Update_Navigation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		initial    int
		msg        tea.Msg
		wantCursor int
		wantDone   bool
	}{
		{"down moves cursor", 0, keyPress(tea.KeyDown, 0, ""), 1, false},
		{"j moves cursor down", 0, keyPress('j', 0, "j"), 1, false},
		{"up moves cursor", 2, keyPress(tea.KeyUp, 0, ""), 1, false},
		{"k moves cursor up", 2, keyPress('k', 0, "k"), 1, false},
		{"down at bottom stays", 2, keyPress(tea.KeyDown, 0, ""), 2, false},
		{"up at top stays", 0, keyPress(tea.KeyUp, 0, ""), 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := selectModel{
				title:   "Choose:",
				options: []string{"a", "b", "c"},
				cursor:  tt.initial,
			}

			result, _ := m.Update(tt.msg)
			rm := result.(selectModel)
			assert.Equal(t, tt.wantCursor, rm.cursor)
			assert.Equal(t, tt.wantDone, rm.done)
		})
	}
}

func TestSelectModel_Update_Selection(t *testing.T) {
	t.Parallel()

	m := selectModel{
		title:   "Choose:",
		options: []string{"a", "b", "c"},
		cursor:  1,
	}

	result, cmd := m.Update(keyPress(tea.KeyEnter, 0, ""))
	rm := result.(selectModel)

	assert.True(t, rm.done, "enter should mark as done")
	assert.Equal(t, 1, rm.selected, "selected should match cursor position")
	assert.False(t, rm.canceled)
	assert.NotNil(t, cmd, "enter should return quit command")
}

func TestSelectModel_Update_Space_Selects(t *testing.T) {
	t.Parallel()
	m := selectModel{
		title:   "Choose:",
		options: []string{"a", "b", "c"},
		cursor:  1,
	}

	result, cmd := m.Update(keyPress(tea.KeySpace, 0, "space"))
	rm := result.(selectModel)

	assert.True(t, rm.done)
	assert.False(t, rm.canceled)
	assert.Equal(t, 1, rm.selected)
	assert.NotNil(t, cmd)
}

func TestSelectModel_Update_Cancel(t *testing.T) {
	t.Parallel()

	cancelMsgs := []struct {
		name string
		msg  tea.Msg
	}{
		{"ctrl+c", keyPress('c', tea.ModCtrl, "")},
		{"q", keyPress('q', 0, "q")},
		{"esc", keyPress(tea.KeyEsc, 0, "")},
	}

	for _, tc := range cancelMsgs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := selectModel{
				title:   "Choose:",
				options: []string{"a", "b"},
				cursor:  0,
			}

			result, cmd := m.Update(tc.msg)
			rm := result.(selectModel)

			assert.True(t, rm.canceled, "%s should cancel", tc.name)
			assert.True(t, rm.done, "%s should mark done", tc.name)
			assert.NotNil(t, cmd, "%s should return quit command", tc.name)
		})
	}
}

func TestSelectModel_View_Done_ReturnsEmptyView(t *testing.T) {
	t.Parallel()

	m := selectModel{
		title:   "Pick:",
		options: []string{"a"},
		done:    true,
	}

	view := m.View()
	// when done, View returns NewView("") - the view's string representation is empty
	expected := tea.NewView("")
	assert.Equal(t, expected, view, "done view should be empty NewView")
}

func TestSelectOne_EmptyOptions(t *testing.T) {
	t.Parallel()

	idx, err := SelectOne("Title", []string{}, 0)
	assert.Error(t, err, "empty options should return an error")
	assert.Equal(t, -1, idx)
}

func TestSelectOneValue_EmptyOptions(t *testing.T) {
	t.Parallel()

	val, err := SelectOneValue("Title", []SelectOption[string]{}, 0)
	assert.NoError(t, err)
	assert.Empty(t, val, "empty options should return zero value")
}

func TestInputModel_Init(t *testing.T) {
	t.Parallel()

	m := inputModel{title: "Enter name:"}
	cmd := m.Init()
	assert.Nil(t, cmd)
}

func TestInputModel_Update_Typing(t *testing.T) {
	t.Parallel()

	m := inputModel{title: "Name:"}

	// type 'h' - single printable character
	result, _ := m.Update(keyPress('h', 0, "h"))
	rm := result.(inputModel)
	assert.Equal(t, "h", rm.value)

	// type 'i'
	result, _ = rm.Update(keyPress('i', 0, "i"))
	rm = result.(inputModel)
	assert.Equal(t, "hi", rm.value)
}

func TestInputModel_Update_Backspace(t *testing.T) {
	t.Parallel()

	m := inputModel{title: "Name:", value: "hello"}

	result, _ := m.Update(keyPress(tea.KeyBackspace, 0, ""))
	rm := result.(inputModel)
	assert.Equal(t, "hell", rm.value)
}

func TestInputModel_Update_BackspaceOnEmpty(t *testing.T) {
	t.Parallel()

	m := inputModel{title: "Name:", value: ""}

	result, _ := m.Update(keyPress(tea.KeyBackspace, 0, ""))
	rm := result.(inputModel)
	assert.Empty(t, rm.value, "backspace on empty should stay empty")
}

func TestInputModel_Update_Enter(t *testing.T) {
	t.Parallel()

	m := inputModel{title: "Name:", value: "test"}

	result, cmd := m.Update(keyPress(tea.KeyEnter, 0, ""))
	rm := result.(inputModel)

	assert.True(t, rm.done)
	assert.False(t, rm.canceled)
	assert.Equal(t, "test", rm.value)
	assert.NotNil(t, cmd)
}

func TestInputModel_Update_Cancel(t *testing.T) {
	t.Parallel()

	cancelMsgs := []struct {
		name string
		msg  tea.Msg
	}{
		{"ctrl+c", keyPress('c', tea.ModCtrl, "")},
		{"esc", keyPress(tea.KeyEsc, 0, "")},
	}

	for _, tc := range cancelMsgs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := inputModel{title: "Name:", value: "partial"}

			result, cmd := m.Update(tc.msg)
			rm := result.(inputModel)

			assert.True(t, rm.canceled)
			assert.True(t, rm.done)
			assert.NotNil(t, cmd)
		})
	}
}

func TestInputModel_View_Done_ReturnsEmptyView(t *testing.T) {
	t.Parallel()

	m := inputModel{
		title: "Name:",
		done:  true,
	}

	view := m.View()
	expected := tea.NewView("")
	assert.Equal(t, expected, view, "done view should be empty NewView")
}
