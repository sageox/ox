# Cart Analysis UX — Target Interaction

Design goal for `ox cart-analyze`. The JSON output should enable an AI coworker
to produce this conversation without reading the full `work_items` array until
the user zooms in.

## Pyramid scanning contract

| Turn type | AI reads | Pyramid level |
|-----------|----------|---------------|
| "where are we?" | `overall.l4` + `by_status.in_progress` | L4, L8 |
| "show me the carts" | `carts_by_status` listing | L2 scan → full titles |
| "what's blocking?" | `blockers` array | L8 |
| "what did X do?" | filtered `work_items` for one person | full detail |

The AI coworker should never need all 59 work items for a status question.

## Target interaction (digital twin, all_windows)

---

**User:** where are we?

**AI coworker:**

57/59 carts completed, 2 in progress across 6 coworkers.

**Active right now:**

| Cart | Assignee | Status |
|------|----------|--------|
| API rate limiter | bob | in progress |
| Daemon metrics collection | carol | in progress |

**Completed:** 57 carts closed (49 tasks, 6 features, 3 bugs, 1 epic).

```
alice    14 done
bob      12 done    + 1 active
carol     9 done    + 1 active
dave     10 done
eve       7 done
frank     5 done
```

No blockers.

---

**User:** can I see the carts? group by work area

**AI coworker:** *(scans per-cart L2 pyramids to cluster, then shows full titles)*

**Sync pipeline** — 20 carts, March 10-12

| Cart | Assignee | Type |
|------|----------|------|
| Sync pull refactor | alice | task |
| Sync core rewrite | alice | epic (P0) |
| Sync API v2 | bob | task |
| ... | ... | ... |

**Auth** — 12 carts | **CLI** — 10 carts | **API** — 5 carts | **Infra** — 10 carts

*(full tables per group)*

---

**User:** what's getting in the way?

**AI coworker:**

Nothing is blocked. Both active carts are progressing.

The sync rewrite (March 10-12) was the only period that showed strain — started
as 2 people, pulled in all 6 by Day 3 when scope proved bigger than planned.
Resolved cleanly, all 20 carts closed.

---

**User:** what have we learned?

**AI coworker:**

1. **Pipeline rewrites are team-wide events.** alice's pull.go refactor cascaded
   into API, daemon, CLI, tests, and docs by Day 3. Plan for full convergence
   from the start next time.
2. **Auth knowledge is concentrated in alice** (12/14 auth carts). Consider
   spreading context.
3. **Docs kept pace with code.** frank shipped same-day docs on both Day 2 and
   Day 3. Worth preserving as a team norm.

---

**User:** what's next?

**AI coworker:**

**Finish active:** bob's API rate limiter, carol's daemon metrics.

**Follow-ups (no carts yet):**
- Manual-review state UX — the sync state machine has the state, no workflow
- Auth context spreading — pairing session on token/middleware code
- SSE streaming load testing for sync API v2

---

**User:** tell me more about alice on the sync rewrite

**AI coworker:** *(now reads full work_items for alice + OxA\*)*

4 carts across 3 days:

- **Day 1** — Sync pull refactor. Split monolithic pull into fetch/merge/apply.
- **Day 2** — Merge conflict resolution (P1). 3-way resolver with manifest rules.
  The inflection point.
- **Day 3 AM** — Sync core rewrite (P0 epic). State-machine pipeline. Pulled
  in the whole team.
- **Day 3 PM** — Sync state machine. Added conflict-detected and manual-review
  states from edge cases carol and bob surfaced.

alice drove the arc: reconnaissance → proof-of-concept → execution.

---

## Design principles

1. **Lead with status, not data.** The first answer is "57/59 done" not a table.
2. **Cart listing on request.** Show the actual cards when asked, grouped by
   area or status — this is what the user wants to scan.
3. **Pyramid = permission to skip.** The AI coworker doesn't read all work items
   unless the user zooms in. L2 pyramids (~2 tokens/cart) enable scanning 100+
   carts in a single pass.
4. **Synthesis over repetition.** "What we learned" is 3 cross-cutting insights,
   not 57 per-cart summaries.
5. **Follow-ups from gaps.** "What's next" proposes work that follows from the
   data (missing UX for a state, concentrated knowledge) not generic suggestions.
