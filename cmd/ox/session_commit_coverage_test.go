package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestContains(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		slice []string
		val   string
		want  bool
	}{
		{"found in middle", []string{"a", "b", "c"}, "b", true},
		{"found at start", []string{"a", "b", "c"}, "a", true},
		{"found at end", []string{"a", "b", "c"}, "c", true},
		{"not found", []string{"a", "b", "c"}, "d", false},
		{"empty slice", []string{}, "a", false},
		{"nil slice", nil, "a", false},
		{"empty string in slice", []string{"", "a"}, "", true},
		{"empty string not in slice", []string{"a", "b"}, "", false},
		{"single element match", []string{"only"}, "only", true},
		{"single element no match", []string{"only"}, "other", false},
		{"case sensitive", []string{"ABC"}, "abc", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := contains(tt.slice, tt.val)
			assert.Equal(t, tt.want, got)
		})
	}
}
