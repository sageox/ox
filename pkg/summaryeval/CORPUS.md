# Golden Corpus Curation

The eval harness scores candidate summaries against hand-reviewed reference summaries. Quality of the eval is bounded by quality of the corpus.

## Layout

```
<corpus-dir>/
  <session-name-1>/
    reference.json       # GoldenSession { name, notes, reference }
  <session-name-2>/
    reference.json
  ...
```

Each subdirectory with a `reference.json` is one golden session. Directories without one are skipped silently.

## Choosing sessions

Target **20–30 diverse sessions** for v1. Diversity matters more than quantity:

- **Length**: short sessions (< 50 entries), medium (100–500), long (1000+)
- **Agent type**: Claude Code, Aider, Amp, Pi — at least one each if you have recordings
- **User**: sessions from different humans produce different interaction patterns
- **Outcome**: successes, partials, and failures — the rubric scores outcome directly
- **Content**: bug fixes, feature work, refactors, debugging sessions, design discussions

## Writing the reference

Start from whatever existing summary the distiller produced, then **edit it by hand** until it's the summary you wish every session had. Put the session recording open alongside. Rules of thumb:

- **Title**: 5–10 words, captures actual work performed. Not a date, not a file name, not a command.
- **Summary**: one paragraph. Motivation + approach + outcome. Prioritize what a teammate who wasn't there would need to know.
- **Key actions**: 3–10 concrete bullets. Real verbs. Specifics, not generalities.
- **Outcome**: `success` / `partial` / `failed` exactly. Be honest — a session that ended with an open PR but failing CI is `partial`.
- **Aha moments**: 3–5, no more. Only genuinely pivotal ones. A user question that redirected the work is often more valuable than an assistant insight.

Include `notes` describing why this session was chosen — helps future curators understand the corpus's coverage intent.

## Running the eval

```go
corpus, _ := summaryeval.LoadCorpus("./corpus")
// ... produce candidates map[string]summaryeval.Summary from distiller run ...
report := summaryeval.ScoreCorpus(corpus, candidates, summaryeval.DefaultWeights(), &summaryeval.Gates{
    MinOverall: 0.70,
})
if len(report.GatesFailed) > 0 {
    // fail CI
}
```

## Interpreting scores

- **Overall mean ≥ 0.85**: summaries are strong. Baseline for a well-tuned pipeline.
- **0.70–0.85**: acceptable but has room. Look at per-dimension means to find the weak spot.
- **< 0.70**: something regressed. Either the pipeline or the rubric — investigate before shipping.

The scorer is **lexical, not semantic**. A candidate that paraphrases heavily using different vocabulary can score lower than one that copies reference wording. This is a known tradeoff of the v1 deterministic scorer — the LLM-judge path (future work) would address it. In the meantime, keep reference vocabulary reasonable (don't load the reference with rare synonyms the distiller wouldn't naturally produce).

## Adding a session

1. Pick a session from your ledger: `ox session list`
2. Create `<corpus-dir>/<session-name>/reference.json` with `{name, notes, reference: {title, summary, key_actions, outcome, aha_moments, topics_found}}`
3. Run the eval — a fresh golden session paired with a candidate should score near 1.0. If not, either the candidate is poor or the reference is over-specific.

## Do not

- Put secrets in reference summaries. They commit to source control with the corpus.
- Sync the corpus via LFS. These files are small markdown + JSON; git handles them fine.
- Let the corpus drift silently. Re-review entries quarterly; sessions evolve as conventions change.
