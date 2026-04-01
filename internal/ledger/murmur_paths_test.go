package ledger

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMurmurDateHourDir(t *testing.T) {
	tests := []struct {
		name string
		time time.Time
		want string
	}{
		{
			name: "afternoon",
			time: time.Date(2026, 3, 22, 14, 30, 0, 0, time.UTC),
			want: filepath.Join("data", "murmurs", "2026-03-22", "14"),
		},
		{
			name: "midnight",
			time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			want: filepath.Join("data", "murmurs", "2026-01-01", "00"),
		},
		{
			name: "end of day",
			time: time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC),
			want: filepath.Join("data", "murmurs", "2026-12-31", "23"),
		},
		{
			name: "single digit month and day",
			time: time.Date(2026, 3, 5, 9, 0, 0, 0, time.UTC),
			want: filepath.Join("data", "murmurs", "2026-03-05", "09"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MurmurDateHourDir(tt.time)
			if got != tt.want {
				t.Errorf("MurmurDateHourDir() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMurmurFilePath(t *testing.T) {
	tests := []struct {
		name string
		time time.Time
		id   string
		want string
	}{
		{
			name: "standard path",
			time: time.Date(2026, 3, 22, 14, 0, 0, 0, time.UTC),
			id:   "01961234-5678-7abc-def0-123456789abc",
			want: filepath.Join("data", "murmurs", "2026-03-22", "14", "01961234-5678-7abc-def0-123456789abc.json"),
		},
		{
			name: "midnight boundary",
			time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			id:   "test-id",
			want: filepath.Join("data", "murmurs", "2026-01-01", "00", "test-id.json"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MurmurFilePath(tt.time, tt.id)
			if got != tt.want {
				t.Errorf("MurmurFilePath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestComputeMurmurDataPaths(t *testing.T) {
	t.Run("generates correct count", func(t *testing.T) {
		paths := ComputeMurmurDataPaths(12)
		if len(paths) != 12 {
			t.Errorf("expected 12 paths, got %d", len(paths))
		}
	})

	t.Run("no duplicates", func(t *testing.T) {
		paths := ComputeMurmurDataPaths(24)
		seen := make(map[string]bool)
		for _, p := range paths {
			if seen[p] {
				t.Errorf("duplicate path: %s", p)
			}
			seen[p] = true
		}
	})

	t.Run("all paths have trailing slash", func(t *testing.T) {
		paths := ComputeMurmurDataPaths(6)
		for _, p := range paths {
			if !strings.HasSuffix(p, "/") {
				t.Errorf("path missing trailing slash: %s", p)
			}
		}
	})

	t.Run("all paths start with data/murmurs/", func(t *testing.T) {
		paths := ComputeMurmurDataPaths(6)
		prefix := filepath.Join("data", "murmurs") + "/"
		for _, p := range paths {
			if !strings.HasPrefix(p, prefix) {
				t.Errorf("path %q does not start with %q", p, prefix)
			}
		}
	})

	t.Run("zero hours returns empty", func(t *testing.T) {
		paths := ComputeMurmurDataPaths(0)
		if len(paths) != 0 {
			t.Errorf("expected 0 paths, got %d", len(paths))
		}
	})
}

func TestComputeMurmurDataPaths_DateBoundaryCrossing(t *testing.T) {
	// Use a 25-hour window to guarantee spanning at least 2 calendar dates
	// regardless of what time the test runs (24h can land within a single
	// date if it runs near midnight UTC).
	paths := ComputeMurmurDataPaths(25)

	dates := make(map[string]bool)
	for _, p := range paths {
		// extract YYYY-MM-DD from "data/murmurs/YYYY-MM-DD/HH/"
		parts := strings.Split(p, "/")
		if len(parts) >= 4 {
			dates[parts[2]] = true
		}
	}

	// a 25-hour window must always span at least 2 calendar dates
	if len(dates) < 2 {
		t.Errorf("24-hour window should span at least 2 dates, got %d: %v", len(dates), dates)
	}
}
