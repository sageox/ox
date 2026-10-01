package sessionsummary

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func turn(typ string, n int) Entry { return Entry{Type: typ, Content: strings.Repeat("x", n)} }

func TestTrimEntriesForBudget(t *testing.T) {
	t.Run("under budget is unchanged", func(t *testing.T) {
		in := []Entry{turn(EntryTypeUser, 10), {Type: "tool", Content: "ls"}, turn(EntryTypeAssistant, 10)}
		assert.Equal(t, in, TrimEntriesForBudget(in, 10_000))
	})

	t.Run("over budget keeps head, tail and a sample of the middle", func(t *testing.T) {
		var in []Entry
		for i := 0; i < 400; i++ {
			e := turn(EntryTypeUser, 1000)
			if i%2 == 1 {
				e = turn(EntryTypeAssistant, 1000)
			}
			e.Content = fmt.Sprintf("%03d", i) + e.Content[3:]
			in = append(in, e, Entry{Type: "tool", Content: "noise"})
		}
		const budget = 60_000
		out := TrimEntriesForBudget(in, budget)

		total := 0
		for _, e := range out {
			total += renderedCost(e)
			assert.NotEqual(t, "tool", e.Type, "unrendered entries are dropped once trimming")
		}
		assert.LessOrEqual(t, total, budget)
		assert.Equal(t, "000", out[0].Content[:3], "the opening survives")
		assert.Equal(t, "399", out[len(out)-1].Content[:3], "the ending survives")
		for i := 1; i < len(out); i++ {
			assert.Less(t, out[i-1].Content[:3], out[i].Content[:3], "order is preserved")
		}
		middle := 0
		for _, e := range out {
			if e.Content[:3] > "100" && e.Content[:3] < "300" {
				middle++
			}
		}
		assert.Positive(t, middle, "the middle is sampled, not dropped")
	})
}

const goodSummary = `{"title":"Fix stale LFS pointer detection","summary":"Found why pushes failed and fixed pointer parsing in the uploader.","key_actions":["Read the uploader","Fixed the parser","Added a test"],"outcome":"success","summary_status":"unrecoverable","validation_error":"model said so"}`

func TestParseAndValidate(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		entries int
		wantErr string
	}{
		{name: "valid summary", output: goodSummary, entries: 10},
		{name: "fenced JSON", output: "```json\n" + goodSummary + "\n```", entries: 10},
		{name: "not JSON", output: "I cannot summarize this.", entries: 10, wantErr: "no valid summary JSON"},
		{
			name:    "red-flag title",
			output:  strings.Replace(goodSummary, "Fix stale LFS pointer detection", "Fix file permission checks", 1),
			entries: 10, wantErr: "title contains",
		},
		{name: "too thin for a long session", output: goodSummary, entries: 60, wantErr: "too short for a session with 60 entries"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAndValidate(tt.output, tt.entries)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, SummaryStatusOK, got.SummaryStatus, "status comes from validation, not the model")
			assert.Empty(t, got.ValidationError)
		})
	}
}

func TestImportFallbackSummary(t *testing.T) {
	rejected := errors.New("title contains permission request in title")
	tests := []struct {
		name      string
		entries   []Entry
		wantTitle string
	}{
		{
			name:      "first prompt becomes the title",
			entries:   []Entry{{Type: EntryTypeUser, Content: "  Why do file\npermissions break the push?  "}, {Type: EntryTypeAssistant, Content: "..."}},
			wantTitle: "Why do file permissions break the push?",
		},
		{
			name:      "a prompt too short for a title falls back to the label",
			entries:   []Entry{{Type: EntryTypeUser, Content: "ok"}},
			wantTitle: "Imported Claude Code session",
		},
		{
			name:      "a title that reads like a pipeline failure is labeled",
			entries:   []Entry{{Type: EntryTypeUser, Content: "Summary generation failed again, why?"}},
			wantTitle: "Imported Claude Code session: Summary generation failed again, why?",
		},
		{
			name:      "long prompts are clipped",
			entries:   []Entry{{Type: EntryTypeUser, Content: strings.Repeat("word ", 40)}},
			wantTitle: strings.TrimSpace(strings.Repeat("word ", 16)) + "…",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ImportFallbackSummary(tt.entries, "Imported Claude Code session", rejected)
			assert.Equal(t, tt.wantTitle, got.Title)
			assert.Equal(t, SummaryStatusOK, got.SummaryStatus)
			assert.Empty(t, got.ValidationError, "the rejection never reaches validation_error")
			assert.Contains(t, got.ScoreReason, "permission request", "which validator rejected it is recorded")
			assert.GreaterOrEqual(t, len(got.Summary), 20)
			assert.False(t, IsSkipCategory(got))
		})
	}
}

// Failure prevented: a prompt over the summarizer's budget because the evenly
// sampled middle turns happened to be the long ones.
func TestTrimEntriesForBudgetShedsLongSampledTurns(t *testing.T) {
	var entries []Entry
	for i := 0; i < 21; i++ {
		content := "ok"
		if i%2 == 0 {
			content = strings.Repeat("x", 2000)
		}
		entries = append(entries, Entry{Type: EntryTypeUser, Content: content})
	}
	const budget = 4000
	got := TrimEntriesForBudget(entries, budget)
	cost := 0
	for _, e := range got {
		cost += renderedCost(e)
	}
	assert.LessOrEqual(t, cost, budget)
	assert.NotEmpty(t, got, "some of the middle is still sampled")
}

// Failure prevented: a fallback summary for a long session listing every
// prompt it ever had.
func TestImportFallbackSummaryCapsEchoedPrompts(t *testing.T) {
	var entries []Entry
	for i := 0; i < maxPromptsToEcho+3; i++ {
		entries = append(entries, Entry{Type: EntryTypeUser, Content: fmt.Sprintf("prompt number %d about the deploy", i)})
	}
	got := ImportFallbackSummary(entries, "Imported Codex session", errors.New("title too short"))
	assert.Contains(t, got.Summary, "(plus 3 more prompts)")
}
