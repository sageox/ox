package sessionsummary

import (
	"strings"
	"testing"
)

func TestValidateSummaryContent(t *testing.T) {
	validBase := func() *SummarizeResponse {
		return &SummarizeResponse{
			Title:   "Fix session recording upload pipeline",
			Summary: "Refactored the session upload pipeline to handle LFS failures gracefully with retry logic and fallback to direct git commits.",
			Outcome: "success",
		}
	}

	tests := []struct {
		name    string
		modify  func(r *SummarizeResponse)
		wantErr string // substring of expected error, empty = no error
	}{
		// --- A. Valid summaries pass ---
		{
			name:   "valid summary passes",
			modify: func(r *SummarizeResponse) {},
		},
		{
			name: "valid partial outcome",
			modify: func(r *SummarizeResponse) {
				r.Outcome = "partial"
			},
		},
		{
			name: "valid failed outcome",
			modify: func(r *SummarizeResponse) {
				r.Outcome = "failed"
			},
		},

		// --- B. Structural validation ---
		{
			name:    "nil response",
			modify:  nil, // handled specially below
			wantErr: "nil summary response",
		},
		{
			name: "empty title",
			modify: func(r *SummarizeResponse) {
				r.Title = ""
			},
			wantErr: "title too short",
		},
		{
			name: "title too short",
			modify: func(r *SummarizeResponse) {
				r.Title = "ab"
			},
			wantErr: "title too short",
		},
		{
			name: "title too long",
			modify: func(r *SummarizeResponse) {
				r.Title = strings.Repeat("a", 201)
			},
			wantErr: "title too long",
		},
		{
			name: "empty summary",
			modify: func(r *SummarizeResponse) {
				r.Summary = ""
			},
			wantErr: "summary too short",
		},
		{
			name: "summary too short",
			modify: func(r *SummarizeResponse) {
				r.Summary = "Did some work."
			},
			wantErr: "summary too short",
		},
		{
			name: "empty outcome",
			modify: func(r *SummarizeResponse) {
				r.Outcome = ""
			},
			wantErr: "outcome is empty",
		},
		{
			name: "invalid outcome",
			modify: func(r *SummarizeResponse) {
				r.Outcome = "maybe"
			},
			wantErr: "invalid outcome",
		},

		// --- C. Permission request detection ---
		{
			name: "summary contains permission request",
			modify: func(r *SummarizeResponse) {
				r.Summary = "It looks like file write permissions need to be granted. Could you please approve the write to .ox-summary.json in the workspace?"
			},
			wantErr: "permission request",
		},
		{
			name: "summary contains approve the write",
			modify: func(r *SummarizeResponse) {
				r.Summary = "Could you please approve the write to the sessions directory so I can save the summary file there?"
			},
			wantErr: "permission request",
		},
		{
			name: "title contains permission",
			modify: func(r *SummarizeResponse) {
				r.Title = "Permission needed to write summary"
			},
			wantErr: "permission request in title",
		},

		// --- D. Tool call artifact detection ---
		{
			name: "summary contains function_results tag",
			modify: func(r *SummarizeResponse) {
				r.Summary = "The session produced output including </function_results> which was processed by the agent."
			},
			wantErr: "tool call artifact",
		},
		{
			name: "summary contains mcp tool reference",
			modify: func(r *SummarizeResponse) {
				r.Summary = "Used mcp__claude-in-chrome__navigate to browse the documentation site and verify the deployment."
			},
			wantErr: "tool call artifact",
		},

		// --- E. Agent process leak detection ---
		{
			name: "summary contains ox-summary.json reference",
			modify: func(r *SummarizeResponse) {
				r.Summary = "I need to save the summary file to ox-summary.json before running push-summary command."
			},
			wantErr: "agent process leak",
		},
		{
			name: "summary contains push-summary reference",
			modify: func(r *SummarizeResponse) {
				r.Summary = "Running ox session push-summary to upload the generated summary to the ledger repository."
			},
			wantErr: "agent process leak",
		},

		// --- F. Self-referential agent text detection ---
		{
			name: "summary starts with let me summarize",
			modify: func(r *SummarizeResponse) {
				r.Summary = "Let me summarize what happened in this session. The developer worked on fixing a bug."
			},
			wantErr: "self-referential agent text",
		},
		{
			name: "summary contains I'll generate the summary",
			modify: func(r *SummarizeResponse) {
				r.Summary = "I'll generate the summary for this session now. The main work involved refactoring the auth module."
			},
			wantErr: "self-referential agent text",
		},
		{
			name: "title contains let me",
			modify: func(r *SummarizeResponse) {
				r.Title = "Let me describe this session"
			},
			wantErr: "self-referential text in title",
		},

		// --- G. Conversational text in title ---
		{
			name: "title starts with unfortunately",
			modify: func(r *SummarizeResponse) {
				r.Title = "Unfortunately I could not complete the task"
			},
			wantErr: "conversational text in title",
		},
		{
			name: "title contains could you",
			modify: func(r *SummarizeResponse) {
				r.Title = "Could you review this summary?"
			},
			wantErr: "conversational text in title",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp *SummarizeResponse
			if tt.name == "nil response" {
				resp = nil
			} else {
				resp = validBase()
				tt.modify(resp)
			}

			err := ValidateSummaryContent(resp)

			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("expected no error, got: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestTruncateStr(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is longer than ten", 10, "this is lo..."},
		{"", 5, ""},
	}

	for _, tt := range tests {
		got := truncateStr(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncateStr(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}

func TestValidateSummaryRichness(t *testing.T) {
	richSummary := func() *SummarizeResponse {
		return &SummarizeResponse{
			Title:      "Refactor the thing",
			Summary:    "We refactored the thing by doing this and then that and the outcome was X. Opened PR, tests green.",
			KeyActions: []string{"Ran lint", "Fixed 19 issues", "Opened PR"},
			AhaMoments: []AhaMoment{{Seq: 5, Type: "question"}},
			Outcome:    "success",
		}
	}

	t.Run("rich summary on non-trivial session passes", func(t *testing.T) {
		if err := ValidateSummaryRichness(richSummary(), 100); err != nil {
			t.Errorf("expected pass, got: %v", err)
		}
	})

	t.Run("trivial session skips all richness checks", func(t *testing.T) {
		skeletal := &SummarizeResponse{Title: "x", Summary: "short one", Outcome: "success"}
		if err := ValidateSummaryRichness(skeletal, 5); err != nil {
			t.Errorf("trivial session should skip richness, got: %v", err)
		}
	})

	t.Run("missing key_actions on medium session rejected", func(t *testing.T) {
		r := richSummary()
		r.KeyActions = nil
		err := ValidateSummaryRichness(r, 100)
		if err == nil || !strings.Contains(err.Error(), "key_actions") {
			t.Errorf("expected key_actions rejection, got: %v", err)
		}
	})

	t.Run("under-populated key_actions rejected", func(t *testing.T) {
		r := richSummary()
		r.KeyActions = []string{"just one"}
		if err := ValidateSummaryRichness(r, 100); err == nil || !strings.Contains(err.Error(), "key_actions") {
			t.Errorf("expected under-populated rejection, got: %v", err)
		}
	})

	t.Run("empty-string bullets don't count", func(t *testing.T) {
		r := richSummary()
		r.KeyActions = []string{"real one", "", "   ", ""}
		if err := ValidateSummaryRichness(r, 100); err == nil {
			t.Errorf("expected rejection when only 1 non-empty of 4 bullets")
		}
	})

	t.Run("missing aha_moments on long session rejected", func(t *testing.T) {
		r := richSummary()
		r.AhaMoments = nil
		if err := ValidateSummaryRichness(r, 200); err == nil || !strings.Contains(err.Error(), "aha_moments") {
			t.Errorf("expected aha_moments rejection, got: %v", err)
		}
	})

	t.Run("missing aha_moments acceptable below aha threshold", func(t *testing.T) {
		r := richSummary()
		r.AhaMoments = nil
		if err := ValidateSummaryRichness(r, 30); err != nil {
			t.Errorf("expected aha_moments to be optional at 30 entries, got: %v", err)
		}
	})

	t.Run("too-short summary body rejected", func(t *testing.T) {
		r := richSummary()
		r.Summary = "Session stopped."
		if err := ValidateSummaryRichness(r, 100); err == nil || !strings.Contains(err.Error(), "summary too short") {
			t.Errorf("expected short-summary rejection, got: %v", err)
		}
	})

	t.Run("nil response rejected", func(t *testing.T) {
		if err := ValidateSummaryRichness(nil, 100); err == nil {
			t.Error("nil response should be rejected")
		}
	})
}
