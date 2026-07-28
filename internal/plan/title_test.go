package plan

import "testing"

// TestTitle covers the H1-first derivation ox-1tjj.8 fixed: Parse splits
// markdown on H2 ("## ") only, so a naive walk of Input.Sections looking for
// the first non-empty Heading returns the first H2 — never the H1, which
// lives in the (Heading=="") preamble section. These cases are the two
// real-world regressions that shipped: a plan whose H1 is a genuine title
// followed by a "1. Context — Why Now" or "TL;DR" H2 rendered correctly
// (RenderHTMLOpts already used this H1-first logic) but saved to the ledger
// under the H2's text instead.
func TestTitle(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "H1 followed by a numbered context H2 — the ox-1tjj.8 regression",
			raw: "# Conversation model update — the execution plan\n\n" +
				"## 1. Context — Why Now\n\nSome framing prose.\n\n" +
				"## Approach\n\nDo the thing.\n",
			want: "Conversation model update — the execution plan",
		},
		{
			name: "H1 followed by a TL;DR H2 — the second ox-1tjj.8 regression",
			raw: "# ox plan — Team-Context-Enriched Plans\n\n" +
				"## TL;DR\n\nShip it.\n\n" +
				"## Design\n\nDetails.\n",
			want: "ox plan — Team-Context-Enriched Plans",
		},
		{
			name: "H1 with no H2 sections at all",
			raw:  "# Just A Title\n\nNo sections here, just prose.\n",
			want: "Just A Title",
		},
		{
			name: "H1 with surrounding whitespace is trimmed",
			raw:  "#    Padded Title   \n\nbody\n",
			want: "Padded Title",
		},
		{
			name: "no H1 — first H2 heading wins",
			raw:  "Some preamble with no heading.\n\n## First Section\n\nbody\n",
			want: "First Section",
		},
		{
			name: "no headings at all falls back",
			raw:  "Just a paragraph, no heading anywhere.\n",
			want: "Implementation Plan",
		},
		{
			name: "empty input falls back",
			raw:  "",
			want: "Implementation Plan",
		},
		{
			name: "H2 before the H1 in raw text is still not chosen (H1 always wins)",
			raw:  "## Not The Title\n\nprose\n\n# The Real Title\n\nmore prose\n",
			want: "The Real Title",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := Parse(tc.raw)
			if got := Title(in); got != tc.want {
				t.Errorf("Title(Parse(%q)) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
