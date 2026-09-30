package sessionsummary

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// ImportPromptBudget bounds the transcript text an imported session sends to
// the summarizer. BuildInlineSummaryPrompt caps each entry but not the total,
// and a months-old session can be far longer than a small model's context.
const ImportPromptBudget = 300_000

// entryOverhead approximates the <entry> envelope around each rendered turn.
const entryOverhead = 64

// renderedCost is what one entry adds to BuildInlineSummaryPrompt's output;
// entries it does not render cost nothing.
func renderedCost(e Entry) int {
	switch e.Type {
	case EntryTypeUser:
		return min(len(e.Content), 2000) + entryOverhead
	case EntryTypeAssistant:
		return min(len(e.Content), 3000) + entryOverhead
	default:
		return 0
	}
}

// TrimEntriesForBudget keeps a transcript within maxChars of rendered prompt
// text. Under budget, entries are returned unchanged. Over budget, it keeps
// only user and assistant turns: the opening quarter of the budget, the closing
// quarter, and evenly spaced turns from the middle, in their original order.
func TrimEntriesForBudget(entries []Entry, maxChars int) []Entry {
	var turns []int
	total := 0
	for i, e := range entries {
		if cost := renderedCost(e); cost > 0 {
			turns = append(turns, i)
			total += cost
		}
	}
	if total <= maxChars || len(turns) == 0 {
		return entries
	}

	keep := make([]bool, len(turns))
	headBudget, tailBudget := maxChars/4, maxChars/4
	head, used := 0, 0
	for head < len(turns) && used+renderedCost(entries[turns[head]]) <= headBudget {
		used += renderedCost(entries[turns[head]])
		keep[head] = true
		head++
	}
	tail, tailUsed := len(turns), 0
	for tail > head && tailUsed+renderedCost(entries[turns[tail-1]]) <= tailBudget {
		tail--
		tailUsed += renderedCost(entries[turns[tail]])
		keep[tail] = true
	}

	// Sample the middle evenly, then shed samples until the budget holds.
	middleBudget := maxChars - used - tailUsed
	if middle := tail - head; middle > 0 && middleBudget > 0 {
		middleCost := 0
		for i := head; i < tail; i++ {
			middleCost += renderedCost(entries[turns[i]])
		}
		want := middle * middleBudget / middleCost
		if want > middle {
			want = middle
		}
		var picked []int
		for k := 0; k < want; k++ {
			picked = append(picked, head+k*middle/max(want, 1))
		}
		cost := 0
		for _, i := range picked {
			cost += renderedCost(entries[turns[i]])
		}
		for len(picked) > 0 && cost > middleBudget {
			drop := len(picked) / 2
			cost -= renderedCost(entries[turns[picked[drop]]])
			picked = append(picked[:drop], picked[drop+1:]...)
		}
		for _, i := range picked {
			keep[i] = true
		}
	}

	out := make([]Entry, 0, len(turns))
	for i, idx := range turns {
		if keep[i] {
			out = append(out, entries[idx])
		}
	}
	return out
}

// ParseAndValidate parses a summarizer's output and applies the validators the
// daemon applies: content, then richness for the session's entry count. The
// model never sets status; a summary that passes carries SummaryStatus ok.
func ParseAndValidate(output string, entryCount int) (*SummarizeResponse, error) {
	resp, err := ParseSummaryJSON(output)
	if err != nil {
		return nil, err
	}
	resp.SummaryStatus, resp.ValidationError = "", ""
	if err := ValidateSummaryContent(resp); err != nil {
		return nil, err
	}
	if err := ValidateSummaryRichness(resp, entryCount); err != nil {
		return nil, err
	}
	resp.SummaryStatus = SummaryStatusOK
	return resp, nil
}

const (
	fallbackTitleRunes  = 80
	fallbackPromptRunes = 200
)

// ImportFallbackSummary is the deterministic summary an import uploads when the
// validators still reject the summarizer's output after every attempt. Sessions
// about permissions, MCP tools or tool parsing trip them on every retry, so
// holding them would make whole topics impossible to import.
//
// The title is the first prompt; label stands in when it is too short. Which
// validator rejected the summary goes in ScoreReason, part of summary.json and
// never meta.json, so no summary retry path ever picks the session up.
func ImportFallbackSummary(entries []Entry, label string, rejected error) *SummarizeResponse {
	var prompts []string
	for _, e := range entries {
		if e.Type == EntryTypeUser {
			if text := strings.Join(strings.Fields(e.Content), " "); text != "" {
				prompts = append(prompts, text)
			}
		}
	}

	title := label
	if len(prompts) > 0 {
		if first := clipRunes(prompts[0], fallbackTitleRunes); len(strings.TrimSpace(first)) >= 3 {
			title = first
		}
	}
	// A title that reads like a pipeline failure would be refused by the meta
	// writer; the label keeps it a plain description.
	if strings.HasPrefix(title, "Summary ") {
		title = label + ": " + title
	}

	var sb strings.Builder
	sb.WriteString("Summary written from the session's prompts, because the generated summary did not pass validation.")
	for i, p := range prompts {
		if i == maxPromptsToEcho {
			fmt.Fprintf(&sb, "\n(plus %d more prompts)", len(prompts)-maxPromptsToEcho)
			break
		}
		sb.WriteString("\n- ")
		sb.WriteString(clipRunes(p, fallbackPromptRunes))
	}

	reason := "import fallback"
	if rejected != nil {
		reason += ": " + clipRunes(rejected.Error(), fallbackPromptRunes)
	}
	return &SummarizeResponse{
		Title:         title,
		Summary:       sb.String(),
		ScoreReason:   reason,
		SummaryStatus: SummaryStatusOK,
	}
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}
