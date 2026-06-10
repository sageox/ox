package plan

import (
	"strings"
	"testing"
)

// TestReviewJS_AnchorIgnoresReviewGlyph verifies the review asset hashes the
// underlying content, not the glyphs it injects during paint. Failure prevented:
// re-clicking an already-marked item computes a different anchor and creates an
// unreachable duplicate mark instead of editing the existing one.
func TestReviewJS_AnchorIgnoresReviewGlyph(t *testing.T) {
	b, err := renderAssets.ReadFile("assets/review.js")
	if err != nil {
		t.Fatalf("read review.js: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		"function anchorText(el)",
		"clone.querySelectorAll('.rev-glyph')",
		"norm(anchorText(el))",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("review.js missing %q", want)
		}
	}
}
