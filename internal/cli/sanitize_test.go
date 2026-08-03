package cli

import (
	"strings"
	"testing"
)

// TestSanitizeTerminalText covers the class of failure this guards: any
// untrusted string reaching a TTY. Cases are grouped by the capability an
// attacker would be reaching for, not by escape-sequence syntax — a new
// bypass in any of these groups is the same bug.
//
// Failure prevented: a hostile knowledge bubble (or compromised server)
// repainting the screen, forging a row, retitling the window, or writing
// the user's clipboard when they run a read-only inspect command.
func TestSanitizeTerminalText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// --- A. benign content survives untouched ---
		{"plain text", "deploy tooling and incident retros", "deploy tooling and incident retros"},
		{"empty", "", ""},
		{"unicode preserved", "café — naïve 日本語", "café — naïve 日本語"},
		{"tab preserved", "a\tb", "a\tb"},
		{"bare bracket is not CSI", "array[0] and x[i]", "array[0] and x[i]"},

		// --- B. screen repainting / style forgery (CSI) ---
		{"color codes stripped", "\x1b[31mred\x1b[0m", "red"},
		{"cursor move stripped", "\x1b[2J\x1b[Hwiped", "wiped"},
		{"multi-param CSI", "\x1b[1;32;40mstyled\x1b[m", "styled"},
		// C1 introducers as they actually arrive from a UTF-8 source
		// (\u009b == 0xC2 0x9B on the wire), not as a raw \x9b byte.
		{"8-bit CSI stripped", "\u009b31mred", "red"},
		{"8-bit OSC stripped", "\u009d0;pwned\x07title", "title"},
		// A raw 0x9b byte is invalid UTF-8, so decoding yields U+FFFD
		// before we ever see it. That is already safe — the terminal
		// receives a replacement char, never a live CSI introducer.
		{"raw invalid C1 byte degrades to U+FFFD", "\x9b31mred", "�31mred"},

		// --- C. row forgery via line control ---
		{"newline dropped", "real\nfake:            forged", "realfake:            forged"},
		{"carriage return dropped", "visible\rhidden", "visiblehidden"},
		{"backspace dropped", "abc\x08\x08\x08xyz", "abcxyz"},

		// --- D. OSC family: window title, hyperlink, clipboard ---
		{"OSC BEL-terminated", "\x1b]0;pwned\x07title", "title"},
		{"OSC ST-terminated", "\x1b]0;pwned\x1b\\title", "title"},
		{"OSC 8 hyperlink", "\x1b]8;;https://evil.example\x07click\x1b]8;;\x07", "click"},
		{"OSC 52 clipboard", "\x1b]52;c;ZXZpbA==\x07ok", "ok"},
		{"DCS stripped", "\x1bPq#0;2;0;0;0\x1b\\after", "after"},

		// --- E. malformed input fails closed ---
		{"lone trailing ESC", "text\x1b", "text"},
		{"unterminated CSI consumes tail", "text\x1b[38;5;", "text"},
		{"unterminated OSC consumes tail", "text\x1b]0;no-terminator", "text"},
		{"two-char sequence: full reset", "a\x1bcb", "ab"},
		{"nothing but escapes", "\x1b[31m\x1b[0m", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeTerminalText(tt.in); got != tt.want {
				t.Errorf("SanitizeTerminalText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestSanitizeTerminalText_NoESCSurvives is the invariant behind every case
// above, asserted directly: whatever the input, no ESC or C1 introducer may
// reach the terminal. Catches a future edit that adds a passthrough branch.
func TestSanitizeTerminalText_NoESCSurvives(t *testing.T) {
	inputs := []string{
		"\x1b[31mred", "\x1b]0;t\x07", "\x1b", "\x1b\x1b\x1b[0m",
		"\u009b1m", "\u009d0;t\x07", "\u00901;2\u009c",
		"mixed \x1b[1m text \x1b]8;;u\x07 more", "\x1bPdata\x1b\\",
	}
	for _, in := range inputs {
		got := SanitizeTerminalText(in)
		for _, r := range got {
			if r == 0x1b || (r >= 0x80 && r <= 0x9f) {
				t.Errorf("SanitizeTerminalText(%q) = %q — leaked control introducer %#x", in, got, r)
			}
		}
	}
}

// --- SanitizeTerminalLines: multi-line untrusted values ---

// TestSanitizeTerminalLines verifies untrusted multi-line text is split
// into caller-indentable lines instead of collapsed or passed through.
//
// Failure prevented: a multi-line value (steering prompts are multi-line
// in practice) whose continuation lines start at column zero, where they
// read as a new label, a shell prompt, or a standalone instruction rather
// than as part of the row they belong to.
func TestSanitizeTerminalLines(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single line", "focus on deploys", []string{"focus on deploys"}},
		{"lf split", "line one\nline two", []string{"line one", "line two"}},
		{"crlf split", "line one\r\nline two", []string{"line one", "line two"}},
		{"blank lines dropped", "a\n\n\nb", []string{"a", "b"}},
		{"whitespace-only line dropped", "a\n   \nb", []string{"a", "b"}},
		{"trailing whitespace trimmed", "a   \nb\t", []string{"a", "b"}},
		{"escapes stripped per line", "\x1b[31ma\n\x1b]0;t\x07b", []string{"a", "b"}},
		{"line that sanitizes to nothing is dropped", "a\n\x1b[31m\x1b[0m\nb", []string{"a", "b"}},
		{"all lines empty yields nil", "\n\n   \n", nil},
		{"unicode line separators split", "a\u2028b\u2029c", []string{"a", "b", "c"}},
		{"vertical tab and form feed split", "a\vb\fc", []string{"a", "b", "c"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeTerminalLines(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("SanitizeTerminalLines(%q) = %q, want %q", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("line %d: got %q, want %q (full: %q)", i, got[i], tt.want[i], got)
				}
			}
		})
	}
}

// TestSanitizeTerminalLines_NoLineBreaksSurvive is the invariant the
// caller's indentation depends on: no returned line may itself contain a
// break, or the caller's per-line indent is bypassed and the forgery is
// back.
func TestSanitizeTerminalLines_NoLineBreaksSurvive(t *testing.T) {
	inputs := []string{
		"a\nb", "a\r\nb", "a\rb", "a\vb\fc", "a\u2028b",
		"\x1b[31ma\nb\x1b[0m", "steer\n\nmore\n",
	}
	for _, in := range inputs {
		for _, line := range SanitizeTerminalLines(in) {
			if strings.ContainsAny(line, "\n\r\v\f") {
				t.Errorf("SanitizeTerminalLines(%q) returned a line with an embedded break: %q", in, line)
			}
		}
	}
}
