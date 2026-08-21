package theme

import (
	"strings"
	"testing"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withProfile temporarily overrides the detected profile. Adapt reads Profile at
// call time, so this is enough to exercise every terminal class from a test
// binary whose own stdout is never a TTY.
func withProfile(t *testing.T, p colorprofile.Profile) {
	t.Helper()
	prev := Profile
	Profile = p
	t.Cleanup(func() { Profile = prev })
}

// TestAdapt_NoTruecolorBelowTrueColor is the regression test for the unreadable
// grey-on-cyan help columns.
//
// A terminal without 24-bit support parses the parameters of an unrecognized
// "38;2;R;G;B" as independent SGR codes, so a channel in 100-107 silently
// becomes a background color. Emitting no "38;2;" at all below TrueColor is the
// property that makes that class of bug impossible, for every token — not just
// the copper #E0A56A that happened to trip it.
func TestAdapt_NoTruecolorBelowTrueColor(t *testing.T) {
	degraded := []colorprofile.Profile{colorprofile.ANSI256, colorprofile.ANSI, colorprofile.ASCII, colorprofile.NoTTY}

	for _, tok := range Tokens {
		for _, hex := range []string{tok.LightHex, tok.DarkHex} {
			for _, p := range degraded {
				t.Run(tok.Name+"/"+hex+"/"+p.String(), func(t *testing.T) {
					withProfile(t, p)
					got := lipgloss.NewStyle().Foreground(Color(hex)).Render("x")
					assert.NotContains(t, got, "38;2;",
						"%s renders 24-bit color on a %s terminal", hex, p)
				})
			}
		}
	}
}

// TestAdapt_CopperRendersAsOneIndexedColor pins the exact byte shape of the
// reported bug: brand copper on a 256-color terminal must be a single indexed
// foreground, never the four-parameter form whose trailing 106 reads as
// bright-cyan background.
func TestAdapt_CopperRendersAsOneIndexedColor(t *testing.T) {
	withProfile(t, colorprofile.ANSI256)

	got := lipgloss.NewStyle().Foreground(Color("#E0A56A")).Render("code")

	assert.Contains(t, got, "38;5;")
	assert.NotContains(t, got, "106")
}

// TestAdapt_TrueColorIsPassthrough guards the other direction: modern terminals
// must keep full-fidelity brand color.
func TestAdapt_TrueColorIsPassthrough(t *testing.T) {
	withProfile(t, colorprofile.TrueColor)

	got := lipgloss.NewStyle().Foreground(Color("#E0A56A")).Render("code")

	assert.Contains(t, got, "38;2;224;165;106")
}

// TestAdapt_AdaptiveTokensDegrade covers the generated compat.AdaptiveColor
// values, which reach styles as a struct rather than a parsed hex.
func TestAdapt_AdaptiveTokensDegrade(t *testing.T) {
	withProfile(t, colorprofile.ANSI256)

	for name, c := range map[string]interface {
		RGBA() (uint32, uint32, uint32, uint32)
	}{
		"Primary":   ColorPrimary,
		"Secondary": ColorSecondary,
		"Warning":   ColorWarning,
		"Public":    ColorPublic,
	} {
		got := lipgloss.NewStyle().Foreground(Adapt(c)).Render("x")
		assert.NotContains(t, got, "38;2;", "%s should degrade", name)
	}
}

// TestAdapt_NoColorProfilesEmitNoColor documents that ASCII and NoTTY drop color
// entirely rather than picking a nearest match — lipgloss treats the nil that
// Convert returns as "unset", leaving text attributes intact.
func TestAdapt_NoColorProfilesEmitNoColor(t *testing.T) {
	for _, p := range []colorprofile.Profile{colorprofile.ASCII, colorprofile.NoTTY} {
		withProfile(t, p)

		require.Nil(t, Adapt(lipgloss.Color("#E0A56A")))

		got := lipgloss.NewStyle().Foreground(Color("#E0A56A")).Bold(true).Render("x")
		assert.NotContains(t, got, "38;")
		assert.True(t, strings.Contains(got, "1m"), "bold should survive on %s", p)
	}
}

func TestParseProfile(t *testing.T) {
	cases := map[string]colorprofile.Profile{
		"truecolor": colorprofile.TrueColor,
		"24bit":     colorprofile.TrueColor,
		"  ANSI256": colorprofile.ANSI256,
		"256":       colorprofile.ANSI256,
		"ansi":      colorprofile.ANSI,
		"16":        colorprofile.ANSI,
		"ascii":     colorprofile.ASCII,
		"none":      colorprofile.ASCII,
		"notty":     colorprofile.NoTTY,
	}
	for in, want := range cases {
		got, ok := parseProfile(in)
		assert.True(t, ok, "%q should parse", in)
		assert.Equal(t, want, got, "%q", in)
	}

	// A typo must fall through to normal detection rather than breaking output.
	for _, in := range []string{"", "tru-color", "xterm-256color"} {
		_, ok := parseProfile(in)
		assert.False(t, ok, "%q should not parse", in)
	}
}

// TestDetectProfile_ForcedColorToNonTTY covers the CLICOLOR_FORCE precedence
// ladder. The regression it guards: `CLICOLOR_FORCE=1 ox --help > file` emitted
// 125 truecolor sequences before this file existed and 0 after, because
// colorprofile floors a non-TTY at 4-bit ANSI unless COLORTERM is also set. The
// catalog build sets it; the asciinema demo recording does not.
//
// A test binary's stdout is never a TTY, so these exercise the non-TTY branch
// directly.
func TestDetectProfile_ForcedColorToNonTTY(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want colorprofile.Profile
	}{
		{
			name: "forced color to a renderer keeps full fidelity",
			env:  map[string]string{"CLICOLOR_FORCE": "1"},
			want: colorprofile.TrueColor,
		},
		{
			name: "NO_COLOR outranks CLICOLOR_FORCE",
			env:  map[string]string{"CLICOLOR_FORCE": "1", "NO_COLOR": "1"},
			want: colorprofile.ASCII,
		},
		{
			name: "explicit override outranks CLICOLOR_FORCE",
			env:  map[string]string{"CLICOLOR_FORCE": "1", "OX_COLOR_PROFILE": "ansi256"},
			want: colorprofile.ANSI256,
		},
		{
			name: "plain pipe stays colorless",
			env:  nil,
			want: colorprofile.NoTTY,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear every input so cases don't inherit the ambient environment.
			for _, k := range []string{"CLICOLOR_FORCE", "CLICOLOR", "NO_COLOR", "OX_COLOR_PROFILE", "COLORTERM", "TERM"} {
				t.Setenv(k, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			assert.Equal(t, tt.want, detectProfile())
		})
	}
}
