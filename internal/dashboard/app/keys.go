package app

import "charm.land/bubbles/v2/key"

// GlobalKeyMap defines keybindings active regardless of focus.
type GlobalKeyMap struct {
	Quit        key.Binding
	Help        key.Binding
	Refresh     key.Binding
	Palette     key.Binding // ctrl+k → command palette
	OpenBrowser key.Binding // opens selected item in browser

	// Section navigation
	NextSection key.Binding
	PrevSection key.Binding
	Section1    key.Binding
	Section2    key.Binding
	Section3    key.Binding
	Section4    key.Binding
	Section5    key.Binding

	// List navigation
	Up     key.Binding
	Down   key.Binding
	Select key.Binding

	// Inspector
	ToggleInspector key.Binding // enter opens, esc closes
	CloseInspector  key.Binding
}

// DefaultGlobalKeys returns the standard keybindings.
func DefaultGlobalKeys() GlobalKeyMap {
	return GlobalKeyMap{
		Quit:        key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Help:        key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Refresh:     key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Palette:     key.NewBinding(key.WithKeys("ctrl+k"), key.WithHelp("^K", "search")),
		OpenBrowser: key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open in browser")),

		NextSection: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next section")),
		PrevSection: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("S-tab", "prev section")),
		Section1:    key.NewBinding(key.WithKeys("1"), key.WithHelp("1", "overview")),
		Section2:    key.NewBinding(key.WithKeys("2"), key.WithHelp("2", "sync")),
		Section3:    key.NewBinding(key.WithKeys("3"), key.WithHelp("3", "code")),
		Section4:    key.NewBinding(key.WithKeys("4"), key.WithHelp("4", "sessions")),
		Section5:    key.NewBinding(key.WithKeys("5"), key.WithHelp("5", "feed")),

		Up:     key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("k/↑", "up")),
		Down:   key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j/↓", "down")),
		Select: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "inspect")),

		ToggleInspector: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "inspect")),
		CloseInspector:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
	}
}

// PaneKeyMap is kept for backward compatibility with existing pane interfaces.
type PaneKeyMap struct {
	Up     key.Binding
	Down   key.Binding
	Select key.Binding
	Expand key.Binding
}

// DefaultPaneKeys returns the standard pane keybindings.
func DefaultPaneKeys() PaneKeyMap {
	return PaneKeyMap{
		Up:     key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("k/↑", "up")),
		Down:   key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j/↓", "down")),
		Select: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		Expand: key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "expand")),
	}
}
