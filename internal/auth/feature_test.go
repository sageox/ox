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

// TestIsScoutEnabled guards the scout feature gate: unset (the default) must
// read false so ox scout stays unregistered, and only the truthy tokens flip it
// on. Failure prevented: scout shipping enabled-by-default and advertising a
// third-party Perplexity call no one opted into.
func TestIsScoutEnabled(t *testing.T) {
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
			t.Setenv("FEATURE_SCOUT", tt.value)
			if got := IsScoutEnabled(); got != tt.want {
				t.Errorf("IsScoutEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
