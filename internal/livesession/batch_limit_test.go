package livesession

import (
	"bytes"
	"strings"
	"testing"
)

// A capped batch holds whole lines only, and a first line longer than the cap
// still goes whole: halving a stalled batch must never split a line.
func TestReadBatchCapsAtWholeLines(t *testing.T) {
	t.Parallel()
	short := `{"n":1}` + "\n"
	long := `{"pad":"` + strings.Repeat("x", 100) + `"}` + "\n"
	tests := []struct {
		name  string
		spool string
		limit int
		want  string
	}{
		{name: "cap between lines", spool: short + short + short, limit: 2*len(short) + 3, want: short + short},
		{name: "cap on a line end", spool: short + short + short, limit: 2 * len(short), want: short + short},
		{name: "first line over the cap goes whole", spool: long + short, limit: 10, want: long},
		{name: "torn tail is not a line", spool: short + `{"n":2`, limit: 1 << 20, want: short},
		{name: "no whole line", spool: `{"n":2`, limit: 1 << 20, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := bytes.NewReader([]byte(tt.spool))
			got, err := readBatch(r, 0, int64(len(tt.spool)), tt.limit)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Fatalf("batch is %d bytes, want %d", len(got), len(tt.want))
			}
		})
	}
}
