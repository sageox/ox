package app

// FocusTarget identifies which area currently holds keyboard focus.
type FocusTarget int

const (
	FocusMain      FocusTarget = iota // main content area (section list)
	FocusInspector                    // slide-in inspector detail
	focusCount                        // sentinel
)

// NextFocus cycles focus forward.
func NextFocus(f FocusTarget) FocusTarget {
	if f < FocusMain || f >= focusCount {
		f = FocusMain
	}
	return FocusTarget((int(f) + 1) % int(focusCount))
}

// PrevFocus cycles focus backward.
func PrevFocus(f FocusTarget) FocusTarget {
	if f < FocusMain || f >= focusCount {
		f = FocusMain
	}
	return FocusTarget((int(f) + int(focusCount) - 1) % int(focusCount))
}
