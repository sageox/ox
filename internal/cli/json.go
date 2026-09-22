package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/theme"
)

// Colorized JSON output.
//
// A `--json` payload has two audiences with opposite needs. A human running
// `ox addons list --json` at a terminal is reading a document and wants the
// structure to pop. An AI coworker — or `jq`, or a test — is parsing bytes and
// must never receive an escape sequence.
//
// Both are served by the SAME call because the two audiences are already
// distinguishable without asking: an agent's stdout is a pipe, not a TTY, and
// theme's profile ladder (OX_COLOR_PROFILE > NO_COLOR > CLICOLOR_FORCE >
// colorprofile.Detect on os.Stdout) resolves a pipe to a no-color profile. So
// color appears exactly when a human is looking at it and cannot appear when
// anything is capturing it.
//
// That is what makes this safe: redirecting to a file, piping to jq, or reading
// it from an agent harness all take the no-TTY path, so the bytes stay valid
// JSON. Colorized JSON is never written to something that will parse it.
//
// Use PrintJSON for every `--json` payload. Hand-rolling
// json.NewEncoder(os.Stdout) in a command opts that command out of this, which
// is how ox ended up with 35 slightly different JSON emitters.

// The type palette.
//
// Every value type gets a DIFFERENT HUE, because hue is the only channel a
// reader parses at a glance. The first version of this mapped keys to
// ColorPrimary and strings to ColorSuccess — which are the same hex (#7AAA77)
// in the SageOx palette — so the two things you most need to tell apart were
// distinguished only by bold, and numbers (ColorAccent, #99C693) were a third
// near-identical green. Three of five token types rendered as the same color.
//
// The palette is sage-dominated by design, so the cool tokens do the
// separating work: blue keys follow the convention every developer already
// reads in jq, Chrome DevTools, and VS Code, and leave the brand green for the
// content itself.
//
// NOTE: ColorInfo and ColorPublic are borrowed here for a purpose they were
// not named for (ColorPublic is the visibility teal). Per design rule 9 that
// makes them missing tokens — a proper `ox-json-*` set belongs in
// sageox-design. Borrowing beats inlining a hex, which rule 3 forbids outright.
var (
	// jsonKeyStyle marks object keys — the scan lines a human follows. Blue,
	// the only cool hue in the palette, for maximum separation from values.
	jsonKeyStyle = lipgloss.NewStyle().Foreground(ColorInfo).Bold(true)
	// jsonStringStyle marks string values: brand sage, the content itself.
	jsonStringStyle = lipgloss.NewStyle().Foreground(ColorPrimary)
	// jsonNumberStyle marks numbers. Teal reads as neither key nor prose.
	jsonNumberStyle = lipgloss.NewStyle().Foreground(ColorPublic)
	// jsonBoolStyle marks true/false — gold, where a wrong value usually hides.
	jsonBoolStyle = lipgloss.NewStyle().Foreground(ColorWarning)
	// jsonNullStyle marks null. Dim and italic so absence LOOKS like absence
	// rather than like a value someone set.
	jsonNullStyle = lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
	// jsonPunctStyle recedes commas and colons: they carry no information the
	// layout does not already give (Tufte; design rule 11).
	jsonPunctStyle = lipgloss.NewStyle().Foreground(ColorDim)
)

// jsonDepthStyles tint braces and brackets by nesting depth — the terminal
// equivalent of editor bracket-pair colorization, and the cheapest way to see
// "which object am I inside" in a payload that is mostly arrays of objects.
//
// Deliberately neutral and sage-toned rather than a rainbow: structure is the
// FRAME, and the hues above are reserved for the data. A bracket that competed
// with a value for attention would cost more than the nesting cue is worth.
//
// This is byte-exact — it only colors braces the encoder already emitted.
var jsonDepthStyles = []lipgloss.Style{
	lipgloss.NewStyle().Foreground(ColorDim),
	lipgloss.NewStyle().Foreground(theme.Adapt(theme.ColorWordmarkSage)),
	lipgloss.NewStyle().Foreground(theme.Adapt(theme.ColorWordmarkOx)),
}

// PrintJSON writes v to stdout as indented JSON, colorized when — and only
// when — a human is looking at it.
//
// Returns nothing: a broken stdout is not actionable for a CLI, matching
// PrintSuccess and the errcheck exclusions for terminal output. Use
// PrintJSONTo when the caller can act on the error.
func PrintJSON(v any) {
	_ = PrintJSONTo(os.Stdout, v)
}

// PrintJSONTo writes v to w and reports an encoding or write failure, so a
// command can `return cli.PrintJSONTo(cmd.OutOrStdout(), payload)` in place of
// `return enc.Encode(payload)`.
//
// Color is decided by the process-wide profile (the TTY-ness of os.Stdout),
// not by w: w is usually cmd.OutOrStdout() or a test buffer standing in for
// stdout, and asking whether a *bytes.Buffer is a terminal has no answer. A
// test that wants color forces OX_COLOR_PROFILE.
func PrintJSONTo(w io.Writer, v any) error {
	encoded, err := MarshalJSONIndent(v)
	if err != nil {
		return err
	}
	return WriteJSONBytes(w, encoded)
}

// MarshalJSONIndent is the canonical encoding: two-space indent, one trailing
// newline, never colorized.
//
// For callers that need the bytes themselves — to measure the payload an AI
// coworker will actually consume, to embed it in a markdown fence, or to hash
// it. Pair it with WriteJSONBytes to print what you measured instead of
// encoding the value twice.
func MarshalJSONIndent(v any) ([]byte, error) {
	encoded, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode JSON: %w", err)
	}
	return append(encoded, '\n'), nil
}

// WriteJSONBytes writes already-encoded JSON to w, colorized under the same
// rule as PrintJSONTo.
//
// This is the seam for callers whose encoding is not the default: a
// SetEscapeHTML(false) encoder whose URLs must not become \u0026, or a command
// with a --pretty flag choosing between indented and compact. They keep their
// own encoder and still get themed output, instead of opting out of the
// printer entirely — which is how ox accumulated 100+ JSON emitters.
//
// b is written verbatim when color is off, so it must already end in whatever
// newline the caller wants.
func WriteJSONBytes(w io.Writer, b []byte) error {
	if !theme.ColorEnabled() {
		_, err := w.Write(b)
		return err
	}
	_, err := io.WriteString(w, colorizeJSON(string(b)))
	return err
}

// colorizeJSON styles an already-marshaled, already-indented JSON document.
//
// It lexes rather than pattern-matches: a string value containing `{` or `:`
// would defeat any regex, and json.MarshalIndent's output is a closed grammar
// where a five-state scanner is both shorter and exact. Anything unrecognized
// passes through verbatim, so a colorizer bug can lose color but never bytes.
func colorizeJSON(s string) string {
	var b strings.Builder
	b.Grow(len(s) * 2)

	depth := 0
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == '"':
			end := endOfJSONString(s, i)
			lit := s[i:end]
			// A string immediately followed by ':' is a key, not a value.
			if isJSONKey(s, end) {
				b.WriteString(jsonKeyStyle.Render(lit))
			} else {
				b.WriteString(jsonStringStyle.Render(lit))
			}
			i = end
		case c == '-' || (c >= '0' && c <= '9'):
			end := i
			for end < len(s) && strings.IndexByte("-+.eE0123456789", s[end]) >= 0 {
				end++
			}
			b.WriteString(jsonNumberStyle.Render(s[i:end]))
			i = end
		case strings.HasPrefix(s[i:], "true"):
			b.WriteString(jsonBoolStyle.Render(s[i : i+4]))
			i += 4
		case strings.HasPrefix(s[i:], "null"):
			b.WriteString(jsonNullStyle.Render(s[i : i+4]))
			i += 4
		case strings.HasPrefix(s[i:], "false"):
			b.WriteString(jsonBoolStyle.Render(s[i : i+5]))
			i += 5
		case c == '{' || c == '[':
			b.WriteString(depthStyle(depth).Render(string(c)))
			depth++
			i++
		case c == '}' || c == ']':
			// Decrement BEFORE rendering so a closer is tinted to match its
			// opener; tinting it at the inner depth would pair every brace
			// with the wrong one and make the cue actively misleading.
			if depth > 0 {
				depth--
			}
			b.WriteString(depthStyle(depth).Render(string(c)))
			i++
		case c == ',' || c == ':':
			b.WriteString(jsonPunctStyle.Render(string(c)))
			i++
		default:
			// whitespace and anything unexpected: emit unchanged
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// depthStyle returns the brace tint for a nesting level, cycling so arbitrarily
// deep payloads still pair visually.
func depthStyle(depth int) lipgloss.Style {
	return jsonDepthStyles[depth%len(jsonDepthStyles)]
}

// endOfJSONString returns the index just past the closing quote of the string
// starting at start, honoring backslash escapes so a `\"` inside a value does
// not end it early.
func endOfJSONString(s string, start int) int {
	for i := start + 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++ // skip the escaped byte, whatever it is
		case '"':
			return i + 1
		}
	}
	return len(s) // unterminated: treat the remainder as the literal
}

// isJSONKey reports whether the string ending at end is an object key, i.e. the
// next non-space byte is ':'.
func isJSONKey(s string, end int) bool {
	for i := end; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r':
			continue
		case ':':
			return true
		default:
			return false
		}
	}
	return false
}
