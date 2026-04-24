# LLM-as-Judge Scorer

The deterministic `Score()` function in scorer.go uses lexical metrics (Jaccard, F1, exact match). It's fast, free, deterministic — and blind to paraphrasing. A candidate summary that conveys the same meaning with different vocabulary scores lower than a candidate that copies the reference verbatim.

The `Judge` in judge.go complements it: a pluggable LLM-backed scorer with the same dimension rubric and output shape.

## When to use which

| Signal | Use |
|---|---|
| CI regression gate on every commit | Deterministic `ScoreCorpus` — fast, free, deterministic |
| Periodic quality audits | `Judge` — catches paraphrase regressions the lexical scorer misses |
| Daemon anti-entropy runs | `Judge` in absolute mode (no reference needed) — write result to ledger cache, log path |
| Debugging one bad summary | `Judge` with `IncludeSuggestions: true` — get specific actionable fixes |
| Corpus curation | `Judge` to spot-check your references — "am I asking for something unreasonable?" |

## Integration shape (SDK-agnostic)

`pkg/summaryeval` does NOT depend on any LLM SDK. Callers provide a `Completer` — a `func(ctx, prompt) (CompletionResult, error)` — so authentication, retries, rate limiting, model selection, and streaming all stay with the caller.

```go
import (
    "context"
    "github.com/sageox/ox/pkg/summaryeval"
    // ... your LLM SDK here ...
)

// Adapt your LLM client to the Completer shape.
func myCompleter(ctx context.Context, prompt string) (summaryeval.CompletionResult, error) {
    resp, err := yourClient.Messages.Create(ctx, &yourClient.Request{
        Model:    "claude-haiku-4-5-20251001",
        System:   "You are a session summary quality judge.",
        Messages: []yourClient.Message{{Role: "user", Content: prompt}},
    })
    if err != nil {
        return summaryeval.CompletionResult{}, err
    }
    return summaryeval.CompletionResult{
        Text:             resp.Content[0].Text,
        ModelUsed:        resp.Model,
        PromptTokens:     resp.Usage.InputTokens,
        CompletionTokens: resp.Usage.OutputTokens,
    }, nil
}

// Build the judge.
j := summaryeval.NewJudge(myCompleter, summaryeval.JudgeOptions{
    ModelHint:          "haiku",
    IncludeSuggestions: true,
})

// Score absolute-mode (no reference, daemon use case).
result, err := j.Score(ctx, sessionName, nil, candidateSummary)

// Or paired-mode against a golden reference.
result, err := j.Score(ctx, sessionName, &goldenRef, candidateSummary)
```

## Daemon integration (planned)

When `OX_SUMMARY_JUDGE=on` (or daemon config flag), the daemon runs the judge after each anti-entropy summarization:

```
<ledger>/.sageox/cache/summary-judge/<session_name>.json
```

Contains the full `JudgeResult`. Never committed; never LFS-uploaded; gitignored via the existing `.sageox/cache/` rule. Log line emits the cache path via slog:

```
slog.Info("summary_judge", "session", name, "cache_path", cachePath, "result", jr)
```

`jr.LogValue()` already implements `slog.LogValuer` for key=value emission, consistent with the rest of ox telemetry.

## Prompt design

The judge prompt (`BuildJudgePrompt`) is a single user message asking the LLM to:

1. Score five dimensions (same as deterministic rubric: title, summary, key_actions, outcome, aha_moments) on a 0.0–1.0 scale
2. Provide an overall verdict (not a strict weighted average — holistic)
3. Write a brief rationale (capped by `MaxRationaleChars`, default 600)
4. Optionally provide concrete actionable suggestions

Response is a strict JSON object. The parser tolerates:
- \`\`\`json fences
- Surrounding prose (extracts the outermost `{...}`)
- Out-of-range scores (clamped to [0, 1])
- Empty-string suggestions (filtered)

Malformed JSON surfaces as an error so CI sees the failure instead of getting a silent 0-score.

## Cost notes

Per-session cost (approximate, haiku-4-5 pricing):
- Paired mode: ~1,000 prompt tokens + ~200 completion tokens ≈ $0.0005
- Absolute mode: ~800 prompt tokens + ~200 completion tokens ≈ $0.0004

At 100 sessions/day for a team running it on every anti-entropy run, that's ~$1.50/month. Opus-class models are ~5× more expensive but usually unnecessary for this task.

## Non-goals for v1

- Shipping an Anthropic SDK binding — intentional. `Completer` keeps pkg/summaryeval stdlib-only.
- CLI command — followup bead.
- Daemon auto-enable — followup bead; ships with `OX_SUMMARY_JUDGE=on` gating only.
- Multi-judge consensus — if and when it matters.
