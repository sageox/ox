package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestStripANSIEscapes verifies that terminal escape sequences and bare control
// bytes are removed from untrusted (LLM-generated) text while normal printable
// content, tabs, and newlines survive.
//
// Failure prevented: an adversarial subagent summary containing an OSC 52
// clipboard-write payload (\x1b]52;c;...\x07), a DCS sequence, or bare control
// bytes reaches the terminal and is interpreted, allowing escape-sequence
// injection (security finding #11).
func TestStripANSIEscapes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain text untouched",
			in:   "hello world",
			want: "hello world",
		},
		{
			name: "tab and newline survive",
			in:   "line1\n\tindented\n",
			want: "line1\n\tindented\n",
		},
		{
			name: "CSI color codes stripped",
			in:   "\x1b[31mred\x1b[0m text",
			want: "red text",
		},
		{
			name: "CSI cursor move stripped",
			in:   "a\x1b[2Jb",
			want: "ab",
		},
		{
			// the motivating attack: OSC 52 clipboard write terminated by BEL
			name: "OSC 52 clipboard payload terminated by BEL stripped",
			in:   "before\x1b]52;c;ZWNobyBwd25lZAo=\x07after",
			want: "beforeafter",
		},
		{
			// OSC terminated by ST (ESC backslash) instead of BEL
			name: "OSC payload terminated by ST stripped",
			in:   "x\x1b]0;malicious title\x1b\\y",
			want: "xy",
		},
		{
			name: "DCS sequence stripped",
			in:   "a\x1bP1;2;3mpayload\x1b\\b",
			want: "ab",
		},
		{
			name: "APC sequence stripped",
			in:   "a\x1b_some apc data\x1b\\b",
			want: "ab",
		},
		{
			name: "bare control bytes stripped, tab/newline kept",
			in:   "a\x00b\x07c\x1bd\te\nf\x7fg",
			// \x00 dropped, \x07 (BEL) dropped, ESC+'d' is a two-byte escape so
			// both drop, \t and \n kept, \x7f (DEL) dropped
			want: "abc\te\nfg",
		},
		{
			// 8-bit C1 introducers: 0x9b is CSI, 0x9d is OSC. A terminal acts on
			// these without a leading ESC, so they must be dropped too.
			name: "C1 control bytes stripped (8-bit CSI/OSC/ST)",
			in:   "a\u009bb\u009dc\u0080d",
			want: "abcd",
		},
		{
			name: "unicode passes through (U+00A0 and up survive)",
			in:   "café → résumé",
			want: "café → résumé",
		},
		{
			name: "lone trailing ESC dropped",
			in:   "text\x1b",
			want: "text",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := stripANSIEscapes(c.in)
			assert.Equal(t, c.want, got)
			// defense-in-depth: no ESC byte may survive in any output
			assert.False(t, strings.ContainsRune(got, '\x1b'), "ESC leaked into output: %q", got)
		})
	}
}
