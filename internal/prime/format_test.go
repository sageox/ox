package prime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFormatAge(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{name: "just now", d: 30 * time.Second, want: "just now"},
		{name: "1 minute", d: time.Minute, want: "1m ago"},
		{name: "5 minutes", d: 5 * time.Minute, want: "5m ago"},
		{name: "1 hour", d: time.Hour, want: "1h ago"},
		{name: "3 hours", d: 3 * time.Hour, want: "3h ago"},
		{name: "1 day", d: 24 * time.Hour, want: "1d ago"},
		{name: "7 days", d: 7 * 24 * time.Hour, want: "7d ago"},
		{name: "zero", d: 0, want: "just now"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatAge(tt.d)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSortOtherTeamsByAge(t *testing.T) {
	tests := []struct {
		name  string
		input []OtherTeamEntry
		want  []string // expected slug order
	}{
		{
			name: "sort newest first",
			input: []OtherTeamEntry{
				{Slug: "old", Age: "7d ago"},
				{Slug: "new", Age: "1h ago"},
				{Slug: "mid", Age: "2d ago"},
			},
			want: []string{"new", "mid", "old"},
		},
		{
			name: "empty age goes last",
			input: []OtherTeamEntry{
				{Slug: "no-age", Age: ""},
				{Slug: "fresh", Age: "just now"},
				{Slug: "recent", Age: "5m ago"},
			},
			want: []string{"fresh", "recent", "no-age"},
		},
		{
			name: "just now is freshest",
			input: []OtherTeamEntry{
				{Slug: "minutes", Age: "2m ago"},
				{Slug: "now", Age: "just now"},
			},
			want: []string{"now", "minutes"},
		},
		{
			name:  "empty slice",
			input: []OtherTeamEntry{},
			want:  []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			SortOtherTeamsByAge(tt.input)
			got := make([]string, len(tt.input))
			for i, e := range tt.input {
				got[i] = e.Slug
			}
			assert.Equal(t, tt.want, got)
		})
	}
}
