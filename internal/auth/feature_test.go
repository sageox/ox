package auth

import (
	"testing"
)

func TestIsMemoryEnabled(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"empty", "", false},
		{"true", "true", true},
		{"TRUE", "TRUE", true},
		{"1", "1", true},
		{"yes", "yes", true},
		{"false", "false", false},
		{"0", "0", false},
		{"no", "no", false},
		{"random", "random", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FEATURE_MEMORY", tt.value)
			if got := IsMemoryEnabled(); got != tt.want {
				t.Errorf("IsMemoryEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
