package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// streamStrip feeds text to an ansiStripper in the given chunks.
func streamStrip(t *testing.T, chunks ...string) string {
	t.Helper()
	var out bytes.Buffer
	s := &ansiStripper{w: &out}
	for _, c := range chunks {
		n, err := s.Write([]byte(c))
		require.NoError(t, err)
		require.Equal(t, len(c), n, "Write reports the caller's length")
	}
	return out.String()
}

// However the output is cut into Writes (io.Copy hands the stripper 32 KiB
// chunks, so any long output gets cut somewhere), the result must equal
// stripping the whole text at once. Every cut point of every case is tried.
func TestANSIStripperSplitWritesMatchWholeStrip(t *testing.T) {
	t.Parallel()
	longURL := "https://example.com/" + strings.Repeat("a", 300)
	for name, text := range map[string]string{
		"3-byte character (the release-notes …)": "dirs (`docs/adr`, …) done",
		"2-byte character":                       "café au lait",
		"4-byte character":                       "ship it \U0001F680 now",
		"color around a character":               "\x1b[1mbold …\x1b[0m tail",
		"OSC hyperlink longer than 256 bytes":    "see \x1b]8;;" + longURL + "\x1b\\label\x1b]8;;\x1b\\ end",
		"OSC ended by BEL":                       "a\x1b]0;window title\x07b",
		"DCS string":                             "x\x1bPqabc\x1b\\ok",
		"ESC ( charset selection":                "\x1b(Btext",
	} {
		want := ansi.Strip(text)
		for cut := 0; cut <= len(text); cut++ {
			assert.Equal(t, want, streamStrip(t, text[:cut], text[cut:]), "%s cut at byte %d", name, cut)
		}
	}
}

// One byte per Write is the worst case for state carried between Writes.
func TestANSIStripperByteAtATime(t *testing.T) {
	t.Parallel()
	text := "a…b\U0001F680cé \x1b[31mred\x1b[0m \x1b]8;;https://x.test\x1b\\link\x1b]8;;\x1b\\ \x1bPqdcs\x1b\\z"
	chunks := make([]string, len(text))
	for i := 0; i < len(text); i++ {
		chunks[i] = text[i : i+1]
	}
	assert.Equal(t, ansi.Strip(text), streamStrip(t, chunks...))
	assert.Equal(t, "a…b\U0001F680cé red link z", streamStrip(t, chunks...))
}

// Bytes of a character still unfinished when the input ends pass through
// unchanged: nothing is held back, replaced or dropped.
func TestANSIStripperUnfinishedTailPassesThrough(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []byte("end \xe2\x80"), []byte(streamStrip(t, "end \xe2\x80")))
}

// utf8ByteLen must agree with the lead-byte ranges ansi.Strip uses; -1 marks
// a byte that cannot start a sequence (a stray continuation or 0xf8+).
func TestUTF8ByteLen(t *testing.T) {
	t.Parallel()
	for b, want := range map[byte]int{
		'a': 1, 0x7f: 1, 0xc3: 2, 0xdf: 2, 0xe2: 3, 0xef: 3, 0xf0: 4, 0xf7: 4,
		0x80: -1, 0xbf: -1, 0xf8: -1, 0xff: -1,
	} {
		assert.Equal(t, want, utf8ByteLen(b), "byte %#x", b)
	}
}
