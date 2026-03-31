package app

import "charm.land/bubbles/v2/key"

// GlobalKeyMap defines keybindings that are active regardless of which pane has focus.
type GlobalKeyMap struct {
	FocusNext   key.Binding
	FocusPrev   key.Binding
	Refresh     key.Binding
	Help        key.Binding
	Quit        key.Binding
	OpenBrowser key.Binding // opens the selected session/workspace in the browser
	Palette     key.Binding // ctrl+k → command palette
}

// DefaultGlobalKeys returns the standard global keybindings for the dashboard.
func DefaultGlobalKeys() GlobalKeyMap {
	return GlobalKeyMap{
		FocusNext:   key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next pane")),
		FocusPrev:   key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev pane")),
		Refresh:     key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Help:        key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:        key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		OpenBrowser: key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open in browser")),
		Palette:     key.NewBinding(key.WithKeys("ctrl+k"), key.WithHelp("^K", "search")),
	}
}

// PaneKeyMap defines keybindings for navigating within a focused pane.
type PaneKeyMap struct {
	Up     key.Binding
	Down   key.Binding
	Select key.Binding
	Expand key.Binding
}

// DefaultPaneKeys returns the standard within-pane navigation keybindings.
func DefaultPaneKeys() PaneKeyMap {
	return PaneKeyMap{
		Up:     key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("k/↑", "up")),
		Down:   key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j/↓", "down")),
		Select: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		Expand: key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "expand")),
	}
}
