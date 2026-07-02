package plan

// remap.go — review feedback is SACRED: a human's marks must survive the plan
// being edited and re-rendered. Anchors are content hashes (heading + element
// text), so an agent addressing an item usually rewrites the text and the
// anchor dies — that is the designed "addressed" signal. But an agent can also
// rewrite text the reviewer commented on WITHOUT addressing the comment
// (rewording a heading, reflowing a list), which used to orphan the mark
// silently on the page.
//
// RemapFeedback runs at plan-save time (the only moment plan content actually
// changes on disk): it parses the fresh render, finds every OPEN item whose
// anchor no longer exists, and tries to re-anchor it onto the element that
// most plausibly carries the same content — exact label match first, then a
// conservative token-overlap match. Each rebind is APPENDED to
// feedback/remaps.json (never mutating the human's original rounds), and
// AssembleReview follows the remap chain when merging, so digests, renders,
// and `ox plan feedback resolve` all see the item at its new address. Items
// that can't be confidently rebound stay open under their original anchor —
// still listed in every digest and surfaced by the page's orphan bar. Nothing
// is ever dropped.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/fileutil"
)

const remapsFile = "remaps.json"

// Remap thresholds: a fuzzy rebind must be both strong and unambiguous.
// Conservative on purpose — a wrong rebind attaches a human's words to the
// wrong element, which is worse than leaving the item visibly orphaned.
const (
	remapFuzzyMin    = 0.72 // minimum token-Dice similarity to rebind
	remapFuzzyMargin = 0.08 // best must beat runner-up by this much
)

// RemapEntry records one anchor rebind. Append-only, alongside rounds and
// resolutions, so the full history of where a mark lived stays in the ledger.
type RemapEntry struct {
	From    string    `json:"from"`              // anchor that vanished from the render
	To      string    `json:"to"`                // anchor of the element it was rebound to
	Section string    `json:"section,omitempty"` // new element's section heading
	Label   string    `json:"label,omitempty"`   // new element's label
	Method  string    `json:"method"`            // label-exact | label-fuzzy
	Score   float64   `json:"score"`             // similarity that justified the rebind
	At      time.Time `json:"at"`
}

// LoadRemaps reads the append log of anchor rebinds. Missing file is empty.
func LoadRemaps(planDir string) ([]RemapEntry, error) {
	b, err := os.ReadFile(filepath.Join(planDir, feedbackSubdir, remapsFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read remaps: %w", err)
	}
	var rs []RemapEntry
	if err := json.Unmarshal(b, &rs); err != nil {
		return nil, fmt.Errorf("parse remaps: %w", err)
	}
	return rs, nil
}

// appendRemaps appends entries to remaps.json under the same advisory flock
// discipline as resolutions — two concurrent writers must not clobber. An
// unparseable existing file is RESET (loudly): remaps are derived, re-derivable
// state, and one corrupt byte must not disable remapping for the plan forever.
// The sacred inputs (rounds, resolutions) are never touched here.
func appendRemaps(planDir string, entries []RemapEntry) error {
	if len(entries) == 0 {
		return nil
	}
	dir := filepath.Join(planDir, feedbackSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create feedback dir: %w", err)
	}
	path := filepath.Join(dir, remapsFile)
	return fileutil.WithFileLock(context.Background(), path, func() error {
		existing, err := LoadRemaps(planDir)
		if err != nil {
			slog.Warn("plan feedback: remaps.json unreadable — resetting derived remap state", "error", err, "dir", planDir)
			existing = nil
		}
		existing = append(existing, entries...)
		b, err := json.MarshalIndent(existing, "", "  ")
		if err != nil {
			return fmt.Errorf("encode remaps: %w", err)
		}
		return fileutil.AtomicWriteBytes(path, b, 0o644)
	})
}

// remapResolver returns a canonicalizer mapping an anchor to its current
// address. Entries are CHRONOLOGICAL MOVES ("whatever lived at From moved to
// To"), so resolution is a single ordered fold: walk the entries once,
// following a hop only when it departs from the anchor's CURRENT position.
// This is deliberately not a from→to map + chain-walk — that form is
// path-dependent (a later crafted entry re-routing an old From would
// retroactively drag marks that had already moved elsewhere; the model
// battery caught exactly that) and can loop. The fold terminates in one pass
// by construction and gives every anchor a stable position.
func remapResolver(entries []RemapEntry) func(string) string {
	if len(entries) == 0 {
		return func(a string) string { return a }
	}
	return func(a string) string {
		cur := a
		for _, e := range entries {
			if e.From == cur && e.To != "" && e.To != cur {
				cur = e.To
			}
		}
		return cur
	}
}

// RemapFeedback re-anchors open review items onto a freshly rendered plan.
// html is the new render (the same bytes being saved as plan.html). Returns
// the rebinds it recorded; an empty slice means every open item still anchors
// (or nothing was confidently rebindable). Fail-soft by design: called from
// Save, where feedback durability must never block the plan write itself.
func RemapFeedback(planDir string, htmlBytes []byte, now time.Time) ([]RemapEntry, error) {
	items, err := AssembleReview(planDir)
	if err != nil {
		return nil, err
	}
	var open []MergedItem
	for _, it := range items {
		if it.Open {
			open = append(open, it)
		}
	}
	if len(open) == 0 {
		return nil, nil
	}

	targets, err := extractReviewTargets(htmlBytes)
	if err != nil {
		return nil, err
	}
	live := make(map[string]bool, len(targets))
	for _, t := range targets {
		live[t.Anchor] = true
	}

	var entries []RemapEntry
	for _, it := range open {
		if it.Anchor == "" || live[it.Anchor] {
			continue // still anchored — nothing to do
		}
		if e, ok := bestRebind(it, targets); ok {
			e.At = now.UTC()
			entries = append(entries, e)
		}
	}
	if err := appendRemaps(planDir, entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// bestRebind finds the element an orphaned item most plausibly moved to.
// Exact label equality (case/whitespace-insensitive) is the "heading reworded,
// content untouched" case — but even exact matches must be UNAMBIGUOUS: with
// identical text in several sections, only the stored-section copy may win,
// and with no such disambiguator the rebind is refused (a mark attached to the
// wrong section's identical bullet is misattached sacred data, worse than a
// visible orphan). Fuzzy matches additionally need a strong score and a clear
// margin over the runner-up.
func bestRebind(it MergedItem, targets []reviewTarget) (RemapEntry, bool) {
	normLabel := jsNorm(it.Label)
	if normLabel == "" {
		return RemapEntry{}, false
	}
	entryFor := func(t reviewTarget, method string, score float64) RemapEntry {
		return RemapEntry{From: it.Anchor, To: t.Anchor, Section: t.Section, Label: t.Label, Method: method, Score: score}
	}

	// exact pass: dedupe by anchor (identical text under one heading is one
	// anchor), then disambiguate across sections.
	exact := map[string]reviewTarget{}
	for _, t := range targets {
		if jsNorm(t.Label) == normLabel {
			exact[t.Anchor] = t
		}
	}
	if len(exact) == 1 {
		for _, t := range exact {
			return entryFor(t, "label-exact", 1), true
		}
	}
	if len(exact) > 1 {
		var inSection []reviewTarget
		for _, t := range exact {
			if it.Section != "" && jsNorm(t.Section) == jsNorm(it.Section) {
				inSection = append(inSection, t)
			}
		}
		if len(inSection) == 1 {
			return entryFor(inSection[0], "label-exact", 1), true
		}
		return RemapEntry{}, false // ambiguous duplicates — refuse, stay a visible orphan
	}

	var best, second float64
	var bestT reviewTarget
	for _, t := range targets {
		s := diceTokens(normLabel, t.Norm)
		// prefer a same-section candidate on near ties: the reviewer's mark
		// names its section, and content rarely jumps sections silently.
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
		return entryFor(bestT, "label-fuzzy", best), true
	}
	return RemapEntry{}, false
}

// diceTokens is the Sørensen–Dice coefficient over the two strings' unique
// token sets — cheap, order-insensitive, and robust to small edits, which is
// exactly the "agent reflowed this sentence" shape we rebind across.
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
		f = strings.Trim(f, ".,;:!?()[]{}\"'`…—-")
		if f != "" {
			out[f] = true
		}
	}
	return out
}
