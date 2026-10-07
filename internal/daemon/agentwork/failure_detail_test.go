package agentwork

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Failure prevented: a non-zero exit from the summarizer CLI whose only
// explanation (a short stderr line) is thrown away, leaving an undiagnosable
// "parse claude output" error (GH #1208).
func TestFailureDetail(t *testing.T) {
	t.Run("stderr wins over stdout", func(t *testing.T) {
		require.Equal(t, "rate limited", failureDetail("  rate limited\n", "ignored"))
	})
	t.Run("stdout is the fallback when stderr is empty", func(t *testing.T) {
		require.Equal(t, "Claude usage limit reached", failureDetail("   ", "Claude usage limit reached\n"))
	})
	t.Run("secrets are redacted", func(t *testing.T) {
		got := failureDetail("not logged in glpat-abcdefghijklmnopqrst", "")
		require.NotContains(t, got, "glpat-abcdefghijklmnopqrst")
		require.Contains(t, got, "not logged in")
	})
	t.Run("long output is truncated at the limit", func(t *testing.T) {
		got := failureDetail(strings.Repeat("x", failureDetailLimit+500), "")
		require.Len(t, got, failureDetailLimit+len("...(truncated)"))
		require.True(t, strings.HasSuffix(got, "...(truncated)"))
	})
	t.Run("empty output stays empty", func(t *testing.T) {
		require.Equal(t, "", failureDetail("", ""))
	})
}
