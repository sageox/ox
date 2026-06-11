package app

// Rect is an axis-aligned rectangle in terminal cell coordinates.
type Rect struct {
	X, Y, Width, Height int
}

// Layout holds the computed geometry for the dashboard.
// Two modes: full-width main area, or split (main + inspector).
type Layout struct {
	StatusBar Rect // top health indicator bar
	TabBar    Rect // section tab row
	Main      Rect // primary content area
	Inspector Rect // slide-in detail pane (zero-width when closed)
	InputBar  Rect // bottom key hints
}

const (
	// statusBarHeight is the height of the top status/header bar.
	statusBarHeight = 1
	// tabBarHeight is the height of the section tab row.
	tabBarHeight = 1
	// inputBarHeight is the height of the bottom key hints.
	inputBarHeight = 1
	// inspectorMinWidth is the minimum width for the inspector pane.
	inspectorMinWidth = 30
	// minSplitWidth is the minimum terminal width to allow split view
	minSplitWidth = 60
)

// ComputeLayout calculates geometry for a w×h terminal.
// When inspectorOpen is true, the main area splits to share width with the inspector.
func ComputeLayout(w, h int, inspectorOpen bool) Layout {
	if w < 1 {
		w = 80
	}
	if h < 1 {
		h = 24
	}

	chrome := statusBarHeight + tabBarHeight + inputBarHeight
	contentH := h - chrome
	if contentH < 1 {
		contentH = 1
	}

	contentY := statusBarHeight + tabBarHeight

	var mainW, inspW int
	if inspectorOpen && w >= minSplitWidth {
		// inspector gets ~45% of width, minimum inspectorMinWidth
		inspW = w * 45 / 100
		if inspW < inspectorMinWidth {
			inspW = inspectorMinWidth
		}
		if inspW > w-20 {
			inspW = w - 20
		}
		mainW = w - inspW - 1 // 1 for separator
		// fall back to full-width if split would be too cramped
		if inspW <= 0 || mainW < 20 {
			mainW = w
			inspW = 0
		}
	} else {
		mainW = w
		inspW = 0
	}

	return Layout{
		StatusBar: Rect{0, 0, w, statusBarHeight},
		TabBar:    Rect{0, statusBarHeight, w, tabBarHeight},
		Main:      Rect{0, contentY, mainW, contentH},
		Inspector: Rect{mainW + 1, contentY, inspW, contentH},
		InputBar:  Rect{0, h - inputBarHeight, w, inputBarHeight},
	}
}
