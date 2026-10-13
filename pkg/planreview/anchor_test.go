package planreview

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"
)

// TestAnchorVectors pins the anchor, label and markable-element algorithms to
// the shared vectors in testdata/anchors.json; the anchors are goldens from a
// JS engine running review.js. Failure prevented: the server computes anchors
// the page never produced, so every rebind misses and an edited plan orphans
// all its feedback.
func TestAnchorVectors(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/anchors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Anchors []struct{ Heading, Text, Anchor string }
		Labels  []struct{ Text, Label string }
		Page    struct {
			HTML    string
			Targets []struct{ Anchor, Section, Label string }
		}
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Anchors {
		if got := AnchorFor(c.Heading, c.Text); got != c.Anchor {
			t.Errorf("AnchorFor(%q, %q) = %s, want %s", c.Heading, c.Text, got, c.Anchor)
		}
	}
	for _, c := range v.Labels {
		if got := jsLabel(c.Text); got != c.Label {
			t.Errorf("label(%q) = %q, want %q", c.Text, got, c.Label)
		}
	}
	targets, err := extractTargets([]byte(v.Page.HTML))
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != len(v.Page.Targets) {
		t.Fatalf("%d markable elements, want %d: %+v", len(targets), len(v.Page.Targets), targets)
	}
	for i, w := range v.Page.Targets {
		if g := targets[i]; g.Anchor != w.Anchor || g.Section != w.Section || g.Label != w.Label {
			t.Errorf("target %d = %+v, want %+v", i, g, w)
		}
	}
}

// TestRebind_MovesAMarkOnlyWhenTheMoveIsUnambiguous pins the rebind rules: an
// exact label match follows a reworded heading, identical text is rebound only
// within the mark's own section, a fuzzy match needs a strong score and a
// clear margin, and highlights, closed items and blank labels never move.
// Failure prevented: an edit orphans an unaddressed mark, or attaches a
// reviewer's words to the wrong element.
func TestRebind_MovesAMarkOnlyWhenTheMoveIsUnambiguous(t *testing.T) {
	t.Parallel()
	page := `<section id="a"><h2>Rollout Plan</h2><ul><li>Ship the CLI first</li><li>TBD budget</li></ul></section>` +
		`<section id="b"><h2>Beta</h2><ul><li>TBD budget</li><li>ship the daemon first now</li><li>ship the daemon first soon</li></ul></section>` +
		`<section id="c"><h2>Gamma</h2><ul><li>migrate the ledger store before the cutover window</li></ul></section>`
	mark := func(anchor, section, label string) Merged {
		return Merged{Item: Item{Anchor: anchor, Section: section, Label: label}, Open: true}
	}
	for _, c := range []struct {
		name   string
		item   Merged
		to     string
		method string
	}{
		{"heading reworded", mark(AnchorFor("Rollout", "Ship the CLI first"), "Rollout", "Ship the CLI first"), AnchorFor("Rollout Plan", "Ship the CLI first"), "label-exact"},
		{"identical text, own section survives", mark("hdead0001", "Beta", "TBD budget"), AnchorFor("Beta", "TBD budget"), "label-exact"},
		{"identical text, own section gone", mark("hdead0002", "Deleted", "TBD budget"), "", ""},
		{"reflowed sentence", mark("hdead0003", "Gamma", "migrate the ledger store before cutover"), AnchorFor("Gamma", "migrate the ledger store before the cutover window"), "label-fuzzy"},
		{"two near-identical candidates", mark("hdead0004", "Elsewhere", "ship the daemon first"), "", ""},
		{"nothing similar", mark("hdead0005", "Rollout", "entirely unrelated words"), "", ""},
		{"blank label", mark("hdead0006", "Rollout", "   "), "", ""},
		{"a highlight", mark("q5e1f00ba", "Rollout", "Ship the CLI first"), "", ""},
		{"a closed item", Merged{Item: Item{Anchor: "hdead0007", Label: "Ship the CLI first"}}, "", ""},
		{"still anchored", mark(AnchorFor("Rollout Plan", "Ship the CLI first"), "Rollout Plan", "Ship the CLI first"), "", ""},
	} {
		got, err := Rebind([]Merged{c.item}, []byte(page), at(5))
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case c.to == "" && len(got) != 0:
			t.Errorf("%s: rebound to %+v, want it left where it is", c.name, got)
		case c.to != "" && (len(got) != 1 || got[0].To != c.to || got[0].Method != c.method || got[0].From != c.item.Anchor || !got[0].At.Equal(at(5))):
			t.Errorf("%s: Rebind = %+v, want %s -> %s by %s", c.name, got, c.item.Anchor, c.to, c.method)
		}
	}
}

// TestCanonical_FoldsMovesInOrderInOnePass pins remap resolution as one
// chronological fold, run once for every anchor. Failure prevented: a hostile
// or repeated remap drags a mark that already moved, or a crafted remaps.json
// against a large round hangs every read of the plan (folding each anchor on
// its own costs anchors×remaps).
func TestCanonical_FoldsMovesInOrderInOnePass(t *testing.T) {
	t.Parallel()
	canon := canonical([]string{"ha", "hx", "hq", "hy"}, []Remap{
		{From: "ha", To: "ha"}, // self-loop
		{From: "zz", To: "yy"}, // unknown from
		{From: "ha", To: "hb"},
		{From: "ha", To: "hc"}, // later re-route of an old From: the mark already moved on
		{From: "hx", To: "hy"}, // hx joins hy...
		{From: "hy", To: "hx"}, // ...and both move back together
		{From: "hb", To: ""},   // a remap without a target moves nothing
	})
	if got, want := []string{canon["ha"], canon["hx"], canon["hq"], canon["hy"]}, []string{"hb", "hx", "hq", "hx"}; !slices.Equal(got, want) {
		t.Errorf("resolved %v, want %v", got, want)
	}

	// 100,000 anchors gathered onto one position, then 100,000 moves of it:
	// milliseconds folded together, about half a minute folded one by one.
	var anchors []string
	var remaps []Remap
	for i := range 100_000 {
		anchors = append(anchors, fmt.Sprintf("h%d", i))
		remaps = append(remaps, Remap{From: anchors[i], To: "hub0"})
	}
	for i := range 100_000 {
		remaps = append(remaps, Remap{From: fmt.Sprintf("hub%d", i%2), To: fmt.Sprintf("hub%d", (i+1)%2)})
	}
	done := make(chan map[string]string, 1)
	go func() { done <- canonical(anchors, remaps) }()
	select {
	case got := <-done:
		if got["h0"] != "hub0" || got["h99999"] != "hub0" {
			t.Errorf("h0 -> %s, h99999 -> %s, want both at hub0", got["h0"], got["h99999"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canonical folded each anchor on its own: anchors×remaps")
	}
}
