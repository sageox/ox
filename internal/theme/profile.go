package theme

import (
	"image/color"
	"os"
	"strconv"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/mattn/go-isatty"
)

// Profile is the terminal's color capability, resolved once at package init.
//
// Lip Gloss v2 deleted the per-renderer downsampling that v1 did inside
// Style.Render(): Render now always emits 24-bit "38;2;R;G;B" and expects the
// caller to degrade at the output layer (colorprofile.Writer, lipgloss.Println).
// ox has ~300 fmt.Print(style.Render(...)) call sites that write to os.Stdout
// directly and bypass any such writer, so we degrade the *color* instead — a
// color already converted to this profile renders as "38;5;N", a 4-bit code, or
// nothing, at every one of those call sites without touching them.
//
// This is not a cosmetic nicety. A terminal without 24-bit support (macOS
// Terminal.app; anything reporting TERM=xterm-256color with no COLORTERM) does
// not ignore an unrecognized "38;2;..." — it parses the parameters as
// independent SGR codes. Any channel landing in 100-107 therefore becomes a
// *background* color: a prior brand hex ended in 0x6A = 106 = bright-cyan
// background, which painted the `ox --help` command column and the login
// disclaimer as unreadable grey-on-cyan blocks. Several other palette entries
// sit one token away from the same trap (#2DD4BF starts 0x2D = 45 = magenta
// background), so pinning the one bad hex would not have been a fix.
//
// Init ordering matters and is guaranteed: package-level vars of imported
// packages are evaluated before the main package's init(), so this observes the
// real os.Stdout rather than the ANSI-stripping pipe cmd/ox/main.go swaps in for
// non-TTY output. Detecting against the real stream is what we want — it is how
// piped output resolves to NoTTY and drops color at the source.
var Profile = detectProfile()

// detectProfile honors an explicit OX_COLOR_PROFILE override, then falls back to
// colorprofile's environment rules (NO_COLOR, CLICOLOR, CLICOLOR_FORCE,
// COLORTERM, TERM, terminfo, tmux).
//
// The override is a support tool: a rendering bug that only reproduces on a
// 256-color terminal can be reproduced on any terminal with
// `OX_COLOR_PROFILE=ansi256 ox --help`. Without it, the only way to see what a
// Terminal.app user sees is to own a Terminal.app.
func detectProfile() colorprofile.Profile {
	if forced, ok := parseProfile(os.Getenv("OX_COLOR_PROFILE")); ok {
		return forced
	}
	if noColorRequested() {
		// colorprofile only clamps NO_COLOR when stdout is a TTY, so
		// `NO_COLOR=1 CLICOLOR_FORCE=1 ox > file` slips through as 4-bit ANSI.
		// NO_COLOR wins over CLICOLOR_FORCE (https://no-color.org) and this
		// repo treats it as sacred, so clamp it here regardless of stream type.
		return colorprofile.ASCII
	}
	if forcingColorToARenderer() {
		return colorprofile.TrueColor
	}
	return colorprofile.Detect(os.Stdout, os.Environ())
}

func noColorRequested() bool {
	noColor, _ := strconv.ParseBool(os.Getenv("NO_COLOR"))
	return noColor
}

// forcingColorToARenderer reports whether CLICOLOR_FORCE is asking for color on
// a stream that is not a terminal.
//
// That combination means the consumer is a *renderer*, not a terminal:
// charmbracelet/freeze turning `ox dev catalog` into the catalog SVGs, or
// asciinema recording the demo casts (Makefile `catalog-build` / `demo-record`).
// Their fidelity ceiling is the source, not any terminal's capability, so
// TrueColor is the correct answer and anything less bakes a permanent downgrade
// into published assets.
//
// colorprofile.Detect cannot reach that conclusion on its own — with no TTY to
// interrogate it floors CLICOLOR_FORCE at 4-bit ANSI unless COLORTERM happens to
// be set too. `catalog-build` does set it; `demo-record` does not, and lost all
// 24-bit color the first time this file existed without this branch.
//
// Deliberately narrow: when stdout *is* a terminal we trust detection, because
// upgrading a real 256-color terminal to TrueColor would reintroduce the exact
// unreadable-background bug this file exists to prevent.
func forcingColorToARenderer() bool {
	if force, _ := strconv.ParseBool(os.Getenv("CLICOLOR_FORCE")); !force {
		return false
	}
	return !isatty.IsTerminal(os.Stdout.Fd()) && !isatty.IsCygwinTerminal(os.Stdout.Fd())
}

// parseProfile maps an OX_COLOR_PROFILE value to a profile. Unrecognized values
// report false rather than erroring, so a typo degrades to normal detection
// instead of breaking every command's output.
func parseProfile(s string) (colorprofile.Profile, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "truecolor", "24bit":
		return colorprofile.TrueColor, true
	case "ansi256", "256":
		return colorprofile.ANSI256, true
	case "ansi", "16":
		return colorprofile.ANSI, true
	case "ascii", "none":
		return colorprofile.ASCII, true
	case "notty":
		return colorprofile.NoTTY, true
	}
	return colorprofile.Unknown, false
}

// Adapt downgrades a color to something the terminal can actually render.
//
// Every color that reaches a Style outside Bubble Tea must pass through here.
// Bubble Tea v2 downsamples its own frames, so TUI code (internal/dashboard,
// the tea models in cmd/ox) deliberately does not.
//
// Returns nil for the ASCII and NoTTY profiles; lipgloss treats a nil color as
// "unset" and emits text attributes only, which is the intended NO_COLOR shape.
func Adapt(c color.Color) color.Color {
	return Profile.Convert(c)
}

// Color parses a hex ("#7AAA77") or ANSI index ("214") color and adapts it to
// the terminal's profile. Use this anywhere lipgloss.Color would be reached for
// in non-TUI code.
func Color(s string) color.Color {
	return Adapt(lipgloss.Color(s))
}
