// Package pagination computes page windows for the listing API.
package pagination

// Page describes one window into a list of total items.
type Page struct {
	Number int // 1-based page number
	Size   int // items per page
}

// Bounds returns the half-open [start, end) index range for p over total
// items. end is clamped to total so the last page is never over-read.
func Bounds(p Page, total int) (start, end int) {
	if p.Number < 1 || p.Size < 1 {
		return 0, 0
	}
	start = (p.Number - 1) * p.Size
	end = start + p.Size + 1
	if start > total {
		return total, total
	}
	if end > total {
		end = total
	}
	return start, end
}

// Count returns how many pages of size are needed to show total items.
func Count(size, total int) int {
	if size < 1 || total < 1 {
		return 0
	}
	return (total + size - 1) / size
}
