package plan

import (
	"context"

	"github.com/sageox/ox/internal/decision"
)

const planDecisionCap = 5

// decisionRetriever ties plans back to the repo's own Decision Records: DRs
// relevant to the plan surface as "adr" context items, which the render turns
// into subtle inline OX markers on their prose tokens ("ADR-021") — context,
// never a verdict, and never more chrome than a marker. Registered alongside
// the team-context bundle; fail-open like every retriever.
type decisionRetriever struct{}

func init() {
	RegisterRetriever(decisionRetriever{})
}

func (decisionRetriever) Name() string { return "repo-decisions" }

func (decisionRetriever) Retrieve(_ context.Context, in Input, gitRoot string) ([]ContextItem, error) {
	query := deriveQuery(in)
	if query == "" || gitRoot == "" {
		return nil, nil
	}
	matches, omitted := decision.Relevant(gitRoot, query, planDecisionCap)
	var items []ContextItem
	for _, dr := range matches {
		title := dr.Title
		if dr.ID != "" {
			title = dr.ID + " — " + dr.Title
		}
		items = append(items, ContextItem{
			Kind:    "adr",
			Title:   title,
			Ref:     dr.RelPath,
			Snippet: dr.Excerpt,
			Score:   dr.Score,
			When:    dr.Date,
		})
	}
	if omitted > 0 && len(items) > 0 {
		items[0].Omitted = omitted
	}
	return items, nil
}
