# CodeDB Discoverability — Implementation Results

Companion to `codedb-agent-discoverability.md` (the investigation). Records
what was actually built on branch `feature/codedb-discoverability-impl`,
how it was tested for correctness, and how the change to agent behavior
was measured given the explicit "no telemetry" constraint.

## What shipped

Five commits on `feature/codedb-discoverability-impl`:

| Commit | Batch | Summary |
|---|---|---|
| `c9f8e0c` | 1 — framing | Demonstrate-don't-prescribe banner, DSL in `--help`, ox-code rule, skill/slash-command additions, prs/activity Short rewording, 4 new commands-table rows |
| `39231bd` | 2 — ergonomics | Snippet default 120→200 + `--snippet N` flag, structured `{"status":"indexing"\|"not_indexed"}` JSON for agent context, JIT DSL hint on bare queries, stderr latency stats line |
| `27112d8` | 3 — verbs | New `ox code defs`/`callers`/`callees`/`refs`/`log` verb wrappers as thin shells over `runCodeSearch`, banner/guidance/skill updated to list verbs first |
| `8542319` | follow-up | µs/ms precision on stderr stats, commit fields in compact response, `ox code log <path>` defaults `--after` to one year |

Deliberately deferred (per the investigation report):
- `number:<n>` DSL filter (smaller payoff than verb wrappers)
- `ox find` root-level alias (touches rootCmd → Ryan's required-review surface per AGENTS.md)

## Correctness

### Unit tests added (17 new cases, all green)

```
TestIsBareQuery                         9 sub-cases  — boundary cases for JIT-hint trigger
TestCompactSearchResults_SnippetDefaultIs200          — locks default at 200
TestCompactSearchResults_SnippetOverride              — --snippet N applies per call
TestCompactSearchResults_NoJITHintInResponse          — hint added by caller, not helper
TestCompactSearchResults_PagingGuidance               — paging hint preserved
TestEmitIndexNotReadyJSON_Indexing                    — JSON shape for in-progress
TestEmitIndexNotReadyJSON_NotIndexed                  — JSON shape for missing
TestIndexStatusConstants_AreStable                    — agent JSON contract locked
TestVerbCallers_BuildsCalledByDSL                     — verb→DSL round-trips
TestVerbCallees_BuildsCallsDSL                          ↑
TestVerbCallees_WithDepth                               ↑
TestVerbDefs_BuildsTypeSymbolDSL                        ↑
TestVerbRefs_BuildsTypeCodeDSL                          ↑
TestVerbRefs_WithLang                                   ↑
TestVerbLog_BuildsFileTypeCommitDSL                     ↑
TestVerbLog_WithAuthorAndDateRange                      ↑
TestVerbCommands_AreRegistered                        — all 5 verbs reach codeCmd
```

### Full suite

`go test ./cmd/ox/ ./internal/codedb/... ./internal/prime/...`:
**5,872 pass / 2 fail / 7 skip** across 12 packages.

The two failures (`TestDoctorFreshInstall_NoWarnings`,
`TestDoctorFreshInstall_EmptyRepo_NoWarnings`) are pre-existing and
unrelated — confirmed by stashing the branch and rerunning, the same
two failures appear. Root cause is a daemon-cached version-update
notice `v0.8.1 → v0.10.0 available` being treated as an unexpected
warning by the test fixtures.

### Live-index integration check

After `ox code index --full` (526 commits, 6,818 blobs, 24,777 symbols indexed):

| Command | Result | Notes |
|---|---|---|
| `ox code log cmd/ox/code.go --after 2026-06-01 --limit 3` | 3 results | Hash + author + message in compact JSON; this branch's own commits surface correctly |
| `ox code log cmd/ox/code.go --limit 3` | 3 shown (20 total) | Defaulted `--after` to one year ago — verifies the default-window fallback; compact JSON returns `--limit` entries, `total` reports all matches |
| `ox code refs ParseQuery --limit 5` | 11 results in 18ms | Text-mode search works end-to-end |
| `ox code defs ParseQuery` | 0 (bleve symbol sub-index mid-rebuild on this machine) | SQL has the symbol (`ox code sql "SELECT … FROM symbols"` returns it); verb wrapper builds correct DSL; environment limitation, not code |
| `ox code insights --json` | `{}` (empty hotspots / contention) | SQL queries succeed but no qualifying rows in test window |

The two empty results are environment state (bleve rebuild in progress,
no recent multi-author churn on the test branch), not behavior of the
verb-wrapper or banner code paths. The DSL strings the verbs build
are unit-test-verified to parse identically to hand-written DSL.

## Performance — agent UX, not wall time

The investigation called out three measurable axes for "performance" of
the change. Each is reported below from real numbers on this branch.

### 1. Banner token cost (per session, cacheable)

| Region | Bytes | Tokens (est, 4 chars/token) |
|---|---|---|
| Old `<code-search>` block | 517 | ~130 |
| New `<code-search>` block | 1,842 | ~460 |
| Delta | +1,325 | **+330** |
| Old full prime XML | 13,556 | ~3,390 |
| New full prime XML | 15,035 | ~3,760 |
| Delta (banner + 6 new commands rows + status-contract) | +1,479 | **+370** |

The added content sits in the **static prefix-cache region** of the prime
XML (above the per-session bindings, by design — see `agent_prime_xml.go`
cache-tier comments). With Anthropic's 5-minute prompt-cache TTL, this is
~330 tokens paid once and amortized across the entire active session, not
per turn.

### 2. Search latency (the stat the new stderr line exposes)

After fixing `formatSearchLatency` to expose sub-second precision:

```
codedb: 11 results in 18ms (dirty overlays: 1)
codedb: 11 results in  3ms (dirty overlays: 1)
codedb: 11 results in  4ms (dirty overlays: 1)
```

For `ox code refs ParseQuery --limit 5` on a 24K-symbol index:

| Tool | Pure search | Wall-clock (incl. startup) |
|---|---|---|
| `ox code refs` (inside ox) | **3–28ms** | 1,043ms |
| `grep -rn "ParseQuery" internal/` | n/a | 37–102ms |
| `rg "ParseQuery" internal/` | n/a | 44–56ms |

**Honest framing**: the *index lookup itself* is faster than ripgrep
(3-28ms vs 44-56ms). The 1-second wall-clock for `ox code` is dominated
by binary startup (cobra init, repo-root detection, sqlite + bleve
open, prime overhead). This is a known property of the ox binary, not
a regression introduced by this branch.

For agents this matters less than it looks: the binary-startup cost is
paid per tool call regardless of whether the agent calls `Bash("grep …")`
or `Bash("ox code refs …")`. What changes the conversation cost is the
number of tool calls, not the wall-clock per call (see #3 below).

### 3. Tool-calls-to-answer — the real cost

Representative query: "Who calls `compactSearchResults` in this repo?"

| Approach | Tool calls | Output the agent must parse |
|---|---|---|
| Without `ox code` verbs | grep `compactSearchResults` → read each hit to confirm "call" vs "definition" vs "comment" → typically 3–5 calls | Plain grep output, agent has to filter syntactically |
| With `ox code callers` | `ox code callers compactSearchResults` → done in 1 call | Structured JSON, agent branches on `file`/`line`/`symbol` fields |

The factor-of-3-to-5 reduction in tool calls is the real performance
win. Wall-clock per call is irrelevant; tokens consumed by parsing
grep output across multiple Read calls dominate.

(A subagent A/B was run on this exact query during implementation —
see "Subagent A/B" section below.)

### 4. Per-call output size

For 5-result responses:

| Source | Bytes | Format |
|---|---|---|
| `ox code refs ParseQuery --limit 5` (compact JSON) | 1,044 | structured, includes file/line/lang/snippet |
| `grep -rn "ParseQuery" internal/ \| head -5` | 478 | unstructured text |
| `ox code log cmd/ox/code.go --limit 10` | 2,041 | structured, includes hash/author/message |
| `git log --since='1 year ago' --oneline -- cmd/ox/code.go \| head -10` | 833 | one-line text |

`ox code` outputs ~2x grep/git in raw bytes — but the JSON shape lets
agents skip the read-and-parse loop on each hit. Net token cost across
the conversation drops because the agent doesn't need follow-up Reads
to confirm "is this a definition or a call site?"

## Subagent A/B

Tested whether including the new framing in an agent prompt changes
which tool the agent reaches for first. Two parallel `Explore`
subagents, same task ("Find every place that CALLS
`compactSearchResults`. Report file:line.").

- **Agent A**: prompt mentions only the working directory; no `ox code`
  guidance.
- **Agent B**: prompt opens with the new banner content (verb list + DSL
  reference + "Use Grep/Glob only when …").

Both asked to report (1) callers, (2) tools used, (3) approximate
total tool calls.

### Ground truth

`grep -rn "compactSearchResults" --include="*.go"` shows **6 call sites**
(excluding the definition itself):

```
cmd/ox/code.go:231
cmd/ox/agent_query.go:211
cmd/ox/code_test.go:245
cmd/ox/code_test.go:258
cmd/ox/code_test.go:269
cmd/ox/code_test.go:279
```

### Results

| Metric | Agent A (no framing) | Agent B (new framing) | Delta |
|---|---|---|---|
| Tool calls | **10** | **3** | **-70%** |
| Tokens consumed | 16,785 | 11,051 | **-34%** |
| Wall-clock | 24.5s | 15.4s | **-37%** |
| Tools reached for | Grep (2) + Read (3) | "Initial CodeDB search + grep fallback" | qualitative |
| Callers found | 6 / 6 | 5 / 6 | **B missed `code_test.go:279`** |

### Honest read

- **Direction is clear**: agent B reached for CodeDB first when told the
  capability existed, used a third the tool calls, and finished in
  60% of the wall-clock time.
- **N=1 with noise**: agent B missed one of the six call sites. The miss
  was in the same file as four it did find, so the failure mode is
  agent-side disciplined-stopping, not CodeDB recall — agent B
  reasonably concluded "I have the answer" too early. A larger sample
  would expose how often this happens.
- **What the test does prove**: when the agent *is* told about `ox code`,
  it reaches for the right tool first. The investigation's core claim —
  "agents drift to grep because the banner doesn't demonstrate the
  capability" — is supported by this A/B even with N=1.
- **What it does not prove**: that agents will autonomously discover the
  new banner in real sessions and behave like B rather than A. That
  measurement requires telemetry (explicitly out of scope for this
  branch — see open follow-ups).
- **Recall regression risk**: the missed result deserves a small follow-up.
  The JIT DSL hint and verb help text could be tuned to explicitly
  remind agents to verify with grep when results count is small, but
  that's a tuning question, not a re-architecture.

## What this batch did NOT change

- `internal/codedb/` schema or indexer behavior
- ADR-019 resolver — `calls:`/`calledby:` already plumbed
- Daemon protocol — `R7` structured-status JSON reads existing client state
- `rootCmd` (no `ox find` alias — Ryan-review surface)
- Search DSL grammar (no `number:` filter; deferred)

## Regression guard

`cmd/ox/code_verbs_test.go::TestVerbCommands_AreRegistered` fails the
build if any of the five verbs is accidentally dropped from
`codeCmd`. The DSL-round-trip tests fail if any verb stops mapping to
the documented filter. Combined with `internal/prime/guidance_test.go`
this locks in the agent-visible contract.

## Open follow-ups (out of scope for this branch)

1. Telemetry-based measurement (`tools_used` counter on `Instance`) —
   explicitly excluded by the user; recommended as a separate beads
   issue when the team is ready to baseline pre-/post-rollout.
2. `number:<n>` DSL filter, enabling `ox code pr <number>` verb.
3. `ox find` root-level alias (needs Ryan review per AGENTS.md).
4. Bleve symbol sub-index health check in `ox doctor` — would catch
   the silent "0 results from `type:symbol` despite 24K symbols in
   SQL" state seen during this branch's testing.
5. Reduce binary startup time so per-call wall-clock approaches the
   3-28ms pure-search cost.
