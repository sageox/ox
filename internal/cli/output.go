package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"runtime"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/mattn/go-isatty"
	"github.com/pkg/browser"

	"github.com/sageox/ox/internal/theme"
)

// styles using lipgloss for consistent, terminal-aware styling
// lipgloss handles terminal capability detection and NO_COLOR automatically
//
// Colors are sourced from the sageox-design system.
// See: internal/theme/generated.go
var (
	successStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.AnsiSuccess)).Bold(true)
	preservedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.AnsiPreserved)).Bold(true) // cyan for preserved/kept
	errorStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.AnsiError)).Bold(true)
	warningStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.AnsiWarning)).Bold(true)
	infoStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.AnsiInfo))
	hintStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.AnsiHint))
	codeStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.AnsiCode))
	tipIconStyle    = lipgloss.NewStyle().Foreground(ColorSecondary) // warm gold for ✦
	tipTextStyle    = lipgloss.NewStyle().Foreground(ColorDim)       // muted gray
	tipCommandStyle = lipgloss.NewStyle().Foreground(ColorPrimary)   // sage green for commands
)

var backtickRegex = regexp.MustCompile("`([^`]+)`")

var jsonMode bool
var noInteractive bool

func SetJSONMode(enabled bool) {
	jsonMode = enabled
}

// SetNoInteractive sets the global non-interactive mode flag.
// When enabled, spinners and TUI elements are disabled.
func SetNoInteractive(enabled bool) {
	noInteractive = enabled
}

// IsInteractive returns true if interactive mode is enabled.
// Interactive mode is disabled when --no-interactive flag is set, CI=true,
// or stdin is not a terminal (e.g., running inside an AI agent).
func IsInteractive() bool {
	if noInteractive {
		return false
	}
	return isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())
}

// IsHeadless returns true when no graphical display is available.
// Detects SSH sessions and missing display servers so callers can skip
// browser-open calls that would launch broken text-mode browsers (lynx, w3m).
func IsHeadless() bool {
	// SSH session indicators
	for _, key := range []string{"SSH_CLIENT", "SSH_CONNECTION", "SSH_TTY"} {
		if os.Getenv(key) != "" {
			return true
		}
	}

	// no graphical display on Linux/Unix
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		// macOS always has a display server (even over SSH, open(1) uses Launch Services)
		// so only treat missing DISPLAY as headless on non-macOS
		if runtime.GOOS != "darwin" {
			return true
		}
	}

	return false
}

func PrintJSON(v any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "error encoding JSON: %v\n", err)
	}
}

func PrintSuccess(msg string) {
	if jsonMode {
		PrintJSON(map[string]any{
			"status":  "success",
			"message": msg,
		})
		return
	}
	fmt.Fprintf(os.Stdout, "%s %s\n", successStyle.Render("✓"), msg)
}

// PrintPreserved prints a message indicating an existing file was preserved (not overwritten).
// Uses cyan color to distinguish from green success (created) messages.
func PrintPreserved(msg string) {
	if jsonMode {
		PrintJSON(map[string]any{
			"status":  "preserved",
			"message": msg,
		})
		return
	}
	fmt.Fprintf(os.Stdout, "%s %s\n", preservedStyle.Render("✓"), msg)
}

func PrintError(msg string) {
	if jsonMode {
		PrintJSON(map[string]any{
			"status":  "error",
			"message": msg,
		})
		return
	}
	fmt.Fprintf(os.Stderr, "%s %s\n", errorStyle.Render("✗"), msg)
}

func PrintWarning(msg string) {
	if jsonMode {
		PrintJSON(map[string]any{
			"status":  "warning",
			"message": msg,
		})
		return
	}
	fmt.Fprintf(os.Stderr, "%s %s\n", warningStyle.Render("⚠"), msg)
}

func PrintInfo(msg string) {
	if jsonMode {
		PrintJSON(map[string]any{
			"status":  "info",
			"message": msg,
		})
		return
	}
	fmt.Fprintf(os.Stdout, "%s %s\n", infoStyle.Render("ℹ"), msg)
}

// PrintHint prints a dimmed hint message (e.g., "Run 'ox login' to authenticate")
func PrintHint(msg string) {
	if jsonMode {
		return // hints are not included in JSON output
	}
	fmt.Fprintln(os.Stdout, hintStyle.Render(msg))
}

// PrintActionHint prints a prominent actionable hint with star, command, and optional step.
// Matches the visual styling used in help output for contextual next-action guidance.
// Example: "★ ox login  Authenticate with SageOx (Step 1)"
func PrintActionHint(command, description string, step int) {
	if jsonMode {
		return
	}
	star := StyleCallout.Render("★ ")
	cmd := StyleCalloutBold.Render(command)
	desc := StyleCallout.Render(description)

	var stepSuffix string
	if step > 0 {
		stepSuffix = " " + StyleCallout.Render(fmt.Sprintf("(Step %d)", step))
	}

	fmt.Fprintf(os.Stdout, "%s%s  %s%s\n", star, cmd, desc, stepSuffix)
}

// Styles exposes lipgloss styles for use in commands
var Styles = struct {
	Success   lipgloss.Style
	Preserved lipgloss.Style
	Error     lipgloss.Style
	Warning   lipgloss.Style
	Info      lipgloss.Style
	Hint      lipgloss.Style
	Code      lipgloss.Style
}{
	Success:   successStyle,
	Preserved: preservedStyle,
	Error:     errorStyle,
	Warning:   warningStyle,
	Info:      infoStyle,
	Hint:      hintStyle,
	Code:      codeStyle,
}

// PrintTip prints a helpful tip message with command highlighting
func PrintTip(tip string) {
	if jsonMode {
		return
	}
	formatted := FormatTipText(tip)
	fmt.Fprintf(os.Stderr, "\n%s %s\n", tipIconStyle.Render("✦"), formatted)
}

// FormatTipText formats tip text by highlighting backtick-wrapped commands
func FormatTipText(tip string) string {
	return backtickRegex.ReplaceAllStringFunc(tip, func(match string) string {
		// match is "`command`", so extract without backticks
		command := match[1 : len(match)-1]
		return tipCommandStyle.Render(command)
	})
}

// SuggestionBox renders a bordered box with a title, message, and fix command.
// Used for actionable suggestions that need visual emphasis.
func SuggestionBox(title, message, fix string) string {
	if jsonMode {
		return ""
	}

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorWarning)

	messageStyle := lipgloss.NewStyle().
		Foreground(ColorDim)

	fixStyle := lipgloss.NewStyle().
		Foreground(ColorPrimary).
		Bold(true)

	boxStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(ColorWarning).
		Padding(0, 2).
		MarginTop(1).
		MarginBottom(1)

	content := titleStyle.Render(title) + "\n\n" +
		messageStyle.Render(message)
	if fix != "" {
		content += "\n\n" + "Run: " + fixStyle.Render(fix)
	}

	return boxStyle.Render(content)
}

// PrintSuggestionBox prints a bordered suggestion box to stderr.
func PrintSuggestionBox(title, message, fix string) {
	if jsonMode {
		return
	}
	fmt.Fprintln(os.Stderr, SuggestionBox(title, message, fix))
}

// PrintDisclaimer prints the Claude Code compatibility disclaimer in brand gold.
func PrintDisclaimer() {
	if jsonMode {
		return
	}
	fmt.Println()
	fmt.Println(Wordmark() + StyleSecondary.Render(" is designed initially for Claude Code. Using with other coding agents and claws is not yet being tested."))
}

// ErrHeadless is returned by OpenInBrowser when the environment has no graphical
// display (SSH session, no DISPLAY). Callers should handle this to provide
// context-appropriate guidance (e.g., upload to view online, or copy the file).
var ErrHeadless = errors.New("cannot open browser: no graphical display available (SSH or headless environment)")

// OpenInBrowser opens a URL in the user's default browser.
// Returns ErrHeadless if the environment is headless (SSH, no display).
// Silently returns nil when SKIP_BROWSER=1 (used by automated tests and
// demo scripts to suppress browser popups during non-interactive runs).
// This is the single entry point for browser-opening across the CLI.
func OpenInBrowser(url string) error {
	if os.Getenv("SKIP_BROWSER") == "1" {
		slog.Debug("browser open suppressed by SKIP_BROWSER=1", "url", url)
		return nil
	}
	if IsHeadless() {
		return ErrHeadless
	}
	return browser.OpenURL(url)
}

// SilentError is an error type that signals the error was already displayed.
// main.go checks for this and skips printing "Error:" prefix.
type SilentError struct{}

func (e SilentError) Error() string { return "" }

// ErrSilent is a sentinel error indicating output was already printed.
var ErrSilent = SilentError{}

// IsSilent checks if an error is a silent error (already displayed).
func IsSilent(err error) bool {
	var silentErr SilentError
	return errors.As(err, &silentErr)
}
