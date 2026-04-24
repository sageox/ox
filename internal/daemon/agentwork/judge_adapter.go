package agentwork

import (
	"context"
	"fmt"
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
		// A non-zero exit code means the runner's agent CLI exited in
		// failure (rate-limited, auth error, crash). Its stdout is
		// unreliable — often raw stderr or a usage banner — and must not
		// be fed into the judge-JSON parser, which would either parse-fail
		// noisily or, worse, pick up malformed "JSON-ish" text and
		// produce spurious verdicts. Mirror SessionFinalizeHandler's
		// behavior: surface the failure to the caller so maybeRunJudge
		// can swallow it and log a warn without caching a verdict.
		if res.ExitCode != 0 {
			return summaryeval.CompletionResult{}, fmt.Errorf("judge runner exited with code %d (output ignored)", res.ExitCode)
		}
		return summaryeval.CompletionResult{
			Text:             res.Output,
			ModelUsed:        res.ModelUsed,
			PromptTokens:     res.TokensIn,
			CompletionTokens: res.TokensOut,
		}, nil
	}
}
