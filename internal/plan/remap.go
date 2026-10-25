package plan

// remap.go — review feedback is SACRED: a human's marks must survive the plan
// being edited and re-rendered. Anchors are content hashes (heading + element
// text), so an agent addressing an item usually rewrites the text and the
// anchor dies — that is the designed "addressed" signal. But an agent can also
// rewrite text the reviewer commented on WITHOUT addressing the comment
// (rewording a heading, reflowing a list), which would orphan the mark.
//
// RemapFeedback runs at plan-save time (the only moment plan content actually
// changes on disk): planreview.Rebind finds every OPEN item whose anchor no
// longer exists in the fresh render and the element that most plausibly
// carries the same content. Each move is published as its own immutable record
// under feedback/remaps/ (never mutating the human's rounds or a shared file),
// and assembly follows the moves at read time, so digests, renders, and
// `ox plan feedback resolve` all see the item at its new address. Items that
// can't be confidently rebound stay open under their original anchor — still
// listed in every digest and surfaced by the page's orphan bar.

import (
	"fmt"
	"time"

	"github.com/sageox/ox/pkg/planreview"
)

// RemapFeedback re-anchors open review items onto a freshly rendered plan.
// html is the new render (the same bytes being saved as plan.html). Returns
// the moves it recorded. It records none from an incomplete read: an item
// whose resolution or later round could not be read would be rebound as if it
// were open, attaching the reviewer's words to another element for good.
func RemapFeedback(planDir string, html []byte, now time.Time) ([]planreview.Remap, error) {
	rev := assembleReview(planDir)
	if !rev.Complete() {
		return nil, fmt.Errorf("review could not be fully read (%d problem(s)): remap skipped", len(rev.Failures))
	}
	remaps, err := planreview.Rebind(rev.Items, html, now)
	if err != nil {
		return nil, err
	}
	for _, m := range remaps {
		rec, err := planreview.RemapRecord(m)
		if err != nil {
			return nil, err
		}
		// a conflict means another file holds this name: it reads as altered,
		// which every later read reports, and the mark keeps its old anchor
		if _, err := planreview.Publish(planDir, rec); err != nil {
			return nil, err
		}
	}
	return remaps, nil
}
