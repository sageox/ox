package oxactivity

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestColorAllowedFor judges NO_COLOR by the value passed in, not the current
// environment. Failure prevented: ox's own startup NO_COLOR=1 (set whenever
// stdout is a pipe) turning the activity line plain for every coworker, or a
// coworker's NO_COLOR ignored.
func TestColorAllowedFor(t *testing.T) {
	tests := []struct {
		name    string
		noColor string
		profile string
		want    bool
	}{
		{"coworker left NO_COLOR unset", "", "", true},
		{"coworker NO_COLOR=1", "1", "", false},
		{"coworker NO_COLOR=0", "0", "", true},
		{"ascii profile", "", "ascii", false},
		{"notty profile", "", "NoTTY", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", "1") // what ox's init forces inside a harness
			t.Setenv("OX_COLOR_PROFILE", tt.profile)
			assert.Equal(t, tt.want, ColorAllowedFor(tt.noColor))
		})
	}
}
