//go:build integration

package benchmark

import (
	"fmt"
	"strings"
	"testing"
)

// generateReport produces a human-readable benchmark report.
func generateReport(run BenchmarkRun) string {
	var b strings.Builder

	fmt.Fprintf(&b, "=== Prime Efficiency Benchmark ===\n")
	fmt.Fprintf(&b, "%s %s | ox v%s (%s) | %s\n\n",
		run.AgentType, run.AgentVersion,
		run.OxVersion, run.GitCommit,
		run.Timestamp.Format("2006-01-02"))

	fmt.Fprintf(&b, "%-20s | Found | Calls Until Found    | Total Calls    | Duration\n", "Query")
	fmt.Fprintf(&b, "%-20s-|-------|----------------------|----------------|----------\n", strings.Repeat("-", 20))

	// group results by query ID
	grouped := groupByQuery(run.Queries)
	for _, qid := range queryOrder() {
		results, ok := grouped[qid]
		if !ok {
			continue
		}

		found := 0
		var untilFound, totalCalls []int
		var durations []string
		for _, r := range results {
			if r.FoundCorrectSource {
				found++
			}
			untilFound = append(untilFound, r.ToolCallsUntilFound)
			totalCalls = append(totalCalls, r.ToolCallsTotal)
			durations = append(durations, fmt.Sprintf("%.0fs", r.Duration.Seconds()))
		}

		med := median(untilFound)
		fmt.Fprintf(&b, "%-20s | %d/%d   | %s (med: %d) | %s | %s\n",
			qid,
			found, len(results),
			intsToStr(untilFound), med,
			intsToStr(totalCalls),
			strings.Join(durations, ", "))
	}

	// overall summary
	allMedian := medianToolCalls(run.Queries)
	totalFound := 0
	for _, r := range run.Queries {
		if r.FoundCorrectSource {
			totalFound++
		}
	}
	fmt.Fprintf(&b, "\nOverall: median calls-until-found=%d  found=%d/%d\n",
		allMedian, totalFound, len(run.Queries))

	// per-variant breakdown
	fmt.Fprintf(&b, "\n=== Variant Breakdown ===\n")
	for _, qid := range queryOrder() {
		results, ok := grouped[qid]
		if !ok {
			continue
		}

		byVariant := groupByVariant(results)
		for variant, vResults := range byVariant {
			found := 0
			for _, r := range vResults {
				if r.FoundCorrectSource {
					found++
				}
			}
			med := medianFromResults(vResults)
			prompt := truncatePrompt(vResults[0].PromptText, 50)
			fmt.Fprintf(&b, "%-20s  v%d %-52s  %d/%d found  med: %d\n",
				qid, variant, prompt, found, len(vResults), med)
		}
	}

	return b.String()
}

// checkRegression compares the current run against a previous run for the same agent.
func checkRegression(t *testing.T, current, previous *BenchmarkRun) {
	t.Helper()

	if previous == nil {
		t.Log("no previous run found for regression comparison")
		return
	}

	currentMedian := medianToolCalls(current.Queries)
	previousMedian := medianToolCalls(previous.Queries)

	diff := currentMedian - previousMedian
	if diff > 2 {
		t.Errorf("regression: median tool calls %d -> %d (increase of %d)",
			previousMedian, currentMedian, diff)
	} else if diff < 0 {
		t.Logf("improvement: median tool calls %d -> %d", previousMedian, currentMedian)
	} else {
		t.Logf("vs previous (ox %s): no regression (median %d -> %d)",
			previous.OxVersion, previousMedian, currentMedian)
	}

	// per-query regression check
	prevByQuery := make(map[string][]QueryResult)
	for _, q := range previous.Queries {
		prevByQuery[q.QueryID] = append(prevByQuery[q.QueryID], q)
	}
	currByQuery := groupByQuery(current.Queries)

	for qid, currResults := range currByQuery {
		prevResults, ok := prevByQuery[qid]
		if !ok {
			continue
		}
		currMed := medianFromResults(currResults)
		prevMed := medianFromResults(prevResults)
		if currMed-prevMed > 2 {
			t.Logf("  query %q regressed: median %d -> %d", qid, prevMed, currMed)
		}
	}
}

func queryOrder() []string {
	return []string{
		"team-discussions",
		"project-guidance",
		"session-history",
		"team-conventions",
		"sageox-issues",
		"sageox-sync",
		"attribution-guidance",
		"subagent-discovery",
	}
}

func groupByQuery(results []QueryResult) map[string][]QueryResult {
	grouped := make(map[string][]QueryResult)
	for _, r := range results {
		grouped[r.QueryID] = append(grouped[r.QueryID], r)
	}
	return grouped
}

func medianFromResults(results []QueryResult) int {
	var vals []int
	for _, r := range results {
		if r.ToolCallsUntilFound >= 0 {
			vals = append(vals, r.ToolCallsUntilFound)
		}
	}
	return median(vals)
}

func median(vals []int) int {
	if len(vals) == 0 {
		return -1
	}
	sorted := make([]int, len(vals))
	copy(sorted, vals)
	for i := range sorted {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] < sorted[i] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	return sorted[len(sorted)/2]
}

func groupByVariant(results []QueryResult) map[int][]QueryResult {
	grouped := make(map[int][]QueryResult)
	for _, r := range results {
		grouped[r.PromptVariant] = append(grouped[r.PromptVariant], r)
	}
	return grouped
}

func truncatePrompt(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func intsToStr(vals []int) string {
	strs := make([]string, len(vals))
	for i, v := range vals {
		strs[i] = fmt.Sprintf("%d", v)
	}
	return strings.Join(strs, ", ")
}
