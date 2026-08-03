package cli

import "strings"

// SanitizeTerminalText makes server-provided or otherwise untrusted text
// safe to write to a terminal.
//
// Any string that originates outside this binary — a knowledge bubble's
// name/description/steering, a team name, an API error body — can carry
// ANSI CSI/OSC escape sequences. Written raw to a TTY those can repaint
// the screen, hide or forge output, retitle the window, or (with OSC 8
// and OSC 52) smuggle hyperlinks and clipboard writes past the user. The
// rendering call site is the only place that knows the value is about to
// hit a terminal, so sanitizing belongs here rather than at the API
// decode boundary.
//
// Behavior: escape sequences are dropped whole, in both their 7-bit
// (ESC-introduced) and 8-bit (C1 introducer) forms; remaining C0/C1
// control characters are dropped; printable text is preserved verbatim.
// Tab is preserved; newline and carriage return are NOT — a control
// character that ends the current line lets untrusted text escape the row
// it was rendered into, which is the specific forgery this guards
// against. Callers that legitimately need multi-line untrusted content
// should split on newlines first and sanitize each line.
func SanitizeTerminalText(s string) string {
	if s == "" {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))

	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]

		switch {
		case r == 0x1b: // ESC — 7-bit introducer, dispatch on the next rune
			i = skipEscapeSequence(runes, i)
			continue
		case r == 0x9b: // CSI, 8-bit form (equivalent to ESC [)
			i = skipCSI(runes, i+1)
			continue
		case r == 0x9d || r == 0x90 || r == 0x9f || r == 0x9e:
			// OSC / DCS / APC / PM, 8-bit forms — string-terminated
			i = skipStringSequence(runes, i+1)
			continue
		case r == '\t':
			b.WriteRune(r)
			continue
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			// remaining C0 controls, DEL, and unused C1 slots
			continue
		}

		b.WriteRune(r)
	}

	return b.String()
}

// SanitizeTerminalLines is the multi-line counterpart to
// SanitizeTerminalText, for untrusted values a caller wants to render
// across several rows instead of collapsing onto one.
//
// It splits on line breaks FIRST, then sanitizes each line independently,
// and drops lines that sanitize to nothing. Splitting first is what makes
// legitimate multi-line content readable while still denying the forgery
// SanitizeTerminalText guards against: the caller controls the indent of
// every emitted line, so untrusted text cannot start a line at column
// zero and pose as a label, a prompt, or a separate instruction.
//
// Returns nil when nothing survives, so callers can skip the row entirely.
func SanitizeTerminalLines(s string) []string {
	if s == "" {
		return nil
	}

	raw := strings.FieldsFunc(s, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\v' || r == '\f' || r == 0x85 || r == 0x2028 || r == 0x2029
	})

	out := make([]string, 0, len(raw))
	for _, line := range raw {
		if clean := strings.TrimRight(SanitizeTerminalText(line), " \t"); clean != "" {
			out = append(out, clean)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// skipEscapeSequence returns the index of the LAST rune of the ESC-
// introduced sequence starting at runes[i], so the caller's loop increment
// lands on the next ordinary rune.
func skipEscapeSequence(runes []rune, i int) int {
	if i+1 >= len(runes) {
		return len(runes) // lone trailing ESC
	}

	switch runes[i+1] {
	case '[':
		return skipCSI(runes, i+2)
	case ']', 'P', '_', '^': // OSC, DCS, APC, PM
		return skipStringSequence(runes, i+2)
	default:
		return i + 1 // two-character sequence (e.g. ESC c full reset)
	}
}

// skipCSI consumes a CSI parameter/intermediate run beginning at start and
// returns the index of its final byte (0x40-0x7e).
//
// An unterminated sequence consumes the remainder of the string:
// truncating is the safe failure mode, since emitting the tail would emit
// exactly the bytes we are trying to suppress.
func skipCSI(runes []rune, start int) int {
	for j := start; j < len(runes); j++ {
		if runes[j] >= 0x40 && runes[j] <= 0x7e {
			return j
		}
	}
	return len(runes)
}

// skipStringSequence consumes an OSC/DCS/APC/PM payload beginning at start
// and returns the index of its terminator — BEL, 7-bit ST (ESC \), or
// 8-bit ST (0x9c). Unterminated payloads consume the rest of the string,
// for the same fail-closed reason as skipCSI.
func skipStringSequence(runes []rune, start int) int {
	for j := start; j < len(runes); j++ {
		switch {
		case runes[j] == 0x07: // BEL
			return j
		case runes[j] == 0x1b && j+1 < len(runes) && runes[j+1] == '\\': // ST
			return j + 1
		case runes[j] == 0x9c: // 8-bit ST
			return j
		}
	}
	return len(runes)
}
