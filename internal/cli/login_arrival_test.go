package cli

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeviceCodeLines_AlwaysShowsTheCode is the golden for the pre-approval
// screen.
// Failure prevented: the code vanishing whenever the URL already carries it
// (the old "Visit: <complete URL>" alone). RFC 8628's phishing check is a
// person comparing the code the terminal showed with the one the approval page
// shows; with no code on the terminal there is nothing to compare, and the
// check becomes a click.
func TestDeviceCodeLines_AlwaysShowsTheCode(t *testing.T) {
	tests := []struct {
		name           string
		visitURL       string
		code           string
		urlCarriesCode bool
		want           string
	}{
		{
			name:           "URL carries the code, and the code still prints",
			visitURL:       "https://sageox.ai/device?user_code=WXYZ-1234",
			code:           "WXYZ-1234",
			urlCarriesCode: true,
			want: "Visit: https://sageox.ai/device?user_code=WXYZ-1234\n" +
				"Code:  WXYZ-1234  (match this in your browser)\n",
		},
		{
			name:     "no code in the URL, so the person types it",
			visitURL: "https://sageox.ai/device",
			code:     "WXYZ-1234",
			want: "Visit: https://sageox.ai/device\n" +
				"Enter code: WXYZ-1234\n",
		},
		{
			name:           "a code from the server cannot rewrite the terminal",
			visitURL:       "https://sageox.ai/device?user_code=X",
			code:           "AB\x1b[2J\x1b[HCD",
			urlCarriesCode: true,
			want: "Visit: https://sageox.ai/device?user_code=X\n" +
				"Code:  ABCD  (match this in your browser)\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ansi.Strip(DeviceCodeLines(tt.visitURL, tt.code, tt.urlCarriesCode))
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestLoginArrival_Golden is the snapshot of the success block without art:
// what a pipe, CI, --quiet or --json run prints. Three lines, who then where
// then what's next.
// Failure prevented: "Welcome back" greeting someone's first login, and the
// block growing past login-flow.md's "three lines max".
func TestLoginArrival_Golden(t *testing.T) {
	tests := []struct {
		name    string
		arrival LoginArrival
		want    string
	}{
		{
			name:    "first login",
			arrival: NewLoginArrival("Person A", "person.a@company.test", "sageox.ai", false),
			want: "✓ Welcome, Person (person.a@company.test)\n" +
				"  Endpoint: sageox.ai\n" +
				"  Next:     ox init\n",
		},
		{
			name:    "returning login",
			arrival: NewLoginArrival("Person A", "person.a@company.test", "sageox.ai", true),
			want: "✓ Welcome back, Person (person.a@company.test)\n" +
				"  Endpoint: sageox.ai\n" +
				"  Next:     ox init\n",
		},
		{
			name:    "an account with no name is greeted by its email",
			arrival: NewLoginArrival("", "person.a@company.test", "staging.sageox.ai", true),
			want: "✓ Welcome back, person.a@company.test\n" +
				"  Endpoint: staging.sageox.ai\n" +
				"  Next:     ox init\n",
		},
		{
			name:    "a hostile display name cannot rewrite the terminal",
			arrival: NewLoginArrival("Eve\x1b]0;pwned\x07 Smith", "eve@company.test", "sageox.ai", false),
			want: "✓ Welcome, Eve (eve@company.test)\n" +
				"  Endpoint: sageox.ai\n" +
				"  Next:     ox init\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.arrival.Render(nil)
			assert.Equal(t, tt.want, ansi.Strip(got))
			assert.NotContains(t, got, "\x1b]", "no operating-system commands reach the terminal")

			lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
			require.Len(t, lines, 3, "login-flow.md: three lines max")
			for _, line := range lines {
				assert.LessOrEqual(t, ansi.StringWidth(line), 80)
			}
		})
	}
}

// TestLoginArrival_RendersBesideArt is the snapshot of the interactive
// terminal's block: Oxy's picture on the left, the three lines level with the
// middle of the figure. The art here is a stand-in with a known shape; the real
// picture is pinned in internal/pet.
// Failure prevented: text landing on the wrong rows, or art rows of uneven width
// shearing the block.
func TestLoginArrival_RendersBesideArt(t *testing.T) {
	art := []string{"▄▄▄▄▄▄▄▄", "▀▀▀▀▀▀▀▀", "▀▀▀▀▀▀▀▀", "▀▀▀▀▀▀▀▀", "▀▀▀▀▀▀▀▀", "▀▀▀▀▀▀▀▀"}
	arrival := NewLoginArrival("Person A", "person.a@company.test", "sageox.ai", true)

	got := ansi.Strip(arrival.Render(art))

	want := "▄▄▄▄▄▄▄▄\n" +
		"▀▀▀▀▀▀▀▀  ✓ Welcome back, Person (person.a@company.test)\n" +
		"▀▀▀▀▀▀▀▀    Endpoint: sageox.ai\n" +
		"▀▀▀▀▀▀▀▀    Next:     ox init\n" +
		"▀▀▀▀▀▀▀▀\n" +
		"▀▀▀▀▀▀▀▀\n"
	assert.Equal(t, want, got)
	for _, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		assert.LessOrEqual(t, ansi.StringWidth(line), 80)
	}
}
