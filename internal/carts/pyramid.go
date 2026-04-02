package carts

import (
	"fmt"
	"strings"
)

// Pyramid holds multi-resolution summaries for progressive disclosure.
// AI coworkers scan L2 (2 words) for hundreds of items, then zoom into
// L4/L8/L16 only for items that need attention.
//
// Inspired by StrongDM's "Pyramid Summaries" technique for agentic factories.
type Pyramid struct {
	L2  string `json:"l2"`            // 2 words: scan-level
	L4  string `json:"l4"`            // 4 words: triage-level
	L8  string `json:"l8"`            // 8 words: context-level
	L16 string `json:"l16,omitempty"` // 16 words: detail-level (omitted at cart level)
}

// PyramidSummary is the top-level pyramid view of an analysis.
type PyramidSummary struct {
	Overall  Pyramid            `json:"overall"`
	ByStatus map[string]Pyramid `json:"by_status"`
}

// buildOverallPyramid produces a multi-resolution summary of the full analysis.
func buildOverallPyramid(o *AnalysisOutput) Pyramid {
	p := o.Progress
	closed := p.ByStatus["closed"]
	inProgress := p.ByStatus["in_progress"]
	open := p.ByStatus["open"]
	blocked := len(o.Blockers)

	// L2: the single most important signal
	switch {
	case blocked > 0:
		return Pyramid{
			L2:  "work blocked",
			L4:  fmt.Sprintf("%d blocked, %d done", blocked, closed),
			L8:  fmt.Sprintf("%d/%d done, %d blocked, %d in progress, %d coworkers", closed, p.TotalCarts, blocked, inProgress, countAssignees(o)),
			L16: buildL16Overall(o),
		}
	case p.CompletionRate == 1:
		return Pyramid{
			L2:  "all done",
			L4:  fmt.Sprintf("%d carts completed", p.TotalCarts),
			L8:  fmt.Sprintf("all %d carts completed by %d coworkers, no blockers", p.TotalCarts, countAssignees(o)),
			L16: buildL16Overall(o),
		}
	case inProgress > 0 && open == 0:
		return Pyramid{
			L2:  "work active",
			L4:  fmt.Sprintf("%d active, %d done", inProgress, closed),
			L8:  fmt.Sprintf("%d/%d done, %d in progress by %d coworkers, no blockers", closed, p.TotalCarts, inProgress, countAssignees(o)),
			L16: buildL16Overall(o),
		}
	case p.TotalCarts == 0:
		return Pyramid{
			L2:  "no carts",
			L4:  "no carts this period",
			L8:  "no carts found in the selected time window",
			L16: "no carts were created or active during this time period",
		}
	default:
		return Pyramid{
			L2:  "in progress",
			L4:  fmt.Sprintf("%d/%d done, %d open", closed, p.TotalCarts, open),
			L8:  fmt.Sprintf("%d/%d done, %d open, %d in progress by %d coworkers", closed, p.TotalCarts, open, inProgress, countAssignees(o)),
			L16: buildL16Overall(o),
		}
	}
}

// buildL16Overall constructs the 16-word detail level.
func buildL16Overall(o *AnalysisOutput) string {
	p := o.Progress
	closed := p.ByStatus["closed"]

	var parts []string
	parts = append(parts, fmt.Sprintf("%d/%d carts completed by %d coworkers", closed, p.TotalCarts, countAssignees(o)))

	// Add type breakdown for the dominant type
	maxType := ""
	maxCount := 0
	for t, c := range p.ByType {
		if c > maxCount {
			maxType = t
			maxCount = c
		}
	}
	if maxType != "" && len(p.ByType) > 1 {
		parts = append(parts, fmt.Sprintf("mostly %ss", maxType))
	}

	if len(o.Blockers) > 0 {
		parts = append(parts, fmt.Sprintf("%d blocked", len(o.Blockers)))
	}
	if len(o.Learnings) > 0 {
		parts = append(parts, fmt.Sprintf("%d learnings captured", len(o.Learnings)))
	}
	if p.SessionCount > 0 {
		parts = append(parts, fmt.Sprintf("%d sessions recorded", p.SessionCount))
	}

	return truncateWords(strings.Join(parts, ", "), 16)
}

// buildStatusPyramids produces per-status pyramids.
func buildStatusPyramids(o *AnalysisOutput) map[string]Pyramid {
	result := make(map[string]Pyramid)
	for status, carts := range o.CartsByStatus {
		result[status] = buildStatusPyramid(status, carts)
	}
	return result
}

func buildStatusPyramid(status string, carts []CartSummary) Pyramid {
	n := len(carts)

	// Collect unique assignees and types
	assignees := make(map[string]bool)
	types := make(map[string]int)
	var highPri []string
	for _, c := range carts {
		assignees[c.Assignee] = true
		types[c.Type]++
		if c.Priority <= 1 {
			highPri = append(highPri, c.Title)
		}
	}

	// L2
	l2 := fmt.Sprintf("%d %s", n, status)

	// L4: count + assignee count
	l4 := fmt.Sprintf("%d %s, %d coworkers", n, status, len(assignees))

	// L8: add type breakdown
	typeStrs := make([]string, 0, len(types))
	for t, c := range types {
		typeStrs = append(typeStrs, fmt.Sprintf("%d %ss", c, t))
	}
	l8 := fmt.Sprintf("%d %s by %d coworkers: %s", n, status, len(assignees), strings.Join(typeStrs, ", "))

	// L16: add high-priority items and assignee names
	var l16Parts []string
	l16Parts = append(l16Parts, fmt.Sprintf("%d %s by %d coworkers", n, status, len(assignees)))
	if len(highPri) > 0 {
		l16Parts = append(l16Parts, fmt.Sprintf("high priority: %s", strings.Join(highPri, ", ")))
	}
	names := make([]string, 0, len(assignees))
	for a := range assignees {
		names = append(names, a)
	}
	l16Parts = append(l16Parts, fmt.Sprintf("by %s", strings.Join(names, ", ")))

	return Pyramid{
		L2:  truncateWords(l2, 2),
		L4:  truncateWords(l4, 4),
		L8:  truncateWords(l8, 8),
		L16: truncateWords(strings.Join(l16Parts, "; "), 16),
	}
}

// buildCartPyramid produces a per-cart pyramid (L2/L4/L8 only; L16 omitted).
func buildCartPyramid(c *Issue, session *SessionEvidence) Pyramid {
	// L2: status + essence of title (first 2 significant words)
	titleWords := significantWords(c.Title)
	l2 := truncateWords(strings.Join(titleWords, " "), 2)

	// L4: status + title essence + assignee
	l4 := truncateWords(fmt.Sprintf("%s %s", c.Title, string(c.Status)), 4)

	// L8: fuller picture
	l8parts := []string{c.Title}
	l8parts = append(l8parts, string(c.Status))
	l8parts = append(l8parts, c.Assignee)
	if c.Priority <= 1 {
		l8parts = append(l8parts, fmt.Sprintf("P%d", c.Priority))
	}
	if session != nil && session.Outcome != "" {
		l8parts = append(l8parts, session.Outcome)
	}

	return Pyramid{
		L2: l2,
		L4: l4,
		L8: truncateWords(strings.Join(l8parts, ", "), 8),
	}
}

// countAssignees counts unique assignees across all work items.
func countAssignees(o *AnalysisOutput) int {
	assignees := make(map[string]bool)
	for _, wi := range o.WorkItems {
		if wi.Cart.Assignee != "" {
			assignees[wi.Cart.Assignee] = true
		}
	}
	return len(assignees)
}

// significantWords extracts meaningful words from a title, skipping stop words.
func significantWords(title string) []string {
	stopWords := map[string]bool{
		"a": true, "an": true, "the": true, "and": true, "or": true,
		"for": true, "to": true, "in": true, "on": true, "of": true,
		"with": true, "by": true, "add": true, "update": true,
	}
	var words []string
	for _, w := range strings.Fields(strings.ToLower(title)) {
		if !stopWords[w] {
			words = append(words, w)
		}
	}
	return words
}

// truncateWords keeps at most n words.
func truncateWords(s string, n int) string {
	words := strings.Fields(s)
	if len(words) <= n {
		return s
	}
	return strings.Join(words[:n], " ")
}
