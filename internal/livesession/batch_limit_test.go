package livesession

import (
	"strings"
	"testing"
)

// A capped send holds whole lines only, and a first line longer than the cap
// still goes whole: halving a stalled batch must never split a line.
func TestCapLinesKeepsWholeLines(t *testing.T) {
	t.Parallel()
	short := `{"n":1}` + "\n"
	long := `{"pad":"` + strings.Repeat("x", 100) + `"}` + "\n"
	tests := []struct {
		name   string
		window string
		limit  int
		want   string
	}{
		{name: "cap between lines", window: short + short + short, limit: 2*len(short) + 3, want: short + short},
		{name: "cap on a line end", window: short + short + short, limit: 2 * len(short), want: short + short},
		{name: "first line over the cap goes whole", window: long + short, limit: 10, want: long},
		{name: "window under the cap goes whole", window: short + long, limit: 1 << 20, want: short + long},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := capLines([]byte(tt.window), tt.limit); string(got) != tt.want {
				t.Fatalf("sent %d bytes, want %d", len(got), len(tt.want))
			}
		})
	}
}
