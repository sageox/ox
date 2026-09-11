package pagination

import "testing"

func TestBounds(t *testing.T) {
	cases := []struct {
		name       string
		page       Page
		total      int
		start, end int
	}{
		{"first page", Page{1, 10}, 35, 0, 10},
		{"middle page", Page{2, 10}, 35, 10, 20},
		{"last partial page", Page{4, 10}, 35, 30, 35},
		{"exact fit", Page{2, 5}, 10, 5, 10},
		{"beyond end", Page{9, 10}, 35, 35, 35},
		{"zero page", Page{0, 10}, 35, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, e := Bounds(tc.page, tc.total)
			if s != tc.start || e != tc.end {
				t.Fatalf("Bounds(%+v, %d) = (%d, %d), want (%d, %d)", tc.page, tc.total, s, e, tc.start, tc.end)
			}
		})
	}
}

func TestCount(t *testing.T) {
	if got := Count(10, 35); got != 4 {
		t.Fatalf("Count(10, 35) = %d, want 4", got)
	}
	if got := Count(10, 0); got != 0 {
		t.Fatalf("Count(10, 0) = %d, want 0", got)
	}
}
