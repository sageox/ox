package githubmirror

import (
	"strings"
	"unicode/utf8"
)

const (
	htmlCommentOpen  = "<!--"
	htmlCommentClose = "-->"
)

// invisibleRanges are the code points that render as nothing (or reorder what
// is drawn) for a person reading GitHub but are still read by a model:
// zero-width and directional marks, bidi embeddings/overrides/isolates,
// invisible operators, the byte-order mark, filler characters, and the Unicode
// tag block — the usual carrier for "ASCII smuggling", where a whole hidden
// sentence is encoded as invisible tag characters. U+2065 is unassigned and
// U+206A-F are deprecated formatting controls, so they are not in it. Variation
// selectors are left alone: emoji need them and they cannot carry text.
var invisibleRanges = [...]struct{ lo, hi rune }{
	{0x00AD, 0x00AD},   // soft hyphen
	{0x115F, 0x1160},   // Hangul choseong/jungseong fillers
	{0x180E, 0x180E},   // Mongolian vowel separator
	{0x200B, 0x200F},   // zero-width space/joiners, LRM, RLM
	{0x202A, 0x202E},   // bidi embeddings and overrides
	{0x2060, 0x2064},   // word joiner, invisible operators
	{0x2066, 0x2069},   // bidi isolates
	{0x3164, 0x3164},   // Hangul filler
	{0xFEFF, 0xFEFF},   // byte-order mark / zero-width no-break space
	{0xFFA0, 0xFFA0},   // halfwidth Hangul filler
	{0xE0000, 0xE007F}, // Unicode tag characters
}

func isInvisible(r rune) bool {
	for _, span := range invisibleRanges {
		if r >= span.lo && r <= span.hi {
			return true
		}
	}
	return false
}

// Cleanup removes text GitHub hides from people but an AI coworker reads:
// HTML comments and invisible/bidi control characters. Each removed span is
// replaced by HiddenTextMarker. It returns the cleaned text and how many spans
// were removed. Deterministic; never "detects" injections — that is the
// server's scan.
//
// A span is one contiguous removed run: a comment, a run of adjacent invisible
// characters, or any mix of the two touching each other. One run is one marker
// and counts once, so the count reflects places a reader would notice
// something missing, not how many characters were hidden there. An
// unterminated "<!--" runs to the end of the text: renderers disagree on
// whether it hides anything, so the conservative reading is that all of it
// might be hidden. Everything else, including invalid UTF-8, is preserved byte
// for byte.
func Cleanup(s string) (cleaned string, removed int) {
	// fast path: most text has nothing to remove, and returning s unchanged
	// also guarantees byte-for-byte preservation there.
	if !strings.Contains(s, htmlCommentOpen) && strings.IndexFunc(s, isInvisible) < 0 {
		return s, 0
	}

	var b strings.Builder
	b.Grow(len(s))
	inRun := false
	removeSpan := func() {
		if !inRun {
			b.WriteString(HiddenTextMarker)
			removed++
			inRun = true
		}
	}

	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], htmlCommentOpen) {
			bodyStart := i + len(htmlCommentOpen)
			if end := strings.Index(s[bodyStart:], htmlCommentClose); end < 0 {
				i = len(s)
			} else {
				i = bodyStart + end + len(htmlCommentClose)
			}
			removeSpan()
			continue
		}

		// invalid bytes decode as (RuneError, 1), which is not invisible, so
		// they are copied through untouched.
		r, size := utf8.DecodeRuneInString(s[i:])
		if isInvisible(r) {
			removeSpan()
		} else {
			b.WriteString(s[i : i+size])
			inRun = false
		}
		i += size
	}
	return b.String(), removed
}
