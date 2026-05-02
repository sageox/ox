package summaryeval

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// mockCompleter returns the configured response for any prompt, recording
// the prompt for assertion.
type mockCompleter struct {
	response   CompletionResult
	err        error
	lastPrompt string
	callCount  int
}

func (m *mockCompleter) complete(ctx context.Context, prompt string) (CompletionResult, error) {
	m.callCount++
	m.lastPrompt = prompt
	if m.err != nil {
		return CompletionResult{}, m.err
	}
	return m.response, nil
}

func TestJudge_PairedMode_HappyPath(t *testing.T) {
	mock := &mockCompleter{
		response: CompletionResult{
			Text: `{
				"dimensions": [
					{"dimension": "title", "score": 0.9, "reason": "specific"},
					{"dimension": "summary", "score": 0.85, "reason": "covers outcome"},
					{"dimension": "key_actions", "score": 0.8, "reason": "concrete"},
					{"dimension": "outcome", "score": 1.0, "reason": "match"},
					{"dimension": "aha_moments", "score": 0.7, "reason": "captured pivot"}
				],
				"overall": 0.85,
				"rationale": "Strong summary that matches reference intent.",
				"suggestions": []
			}`,
			ModelUsed:        "haiku-4-5",
			PromptTokens:     1200,
			CompletionTokens: 180,
		},
	}
	j := NewJudge(mock.complete, JudgeOptions{ModelHint: "haiku"})

	ref := &Summary{Title: "Fix lint errors", Summary: "Fixed 19 issues", Outcome: "success"}
	cand := Summary{Title: "Resolve lint issues", Summary: "Resolved 19 lint problems", Outcome: "success"}

	result, err := j.Score(context.Background(), "s1", ref, cand)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if result.Name != "s1" {
		t.Errorf("Name: got %q want s1", result.Name)
	}
	if result.Overall != 0.85 {
		t.Errorf("Overall: got %f want 0.85", result.Overall)
	}
	if len(result.Dimensions) != 5 {
		t.Errorf("expected 5 dimensions, got %d", len(result.Dimensions))
	}
	if result.ModelUsed != "haiku-4-5" {
		t.Errorf("ModelUsed: got %q", result.ModelUsed)
	}
	if result.PromptTokens != 1200 || result.CompletionTokens != 180 {
		t.Errorf("token counts not propagated: prompt=%d completion=%d", result.PromptTokens, result.CompletionTokens)
	}
	if result.DurationMs < 0 {
		t.Errorf("DurationMs should be non-negative")
	}
}

func TestJudge_AbsoluteMode_NoReference(t *testing.T) {
	mock := &mockCompleter{
		response: CompletionResult{
			Text: `{
				"dimensions": [{"dimension": "title", "score": 0.5}],
				"overall": 0.5,
				"rationale": "ok",
				"suggestions": []
			}`,
		},
	}
	j := NewJudge(mock.complete, JudgeOptions{})

	cand := Summary{Title: "x", Outcome: "success"}
	_, err := j.Score(context.Background(), "s1", nil, cand)
	if err != nil {
		t.Fatalf("absolute-mode Score: %v", err)
	}
	if strings.Contains(mock.lastPrompt, "reference") && strings.Contains(mock.lastPrompt, "## Reference summary") {
		t.Errorf("absolute-mode prompt should NOT include reference section, got:\n%s", mock.lastPrompt)
	}
	if !strings.Contains(mock.lastPrompt, "on its own merits") {
		t.Errorf("absolute-mode prompt should state 'on its own merits'")
	}
}

func TestJudge_PairedPromptIncludesReference(t *testing.T) {
	mock := &mockCompleter{
		response: CompletionResult{Text: `{"dimensions":[],"overall":0.5,"rationale":"","suggestions":[]}`},
	}
	j := NewJudge(mock.complete, JudgeOptions{})

	ref := &Summary{Title: "REF_TITLE_SENTINEL", Outcome: "success"}
	cand := Summary{Title: "CAND_TITLE_SENTINEL", Outcome: "success"}
	_, _ = j.Score(context.Background(), "s1", ref, cand)

	if !strings.Contains(mock.lastPrompt, "## Reference summary") {
		t.Errorf("paired-mode prompt must contain Reference section")
	}
	if !strings.Contains(mock.lastPrompt, "REF_TITLE_SENTINEL") {
		t.Errorf("paired-mode prompt must contain reference content")
	}
	if !strings.Contains(mock.lastPrompt, "CAND_TITLE_SENTINEL") {
		t.Errorf("prompt must contain candidate content")
	}
}

func TestJudge_PromptJSONFenceTolerated(t *testing.T) {
	mock := &mockCompleter{
		response: CompletionResult{
			Text: "```json\n{\n\"dimensions\":[],\n\"overall\":0.75,\n\"rationale\":\"ok\",\n\"suggestions\":[]\n}\n```",
		},
	}
	j := NewJudge(mock.complete, JudgeOptions{})
	result, err := j.Score(context.Background(), "s1", nil, Summary{Title: "x"})
	if err != nil {
		t.Fatalf("fenced response should parse: %v", err)
	}
	if result.Overall != 0.75 {
		t.Errorf("overall: got %f", result.Overall)
	}
}

func TestJudge_PromptSurroundingProseTolerated(t *testing.T) {
	// Some models ignore "no prose" instructions. Extract the outermost {...}.
	mock := &mockCompleter{
		response: CompletionResult{
			Text: `Here's my evaluation:
{"dimensions":[],"overall":0.6,"rationale":"ok","suggestions":[]}
Let me know if you need more detail.`,
		},
	}
	j := NewJudge(mock.complete, JudgeOptions{})
	result, err := j.Score(context.Background(), "s1", nil, Summary{Title: "x"})
	if err != nil {
		t.Fatalf("prose-wrapped response should parse: %v", err)
	}
	if result.Overall != 0.6 {
		t.Errorf("overall: got %f", result.Overall)
	}
}

func TestJudge_MalformedResponseFails(t *testing.T) {
	mock := &mockCompleter{
		response: CompletionResult{Text: "I apologize but I can't do that"},
	}
	j := NewJudge(mock.complete, JudgeOptions{})
	_, err := j.Score(context.Background(), "s1", nil, Summary{Title: "x"})
	if err == nil {
		t.Fatal("expected parse error on non-JSON response")
	}
	if !strings.Contains(err.Error(), "no JSON") {
		t.Errorf("error should mention missing JSON, got: %v", err)
	}
}

func TestJudge_CompleterErrorPropagates(t *testing.T) {
	mock := &mockCompleter{err: errors.New("rate limited")}
	j := NewJudge(mock.complete, JudgeOptions{})
	_, err := j.Score(context.Background(), "s1", nil, Summary{Title: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error should wrap original: %v", err)
	}
}

func TestJudge_ScoresClampedToUnitInterval(t *testing.T) {
	mock := &mockCompleter{
		response: CompletionResult{
			Text: `{
				"dimensions": [
					{"dimension": "title", "score": 1.5, "reason": "too high"},
					{"dimension": "summary", "score": -0.2, "reason": "too low"}
				],
				"overall": 2.0,
				"rationale": "",
				"suggestions": []
			}`,
		},
	}
	j := NewJudge(mock.complete, JudgeOptions{})
	r, err := j.Score(context.Background(), "s1", nil, Summary{Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Overall != 1.0 {
		t.Errorf("overall clamped: got %f want 1.0", r.Overall)
	}
	if r.Dimensions[0].Score != 1.0 {
		t.Errorf("title clamped: got %f want 1.0", r.Dimensions[0].Score)
	}
	if r.Dimensions[1].Score != 0.0 {
		t.Errorf("summary clamped: got %f want 0", r.Dimensions[1].Score)
	}
}

func TestJudge_EmptySuggestionsFiltered(t *testing.T) {
	mock := &mockCompleter{
		response: CompletionResult{
			Text: `{
				"dimensions": [],
				"overall": 0.5,
				"rationale": "",
				"suggestions": ["   ", "real suggestion", "", "another"]
			}`,
		},
	}
	j := NewJudge(mock.complete, JudgeOptions{})
	r, _ := j.Score(context.Background(), "s1", nil, Summary{Title: "x"})
	if len(r.Suggestions) != 2 {
		t.Errorf("expected 2 non-empty suggestions, got %v", r.Suggestions)
	}
}

func TestJudge_SuggestionsRequestedWhenOptSet(t *testing.T) {
	mock := &mockCompleter{
		response: CompletionResult{Text: `{"dimensions":[],"overall":0.5,"rationale":"","suggestions":[]}`},
	}
	j := NewJudge(mock.complete, JudgeOptions{IncludeSuggestions: true})
	_, _ = j.Score(context.Background(), "s1", nil, Summary{Title: "x"})
	if !strings.Contains(mock.lastPrompt, "concrete and actionable") {
		t.Errorf("IncludeSuggestions should surface suggestion guidance in prompt")
	}
}

func TestJudgeResult_ToSessionScore(t *testing.T) {
	jr := JudgeResult{
		Name: "session-1",
		Dimensions: []DimensionScore{
			{Dimension: DimTitle, Score: 0.9},
			{Dimension: DimSummary, Score: 0.7},
		},
		Overall: 0.82,
	}
	ss := jr.ToSessionScore()
	if ss.Name != "session-1" {
		t.Errorf("Name: got %q", ss.Name)
	}
	if ss.Overall != 0.82 {
		t.Errorf("Overall: got %f", ss.Overall)
	}
	if len(ss.Dimensions) != 2 {
		t.Errorf("Dimensions: got %d", len(ss.Dimensions))
	}
}

func TestJudgeResult_ToSessionScore_OverallClamped(t *testing.T) {
	jr := JudgeResult{Name: "s", Overall: 1.5}
	if got := jr.ToSessionScore().Overall; got != 1.0 {
		t.Errorf("overall should clamp to 1.0, got %f", got)
	}
}

func TestBuildJudgePrompt_DeterministicShape(t *testing.T) {
	// The prompt text must contain the critical instruction markers that
	// keep the judge's output parseable. Missing any of these is a bug.
	p := BuildJudgePrompt(nil, Summary{Title: "x", Outcome: "success"}, JudgeOptions{})
	for _, must := range []string{
		"# Session Summary Quality Judge",
		"## Rubric",
		"## Response format",
		"## Candidate summary",
		"Return ONLY the JSON object",
		"0.0 (fails completely) to 1.0 (perfect)",
	} {
		if !strings.Contains(p, must) {
			t.Errorf("prompt missing required section: %q", must)
		}
	}
}

func TestBuildJudgePrompt_PairedModeAddsReferenceSection(t *testing.T) {
	ref := &Summary{Title: "ref"}
	paired := BuildJudgePrompt(ref, Summary{Title: "cand"}, JudgeOptions{})
	if !strings.Contains(paired, "## Reference summary") {
		t.Error("paired mode missing Reference section")
	}

	abs := BuildJudgePrompt(nil, Summary{Title: "cand"}, JudgeOptions{})
	if strings.Contains(abs, "## Reference summary") {
		t.Error("absolute mode should NOT contain Reference section")
	}
}

func TestJudgeResult_LogValue(t *testing.T) {
	jr := JudgeResult{Name: "s1", Overall: 0.82, ModelUsed: "haiku", DurationMs: 1500, PromptTokens: 1000}
	v := jr.LogValue()
	attrs := v.Group()
	found := make(map[string]bool)
	for _, a := range attrs {
		found[a.Key] = true
	}
	for _, want := range []string{"session", "overall", "model", "duration_ms", "prompt_tokens", "completion_tokens", "suggestions"} {
		if !found[want] {
			t.Errorf("LogValue missing %q", want)
		}
	}
}
