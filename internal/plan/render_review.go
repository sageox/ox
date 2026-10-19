package plan

import (
	"encoding/json"
	"html/template"

	"github.com/sageox/ox/pkg/planreview"
)

// render_review.go projects the merged review state (rounds + resolutions) into
// what the rendered page needs: a JSON island the review layer reads to paint
// each mark inline, plus server-side summary counts (visible even with JS off).
// Separated from render.go's template assembly for cohesion. (reviewStateItem and
// reviewSummary are declared in render.go alongside renderData.)

// buildReviewState returns (the JSON island, the summary counts). An item's
// state is "open", or its latest resolution state (addressed|verified|wontfix).
func buildReviewState(items []MergedItem) (template.JS, reviewSummary) {
	var sum reviewSummary
	if len(items) == 0 {
		return template.JS("[]"), sum
	}
	sum.HasReview = true
	slim := make([]reviewStateItem, 0, len(items))
	for _, it := range items {
		state, resolutionNote := "open", ""
		if !it.Open && it.Resolution != nil {
			state, resolutionNote = string(it.Resolution.State), it.Resolution.Note
		}
		switch {
		case it.Open && it.Status == planreview.StatusApprove:
			// an approval is not actionable; don't inflate the open count
		case it.Open:
			sum.Open++
		default:
			switch it.Resolution.State {
			case planreview.StateAddressed:
				sum.Addressed++
			case planreview.StateVerified:
				sum.Verified++
			case planreview.StateWontfix:
				sum.Wontfix++
			}
		}
		slim = append(slim, reviewStateItem{
			Anchor: it.Anchor, Section: it.Section, Label: it.Label, Quote: it.Quote,
			Status: string(it.Status), State: state, Note: it.Note, Reviewer: it.Reviewer,
			ResolutionNote: resolutionNote,
		})
	}
	b, err := json.Marshal(slim)
	if err != nil {
		return template.JS("[]"), sum
	}
	return template.JS(b), sum
}
