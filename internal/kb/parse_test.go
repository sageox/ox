package kb

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNormalizeSlugArg pins the input-parsing rule that a single leading
// "#" in a CLI slug argument is stripped before resolution.
// Failure prevented: the resolver would treat "#marketing" as a literal
// slug and never match the bare "marketing" stored on disk and in the API.
func TestNormalizeSlugArg(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"bare", "marketing", "marketing"},
		{"prefixed", "#marketing", "marketing"},
		// Only one leading "#" is stripped — documents intent so a
		// double-prefix typo surfaces as "kb not found" instead of
		// being silently coerced.
		{"double-prefix-not-stripped", "##weird", "#weird"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeSlugArg(tc.in)
			assert.Equal(t, tc.want, got)
		})
	}
}
