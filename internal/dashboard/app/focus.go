package app

// FocusTarget identifies which pane currently holds keyboard focus.
type FocusTarget int

const (
	FocusNav       FocusTarget = iota // left navigation tree
	FocusTimeline                     // center timeline list
	FocusInspector                    // right inspector detail
	focusCount                        // sentinel — number of focusable panes
)

// NextFocus returns the pane that follows f in the Tab cycle.
func NextFocus(f FocusTarget) FocusTarget {
	return FocusTarget((int(f) + 1) % int(focusCount))
}

// PrevFocus returns the pane that precedes f in the Shift+Tab cycle.
func PrevFocus(f FocusTarget) FocusTarget {
	return FocusTarget((int(f) + int(focusCount) - 1) % int(focusCount))
}
