# Summary Eval Golden Corpus

Hand-reviewed reference summaries for real ox team sessions. This corpus is
the quality bar that `pkg/summaryeval` scores candidate distiller output
against. The 18 sessions were chosen to exercise the summarizer across
length, author, and kind-of-work axes — not to be representative of typical
usage.

All 18 sessions are from the public `sageox/ox` repo's own ledger. Do not
commit absolute filesystem paths (`/Users/...`), private credentials, or
PII beyond what already appears in public commit logs.

## Sessions

| Session | Lines | Outcome | Title | Coverage role |
|---|---|---|---|---|
| 2026-03-11T07-18-ryan-OxZgb1 | 2233 | success | Index GitHub PRs and issues into CodeDB | Very-long multi-topic architectural session |
| 2026-03-11T12-28-ryan-OxStg1 | 939 | success | Collapse daemon-per-worktree into one daemon per repo | Long refactor driven by measured resource waste |
| 2026-03-06T17-19-30-ryan-OxRsd1 | 660 | success | Implement Claude memory import into the ledger | User corrections mid-implementation, review-driven fixes |
| 2026-02-28T10-56-ryan-OxPROV | 628 | success | Break v4 memory spec into epics and start implementation | Planning + parallel-agent kickoff |
| 2026-04-06T02-21-ryan-Oxkxlv | 564 | partial | Fix ox prime agent-ID reuse after /clear | Partial outcome; improves over a broken low-quality prior summary |
| 2026-02-26T16-06-ryan-Ox9KVC | 547 | success | Generalize agent hook lifecycle across 9 agents | Horizontal refactor touching many integrations |
| 2026-03-24T16-55-ryan-OxmFlf | 434 | success | Fix daemon LFS upload gap and backfill test coverage | Bug fix + broad coverage audit |
| 2026-03-25T07-56-ryan-Ox9fDi | 375 | success | Ship ox teams with unified team discovery | Iterative UX refinement |
| 2026-03-23T08-34-ryan-OxmyBF | 346 | success | Add 5-layer progressive disclosure for video discussions | Design-proposal-driven implementation with rebase recovery |
| 2026-03-15T12-36-ryan-OxPTAs | 309 | success | Daemon self-restart, log discoverability, and orphan PID fix | Three unrelated fixes in one session |
| 2026-03-12T19-02-user-OxW4Ag | 242 | success | XML prime output, session liveness, agent list rename | Heterogenous small work; generic `user` author |
| 2026-04-07T19-54-ryan-OxiU0Y | 230 | partial | Fix capture-prior silently dropping tool calls | Mid-implementation ending on a redesign |
| 2026-04-24T19-00-ajit-OxrL5t | 213 | success | Surface titles and agent-friendly JSON in ox session list | Agent-UX feature; author is Ajit (not Ryan) |
| 2026-04-07T01-47-ryan-Ox01yU | 221 | success | Fix daemon FD leak and add a generic leak guard | Specific bug + systemic guard |
| 2026-03-22T09-47-rsnodgrass-Ox6fQF | 220 | success | Build whisper and murmur agent-communication infrastructure | "Continue from where you left off" with almost-no-context opener |
| 2026-03-14T09-50-ryan-OxwyyL | 184 | success | Upgrade go-git v5 to v6 with regression tests | Dependency upgrade with focused evaluation step |
| 2026-04-01T14-31-ajit-banerjee-OxMwYD | 178 | success | Simplify cart CLI and add Claude skills | Ajit session; prior summary failed outright (rate-limit error) |
| 2026-03-26T08-24-galexy-Oxbooa | 86 | success | Fix 19 golangci-lint issues across 10 files | Shortest session; third author (Galex); strong prior summary |

## Schema

Each `<session>/reference.json` matches `summaryeval.GoldenSession`:

```json
{
  "name": "<session_name>",
  "notes": "why this session is in the corpus",
  "reference": {
    "title": "5-10 word action-oriented title",
    "summary": "3-6 sentence narrative paragraph",
    "key_actions": ["concrete action", "..."],
    "outcome": "success|partial|failed",
    "aha_moments": [{"seq": 1, "type": "question"}],
    "topics_found": ["topic", "..."]
  }
}
```

## Adding sessions

1. Pick a session that covers an axis this corpus doesn't yet exercise
   (new author, new session kind, different length bucket, different
   outcome).
2. Read the raw.jsonl (head + tail for large sessions) and any existing
   summary.json. Do not trust the existing summary — it's often the
   low-quality output we want to improve on.
3. Write `reference.json` following the schema above. Titles must be real
   (not `Session recording`, not a date, not a bash command). Summaries
   must be narrative and honest about outcome.
4. Add a row to the table above.
5. Run `go test ./pkg/summaryeval/ -run TestLoadCorpus_Curated` to verify.
