package agentwork

import (
	"context"
	"time"

	"github.com/sageox/ox/pkg/summaryeval"
)

// NewRunnerCompleter adapts the daemon's Runner interface into a
// summaryeval.Completer so the summary-judge scorer can be invoked via
// the same subagent mechanism the daemon already uses for primary
// summarization. No Anthropic SDK dependency is introduced — the judge
// runs as a plain prompt through whichever Runner (Claude / Codex /
// Gemini) the user has configured.
//
// The returned Completer is safe to call concurrently if the underlying
// Runner is concurrency-safe.
func NewRunnerCompleter(r Runner) summaryeval.Completer {
	return func(ctx context.Context, prompt string) (summaryeval.CompletionResult, error) {
		// Judging a summary is a short, bounded task — cap the timeout
		// conservatively so a stuck judge never blocks the finalize path.
		res, err := r.Run(ctx, RunRequest{
			Prompt:          prompt,
			TimeoutOverride: 2 * time.Minute,
		})
		if err != nil {
			return summaryeval.CompletionResult{}, err
		}
		return summaryeval.CompletionResult{
			Text:             res.Output,
			PromptTokens:     res.TokensIn,
			CompletionTokens: res.TokensOut,
		}, nil
	}
}
