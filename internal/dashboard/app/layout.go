package app

// Rect is an axis-aligned rectangle in terminal cell coordinates.
type Rect struct {
	X, Y, Width, Height int
}

// Layout holds the computed geometry for every pane in the dashboard.
// All values are in terminal cells (columns / rows).
type Layout struct {
	Header    Rect
	Nav       Rect
	Timeline  Rect
	Inspector Rect
	StatusBar Rect
}

const (
	// navWidthFraction is the fraction of total width allocated to the nav pane.
	navWidthFraction = 0.22
	// timelineWidthFraction is the fraction allocated to the timeline pane.
	timelineWidthFraction = 0.42
	// headerHeight is the height of the top header bar in rows.
	headerHeight = 1
	// statusBarHeight is the height of the bottom status bar in rows.
	statusBarHeight = 1
	// separatorWidth is the width of the vertical divider between panes.
	separatorWidth = 1
)

// ComputeLayout calculates pane geometry for a w×h terminal.
// Widths are distributed proportionally; the inspector takes whatever remains
// after nav, timeline, and separators are allocated.
func ComputeLayout(w, h int) Layout {
	if w < 1 {
		w = 80
	}
	if h < 1 {
		h = 24
	}

	contentH := h - headerHeight - statusBarHeight
	if contentH < 1 {
		contentH = 1
	}

	navW := maxInt(1, int(float64(w)*navWidthFraction))
	timelineW := maxInt(1, int(float64(w)*timelineWidthFraction))
	inspW := maxInt(1, w-navW-timelineW-2*separatorWidth)

	timelineX := navW + separatorWidth
	inspX := timelineX + timelineW + separatorWidth
	contentY := headerHeight

	return Layout{
		Header:    Rect{0, 0, w, headerHeight},
		Nav:       Rect{0, contentY, navW, contentH},
		Timeline:  Rect{timelineX, contentY, timelineW, contentH},
		Inspector: Rect{inspX, contentY, inspW, contentH},
		StatusBar: Rect{0, h - statusBarHeight, w, statusBarHeight},
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
