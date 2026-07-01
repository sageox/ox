package plan

import (
	"encoding/json"
	"html/template"
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
		state := "open"
		if !it.Open && it.Resolution != nil {
			state = string(it.Resolution.State)
		}
		switch {
		case it.Open && it.Status == FeedbackApprove:
			// an approval is not actionable; don't inflate the open count
		case it.Open:
			sum.Open++
		default:
			switch it.Resolution.State {
			case ResolutionAddressed:
				sum.Addressed++
			case ResolutionVerified:
				sum.Verified++
			case ResolutionWontfix:
				sum.Wontfix++
			}
		}
		slim = append(slim, reviewStateItem{
			Anchor: it.Anchor, Section: it.Section, Label: it.Label,
			Status: string(it.Status), State: state, Note: it.Note, Reviewer: it.Reviewer,
		})
	}
	b, err := json.Marshal(slim)
	if err != nil {
		return template.JS("[]"), sum
	}
	return template.JS(b), sum
}
