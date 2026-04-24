package summaryeval

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Judge evaluates a candidate Summary semantically, returning a
// JudgeResult. Complements the deterministic rubric Score (scorer.go)
// for cases where lexical metrics aren't enough — paraphrased summaries
// that convey the same meaning with different vocabulary score poorly
// on Jaccard but should score well on semantic equivalence.
//
// Two modes:
//   - Paired: Score with a non-nil reference. The judge evaluates
//     semantic equivalence between candidate and reference.
//   - Absolute: Score with a nil reference. The judge evaluates the
//     candidate on its own merits against the rubric description,
//     without needing a curated corpus.
//
// Absolute mode is what the daemon runs in production — no corpus
// maintenance required, just "is this summary good on its face?"
type Judge interface {
	Score(ctx context.Context, name string, reference *Summary, candidate Summary) (JudgeResult, error)
}

// JudgeResult is what a Judge returns for one session.
type JudgeResult struct {
	// Name identifies the session scored.
	Name string `json:"name"`

	// Dimensions are per-dimension 0.0-1.0 scores (same dimension
	// names as Rubric). Absent dimensions are treated as 0.0 when
	// converting to SessionScore.
	Dimensions []DimensionScore `json:"dimensions"`

	// Overall is the judge's aggregate verdict, 0.0-1.0.
	Overall float64 `json:"overall"`

	// Rationale is a short human-readable explanation of the verdict.
	// Useful for humans debugging "why did this summary score 0.4?"
	Rationale string `json:"rationale,omitempty"`

	// Suggestions is a short list of specific, actionable fixes the
	// judge thinks would improve the summary. Surfaced to the user
	// via diagnostic output; consumed by CI in "hint to the prompt
	// engineer" mode.
	Suggestions []string `json:"suggestions,omitempty"`

	// ModelUsed identifies which model produced the judgment, for
	// reproducibility tracking ("haiku-4-5", "opus-4-7", etc.).
	ModelUsed string `json:"model_used,omitempty"`

	// DurationMs captures end-to-end judge latency including the LLM
	// call. Useful for operational telemetry.
	DurationMs int64 `json:"duration_ms,omitempty"`

	// PromptTokens / CompletionTokens are the raw token counts the
	// LLM reported, when available. Used for cost attribution.
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
}

// ToSessionScore converts a JudgeResult to a SessionScore so it composes
// with the same ScoreCorpus / Report aggregation the deterministic
// scorer uses. The overall score is taken verbatim from the judge;
// individual dimensions carry through.
func (jr JudgeResult) ToSessionScore() SessionScore {
	return SessionScore{
		Name:       jr.Name,
		Dimensions: jr.Dimensions,
		Overall:    clamp01(jr.Overall),
	}
}

// LogValue implements slog.LogValuer so callers can emit one-line
// judge telemetry via the existing slog path.
func (jr JudgeResult) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("session", jr.Name),
		slog.Float64("overall", jr.Overall),
		slog.String("model", jr.ModelUsed),
		slog.Int64("duration_ms", jr.DurationMs),
		slog.Int("prompt_tokens", jr.PromptTokens),
		slog.Int("completion_tokens", jr.CompletionTokens),
		slog.Int("suggestions", len(jr.Suggestions)),
	)
}

// Completer is the abstraction for calling an LLM. Callers provide the
// actual implementation; pkg/summaryeval does NOT depend on any
// specific SDK (Anthropic, OpenAI, Bedrock, etc.). This keeps the
// package importable with only stdlib and leaves all API concerns —
// authentication, retries, rate limiting, model selection, streaming —
// to the caller.
//
// The prompt is the full text to send. The return is the model's raw
// response text. Errors propagate up to the Judge caller.
type Completer func(ctx context.Context, prompt string) (CompletionResult, error)

// CompletionResult is the response shape Completer returns. Token
// counts are optional — pass 0 when unknown.
type CompletionResult struct {
	Text             string
	ModelUsed        string
	PromptTokens     int
	CompletionTokens int
}

// JudgeOptions tune a Judge's behavior.
type JudgeOptions struct {
	// ModelHint is a free-form tag the Judge can use to signal the
	// desired model class to the Completer. The Completer may ignore
	// it. Example: "haiku" for cheap/fast; "opus" for deep evaluation.
	// If empty, the Completer picks.
	ModelHint string

	// IncludeSuggestions, when true, asks the judge for up to 5 concrete
	// suggestions for improving the summary. Slight prompt length
	// increase; usually worth it for diagnostic mode.
	IncludeSuggestions bool

	// MaxRationaleChars caps the rationale length the judge is asked
	// to produce. Default: 600. Set lower to reduce completion tokens.
	MaxRationaleChars int
}

// NewJudge constructs a Judge backed by the given Completer. The judge
// uses a fixed prompt template (see BuildJudgePrompt) and parses the
// LLM's JSON response. Callers that need different prompts or response
// shapes should implement Judge directly.
func NewJudge(c Completer, opts JudgeOptions) Judge {
	if opts.MaxRationaleChars == 0 {
		opts.MaxRationaleChars = 600
	}
	return &completerJudge{completer: c, opts: opts}
}

type completerJudge struct {
	completer Completer
	opts      JudgeOptions
}

func (j *completerJudge) Score(ctx context.Context, name string, reference *Summary, candidate Summary) (JudgeResult, error) {
	start := time.Now()

	prompt := BuildJudgePrompt(reference, candidate, j.opts)

	resp, err := j.completer(ctx, prompt)
	if err != nil {
		return JudgeResult{Name: name}, fmt.Errorf("judge completion: %w", err)
	}

	result, parseErr := parseJudgeResponse(resp.Text)
	if parseErr != nil {
		return JudgeResult{Name: name}, fmt.Errorf("judge response parse: %w", parseErr)
	}
	result.Name = name
	result.ModelUsed = resp.ModelUsed
	result.PromptTokens = resp.PromptTokens
	result.CompletionTokens = resp.CompletionTokens
	result.DurationMs = time.Since(start).Milliseconds()
	return result, nil
}

// parseJudgeResponse extracts the JSON envelope from the LLM's response.
// Tolerates surrounding prose by finding the first {...} block.
func parseJudgeResponse(text string) (JudgeResult, error) {
	text = strings.TrimSpace(text)
	// Some models wrap JSON in ```json ... ``` — strip.
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	// Find the outermost {...} block in case the model added prose.
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < 0 || end <= start {
		return JudgeResult{}, fmt.Errorf("no JSON object found in response: %q", truncate(text, 120))
	}
	jsonBody := text[start : end+1]

	// The LLM returns a simpler envelope than our full JudgeResult;
	// translate here so the model's job stays simple. Expected shape:
	//
	//  {
	//    "dimensions": [{"dimension": "title", "score": 0.9, "reason": "..."}, ...],
	//    "overall": 0.85,
	//    "rationale": "...",
	//    "suggestions": ["...", "..."]
	//  }
	var raw struct {
		Dimensions  []DimensionScore `json:"dimensions"`
		Overall     float64          `json:"overall"`
		Rationale   string           `json:"rationale"`
		Suggestions []string         `json:"suggestions"`
	}
	if err := json.Unmarshal([]byte(jsonBody), &raw); err != nil {
		return JudgeResult{}, fmt.Errorf("unmarshal: %w (body=%q)", err, truncate(jsonBody, 200))
	}

	// Clamp scores, filter empty suggestions.
	for i := range raw.Dimensions {
		raw.Dimensions[i].Score = clamp01(raw.Dimensions[i].Score)
	}
	cleaned := make([]string, 0, len(raw.Suggestions))
	for _, s := range raw.Suggestions {
		if s = strings.TrimSpace(s); s != "" {
			cleaned = append(cleaned, s)
		}
	}

	return JudgeResult{
		Dimensions:  raw.Dimensions,
		Overall:     clamp01(raw.Overall),
		Rationale:   strings.TrimSpace(raw.Rationale),
		Suggestions: cleaned,
	}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
