package planreview

import (
	"strings"
	"time"
)

// A reviewer's mark must survive the plan being edited. Anchors hash content,
// so rewriting the text a mark sits on orphans it: by design when the author
// addressed the mark, but not when the author only reworded a heading or
// reflowed a list. When the author saves a new render, Rebind finds each open
// mark whose anchor vanished and the element it most plausibly moved to; the
// author records each move as a Remap, and Assemble follows the moves at read
// time. Rounds are never rewritten, and a mark that cannot be rebound stays
// open and visible at its old anchor.

// Remap thresholds: a fuzzy rebind must be strong and unambiguous. A wrong
// rebind attaches a reviewer's words to the wrong element, which is worse than
// leaving the mark visibly orphaned.
const (
	remapFuzzyMin    = 0.72 // minimum token-Dice similarity to rebind
	remapFuzzyMargin = 0.08 // best must beat the runner-up by this much
)

// canonical maps each of anchors to its current address. Remaps are
// chronological moves ("whatever lived at From moved to To"), so resolution is
// one ordered fold that follows a hop only when it departs from an anchor's
// current position. A from→to map walked as a chain would be path-dependent
// (a later entry re-routing an old From would drag marks that had already
// moved on) and could loop.
//
// The fold runs once for all anchors: anchors at one position move together,
// and the smaller group joins the larger, so the cost is remaps plus
// anchors×log(anchors). Folding each anchor on its own costs anchors×remaps,
// minutes for a one-megabyte round against a one-megabyte remaps.json.
func canonical(anchors []string, remaps []Remap) map[string]string {
	at := map[string][]string{} // position -> the anchors now there
	for _, a := range anchors {
		at[a] = []string{a}
	}
	for _, m := range remaps {
		group, ok := at[m.From]
		if !ok || m.To == "" || m.To == m.From {
			continue
		}
		delete(at, m.From)
		into := at[m.To]
		if len(into) < len(group) {
			into, group = group, into
		}
		at[m.To] = append(into, group...)
	}
	out := make(map[string]string, len(anchors))
	for pos, group := range at {
		for _, a := range group {
			out[a] = pos
		}
	}
	return out
}

// Rebind computes the remaps a new render of the plan needs: for each open
// item whose anchor page no longer has, the element it most plausibly moved
// to. Highlights ("q" anchors) are never rebound, because the page finds them
// by their words. Rebind writes nothing; only the plan's author records its
// result (RemapRecord), never a submission.
func Rebind(items []Merged, page []byte, at time.Time) ([]Remap, error) {
	var open []Merged
	for _, it := range items {
		if it.Open && it.Anchor != "" && !strings.HasPrefix(it.Anchor, "q") {
			open = append(open, it)
		}
	}
	if len(open) == 0 {
		return nil, nil
	}
	targets, err := extractTargets(page)
	if err != nil {
		return nil, err
	}
	live := make(map[string]bool, len(targets))
	for _, t := range targets {
		live[t.Anchor] = true
	}
	var out []Remap
	for _, it := range open {
		if live[it.Anchor] {
			continue
		}
		if m, ok := bestRebind(it, targets); ok {
			m.At = at.UTC()
			out = append(out, m)
		}
	}
	return out, nil
}

// bestRebind finds the element an orphaned item most plausibly moved to. An
// exact label match (case and whitespace aside) is the "heading reworded,
// content untouched" case, but it must be unambiguous: among identical texts
// in several sections only the one in the item's own section may win, and
// with no such disambiguator the rebind is refused. A fuzzy match also needs
// a strong score and a clear margin over the runner-up.
func bestRebind(it Merged, targets []target) (Remap, bool) {
	normLabel := jsNorm(it.Label)
	if normLabel == "" {
		return Remap{}, false
	}
	to := func(t target, method string, score float64) Remap {
		return Remap{From: it.Anchor, To: t.Anchor, Section: t.Section, Label: t.Label, Method: method, Score: score}
	}

	// Identical text under one heading is one anchor, so dedupe by anchor
	// before disambiguating across sections.
	exact := map[string]target{}
	for _, t := range targets {
		if jsNorm(t.Label) == normLabel {
			exact[t.Anchor] = t
		}
	}
	if len(exact) == 1 {
		for _, t := range exact {
			return to(t, "label-exact", 1), true
		}
	}
	if len(exact) > 1 {
		var inSection []target
		for _, t := range exact {
			if it.Section != "" && jsNorm(t.Section) == jsNorm(it.Section) {
				inSection = append(inSection, t)
			}
		}
		if len(inSection) == 1 {
			return to(inSection[0], "label-exact", 1), true
		}
		return Remap{}, false
	}

	var best, second float64
	var bestT target
	for _, t := range targets {
		s := diceTokens(normLabel, t.Norm)
		// prefer a same-section candidate on near ties: content rarely jumps
		// sections silently
		if it.Section != "" && jsNorm(it.Section) == jsNorm(t.Section) {
			s += 0.05
		}
		if s > best {
			second = best
			best, bestT = s, t
		} else if s > second {
			second = s
		}
	}
	if best >= remapFuzzyMin && best-second >= remapFuzzyMargin {
		return to(bestT, "label-fuzzy", best), true
	}
	return Remap{}, false
}

// diceTokens is the Sørensen–Dice coefficient over the strings' unique token
// sets: order-insensitive and robust to small edits, the shape of a reflowed
// sentence.
func diceTokens(a, b string) float64 {
	ta, tb := tokenSet(a), tokenSet(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	inter := 0
	for t := range ta {
		if tb[t] {
			inter++
		}
	}
	return 2 * float64(inter) / float64(len(ta)+len(tb))
}

func tokenSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.Fields(s) {
		if f = strings.Trim(f, ".,;:!?()[]{}\"'`…—-"); f != "" {
			out[f] = true
		}
	}
	return out
}
