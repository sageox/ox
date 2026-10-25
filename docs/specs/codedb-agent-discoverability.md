# CodeDB Agent Discoverability

Investigation into why AI coworkers (Claude Code, Cursor, Codex, Windsurf) rarely
reach for `ox code …` even when it would help, plus ranked recommendations and
concrete proposed patches.

Audience: ox maintainers. Status: **pre-implementation snapshot** (investigation
dated 2026-05-28). The gaps it documents — no DSL grammar in `ox code search
--help`, no verb wrappers, structured index statuses as proposed work — are the
*before* picture. PR #664 implements the load-bearing recommendations; read the
current-state claims below (e.g. "none of it is documented", "verb wrappers out
of scope", "proposed index statuses") as the state that motivated the change,
not the shipped result. See `codedb-discoverability-impl-results.md` for what
actually landed.

---

## TL;DR

`ox code` is *not* invisible — `ox agent prime` already emits a `<code-search>`
banner instructing the agent to "PREFER `ox code search` over Grep/Glob/ripgrep."
The agent still drifts to `Grep`/`Glob`/`Read` because:

1. The banner sells `ox code` as **a faster grep** instead of as **the only tool
   that can answer call-graph, PR, comment, and history questions**. Pre-trained
   tool-use instincts (grep/ripgrep/find) beat a soft "PREFER" instruction.
2. The DSL — `type:`, `calls:`, `calledby:`, `confidence:`, `/regex/`, `before:`,
   `after:`, `author:`, `message:`, `OR` — is the **unique value**, and it is
   surfaced *nowhere* the agent can see (not in `--help`, not in the prime
   banner, not in any skill file, not in `.claude/rules/`).
3. The compact output deliberately truncates snippets to 120 chars; agents read
   the snippet, see `…`, and learn "ox code returns less than grep." That is
   negative reinforcement on the very first call.
4. The verbs the agent looks for (`callers`, `callees`, `who calls X`) are not
   verbs in the CLI — they are filter values (`calledby:X`, `calls:X`). Agents
   pattern-match on verbs, not on undocumented DSL.
5. Insights / prs / activity (the genuinely non-grep capabilities) are listed in
   `ox code --help` with one-line descriptions and zero examples. The agent
   cannot tell from the help text that these subcommands have no grep
   equivalent.

The highest-leverage fix is to **change the framing**: stop selling `ox code
search` as a grep replacement; start selling it as the SQL-and-graph layer over
this repo, with example queries grep cannot answer. Concrete patches at the end
of this doc.

---

## Findings

### What `ox code` offers today

Inspected `cmd/ox/code*.go`, `internal/codedb/search/query.go`, `cmd/ox/code.go:188` (`compactSearchResults`).

| Subcommand | Surface | Default output | Unique vs. grep? |
|---|---|---|---|
| `search <query>` | full DSL, paged JSON | compact JSON (top 10, 120-char snippets) | yes — DSL + indexed PR/issue/comment/symbol |
| `insights` | hotspots, contention, recent commits, open PRs/issues | human text; JSON when agent-invoked | yes — no grep equivalent |
| `prs` | PR triage (stalled / age / activity rankings) | JSON | yes — needs indexed comments/reviews |
| `activity` | event clusters for the fact-extractor pipeline | JSON | yes, but internal-feeling |
| `status` | index health, freshness, next-check | human text or JSON | n/a (operational) |
| `index` | build/rebuild | n/a | n/a |
| `sql` (hidden) | raw SQL on the SQLite store | TSV | yes |
| `stats` (hidden alias for `status`) | back-compat | n/a | — |
| `query` (hidden alias for `search`) | back-compat | n/a | — |

### Search DSL keywords (the actual unique value)

From `internal/codedb/search/query.go:82-262`:

```
type:{code,diff,commit,symbol,comment,pr,issue}
repo:<name>[@<rev>]      -repo:<name>
file:<glob>              -file:<glob>
lang:<id> / language:    -lang:<id>
rev: / revision:<sha>
count:<N>                case:yes
patterntype:{literal,keyword,regexp}
author:<name>            -author:<name>
before:<date>            after:<date>
message:<text>           -message:<text>
select:{repo,file,symbol,symbol.<kind>}
calls:<name>             # symbol_edges-resolved (ADR-019)
calledby:<name>          # symbol_edges-resolved (ADR-019)
returns:<type>
depth:<1..10>            # call-graph depth
ckind: / comment-kind:<kind>
state:<pr_state>
confidence:{extracted,inferred,ambiguous}   # ADR-019
OR                       # boolean across groups
/regex/                  # forced regex
```

This is a Sourcegraph-class query DSL. **None of it is documented in
`ox code search --help`**, which currently shows two flags (`--full-json`,
`--limit`) and zero examples.

### Where the agent learns about `ox code` today

| Surface | Mentions `ox code`? | Mentions the DSL? |
|---|---|---|
| `ox agent prime` XML output, `<code-search>` block (`cmd/ox/agent_prime_xml.go:128-136`) | yes | **no** |
| `ox agent prime` XML output, `<commands>` table (via `internal/prime/guidance.go:73-87`) | yes, two rows: `search` + `insights` | **no** |
| `cmd/ox/code.go` Long text (`Long: "Search git history and current code…"`) | self-referential | **no** |
| `cmd/ox/code_search.go` (i.e. `codeSearchCmd.Short/Long`) | "Search indexed code using queries" | **no** |
| `.claude/rules/ox.md` (project rule shipped to Claude Code) | yes, two rows in a table | **no** |
| `claude-plugin/skills/ox/SKILL.md` (the Claude skill for ox) | **no** — only `prime`/`session`/`status`/`doctor` | **no** |
| `extensions/claude/commands/ox.md` (slash-command help) | **no** | **no** |
| `AGENTS.md` at repo root | **no** | **no** |
| `docs/reference/code/*.mdx` (generated CLI docs) | yes, autogen from cobra — inherits emptiness | **no** |

### What the agent actually sees on session start

Verbatim from the session-start hook on this very session:

```
<code-search status="indexed">
This repo has a live code search index. PREFER `ox code search "<query>"` over Grep/Glob/ripgrep for:
- Cross-file symbol search, function lookup, type definitions
- Git history, diffs, and blame queries
- Exploratory searches where you don't know the exact file
Use `ox code insights` before planning multi-file changes (shows hotspots, contention, open PRs).
Reserve Grep/Glob for: exact-string matches in a known file, or when ox code search returns no results.
</code-search>
```

This is a *prescription*, not a *demonstration*. It tells the agent **what to
do** but never shows **what `ox code` can do that grep cannot**.

---

## Friction Analysis

Ranked by likelihood of being the dominant cause of `ox code` underuse.

### 1. Framing as "faster grep" trips pre-trained instincts

LLMs are trained on millions of repos where `grep -rn`, `rg`, `find` are the
universal solution. When a soft "PREFER" instruction competes with deeply
ingrained tool-use priors, the priors usually win — especially when the agent
sees no demonstration of the new tool's *differentiating* capability. The
banner currently lists three use cases (`Cross-file symbol search`,
`Git history, diffs, blame`, `Exploratory searches`) that grep *can* do
adequately. The agent reads that and concludes the tools are interchangeable
with `ox code` being slightly less familiar.

The real differentiator is the things grep **cannot do**: `type:pr`/`type:issue`
search over indexed GitHub data, `calls:`/`calledby:` resolved call graph,
`type:comment ckind:todo`, `author:foo before:2026-04-01`, `confidence:extracted`.
None of this appears in any agent-visible surface.

### 2. The DSL is invisible to the agent

`ox code search --help` shows:

```
Flags
─────
      --full-json          full uncompacted JSON output (~6x more context tokens)
  -h, --help               help for search
      --limit int          max results to return
```

Zero examples. Zero DSL grammar. The DSL is documented only in source
(`internal/codedb/search/query.go`) and in scattered `docs/ai/specs/` files. An
agent that runs `--help` and sees this concludes `ox code search` is grep with
fewer features.

### 3. Snippet truncation looks like a downgrade

`compactSnippet` (`cmd/ox/code.go:234-257`) collapses whitespace and truncates
to 120 chars per result. The first time an agent calls `ox code search`, it
sees results like:

```
"snippet": "func ResolveSessionRecording(projectRoot, kbID, kbType string) ResolvedSessionRecording { …"
```

…and then has to `Read` the file anyway to get the function body. That round
trip is more expensive than just running `Grep`+`Read`. The compact format
optimizes token cost on the *index lookup* but loses the natural agent loop
("find → read → understand").

### 4. The unique-value verbs are not verbs

Agents pattern-match on verbs. They will try `ox code callers <name>`, `ox code
who-calls <name>`, `ox code refs <name>`. They will not try
`ox code search '' calls:<name>` because that grammar requires reading
`query.go`. ADR-019 just landed a high-value call-graph feature that is
*completely hidden* behind DSL filters with no verb-mode wrapper.

### 5. Error modes train the agent off the tool

Two specific surfaces train the agent to fall back to grep:

- `codeSearchCmd` returns `code index is currently being built — search is
  unavailable until indexing completes` (`cmd/ox/code.go:114`) as a hard error.
  An agent that hits this once on a fresh clone is unlikely to retry.
- `codeInsightsCmd`, `codeActivityCmd`, `codePRsCmd` return
  `no code index found — run 'ox code index' first` (`cmd/ox/code_insights.go:84`,
  similar in others). Grep never asks the agent to run a setup command.

In both cases the JSON return shape would be more agent-friendly:
`{"status":"indexing","ready_estimate_s":30}` is recoverable; a stderr error is
"abandon the tool."

### 6. Stale-index anxiety (mostly already-solved, but invisible)

Agents reasonably worry that `ox code search` won't see edits made earlier in
the same session. The dirty-overlay mechanism (`db.AttachAllDirtyIndexes()`,
`cmd/ox/code.go:125`) addresses this, but the agent has no signal that it does.
A one-line in the response or in `status` ("dirty overlays attached: 2 worktrees,
14 modified files") would visibly resolve the worry.

### 7. Skill / rules files don't carry `ox code`

The shipped skill for the ox CLI (`claude-plugin/skills/ox/SKILL.md`) mentions
`ox agent prime`, `ox status`, `ox doctor`, `ox init`, `ox conventions`, but
**not** `ox code`. The slash-command file (`extensions/claude/commands/ox.md`)
similarly omits it. Skills are activated by intent in many host environments;
omission means `ox code` is never reached via the skill route.

### 8. Naming bites a little

`ox code` reads to a pre-trained model as plausibly "a code generator" or "a
code reviewer." `ox find`, `ox where`, or `ox grep` would map better to intent.
This is a lower-order effect but compounds with the framing problem above.

### 9. No latency / stats signal in output

Agents have no idea whether `ox code search` is faster, slower, or comparable to
grep. A stderr one-liner (`matched 247 results in 12ms via codedb`) would
calibrate the agent on every call.

---

## Recommendations (ranked by impact / effort)

### R1 — Rewrite the prime `<code-search>` banner to demonstrate, not prescribe  [high impact / low effort]

Replace the current prescription with concrete examples showing what grep cannot do:

```diff
<code-search status="indexed">
-This repo has a live code search index. PREFER `ox code search "<query>"` over Grep/Glob/ripgrep for:
-- Cross-file symbol search, function lookup, type definitions
-- Git history, diffs, and blame queries
-- Exploratory searches where you don't know the exact file
-Use `ox code insights` before planning multi-file changes (shows hotspots, contention, open PRs).
-Reserve Grep/Glob for: exact-string matches in a known file, or when ox code search returns no results.
+This repo has a live code search index. Use `ox code` for things grep CANNOT do:
+
+  ox code search "ResolveSession" type:symbol          # symbol defs, not text matches
+  ox code search "" calledby:authenticate              # who calls authenticate() (call graph)
+  ox code search "" calls:Handler depth:2              # what authenticate calls, 2 hops out
+  ox code search "rate limit" type:pr                  # indexed PR titles/bodies/comments
+  ox code search "TODO" type:comment ckind:todo        # indexed source comments by kind
+  ox code search "migration" author:rupak after:2026-04-01  # git log + content together
+  ox code prs --sort stalled --limit 5                 # PR triage (no grep equivalent)
+  ox code insights                                     # hotspots, contention, open PRs/issues
+
+DSL keywords: type:{code,symbol,diff,commit,comment,pr,issue}, repo:, file:, lang:,
+author:, before:, after:, message:, calls:, calledby:, returns:, depth:, confidence:,
+ckind:, state:, OR, /regex/. Negate any filter with a leading `-`.
+
+Use Grep/Glob ONLY when: (a) you need an exact-string match in a single known file,
+or (b) `ox code` returned 0 results and you suspect a typo or stale index.
</code-search>
```

This is the single highest-leverage change. The banner is fully cacheable
(it sits in the prefix-cache region of the prime XML), so adding ~600 tokens
here costs almost nothing per session after the first.

**Patch location:** `cmd/ox/agent_prime_xml.go:127-136`

### R2 — Rewrite `ox code search --help` to expose the DSL  [high impact / low effort]

Currently `codeSearchCmd.Short = "Search indexed code using queries"` with no
`Long`. Add a Long with the DSL grammar and 5–8 example queries. Cobra-generated
docs auto-propagate to `docs/reference/code/search.mdx`.

**Patch location:** `cmd/ox/code.go:100-103` and `cmd/ox/code.go:86-90` (codeCmd
itself). Proposed:

```diff
 var codeCmd = &cobra.Command{
 	Use:   "code",
-	Short: "Search code in this repo",
-	Long:  "Search git history and current code of this repo using queries.",
+	Short: "Search code, symbols, history, PRs, and comments in this repo",
+	Long: `Search the indexed CodeDB for this repo.
+
+CodeDB indexes more than text: symbols, resolved call edges (ADR-019), git
+history, diffs, PR/issue bodies and comments, and source comments. Queries
+use a Sourcegraph-style DSL.
+
+Examples:
+  ox code search "ResolveSession" type:symbol
+  ox code search "" calledby:authenticate
+  ox code search "rate limit" type:pr state:open
+  ox code search "migration" author:rupak after:2026-04-01
+  ox code insights
+  ox code prs --sort stalled
+
+Run 'ox code search --help' for the full DSL.`,
 }
```

…and similarly expand `codeSearchCmd.Long` with the DSL grammar table.

### R3 — Add verb-mode wrappers for the unique-value queries  [high impact / medium effort]

Agents pattern-match on verbs. Wrap the DSL behind verbs so the agent can find
ADR-019 features by guessing:

```
ox code callers <name>    # → search "" calledby:<name>
ox code callees <name>    # → search "" calls:<name>
ox code defs <name>       # → search "<name>" type:symbol
ox code refs <name>       # → search "<name>" type:code
ox code log <path>        # → search "" file:<path> type:commit
ox code pr <number>       # → search "" type:pr number:<n>  (needs a number: filter)
```

These are thin aliases over the existing executor — implementation cost is
small. Listing them in `--help` makes ADR-019's call-graph capability *findable*
by an agent that has not been told the DSL.

**Patch location:** new file `cmd/ox/code_verbs.go`. (Out of scope for this
worktree — recommend as a separate PR.)

### R4 — Add `.claude/rules/ox-code.md` "when to use ox code instead of grep"  [medium impact / low effort]

A short, decision-tree-style rule file. Claude Code reads `.claude/rules/*.md`
on session start; this is the canonical location for tool-selection guidance.
See proposed full text in *Proposed Instructions Diffs* below.

### R5 — Add `ox code` to the shipped Claude skill  [medium impact / low effort]

`claude-plugin/skills/ox/SKILL.md` is currently silent on code search. Adding
a short section unlocks intent-based activation (host environments that route
"search code" / "find function" intents to skills).

### R6 — Loosen the snippet truncation default  [medium impact / low effort]

`compactSnippet` truncates to 120 chars. For typical Go signatures this cuts
mid-arg-list. Bump default to ~200 (still well under a single `Read` page) or
expose `--snippet N`. Better: include 1 line of context above and below the
match line by default (file-line tuple is already in the response, so the
caller can `Read` precisely — but seeing N lines on the *first* call gives the
agent a fair "is this useful?" signal).

**Patch location:** `cmd/ox/code.go:205` (`compactSnippet(snippet, 120)`).

### R7 — Convert hard errors to structured JSON statuses  [medium impact / low effort]

When the index is mid-rebuild:

```diff
-return fmt.Errorf("code index is currently being built — search is unavailable until indexing completes. Run 'ox code status' to check progress")
+return json.NewEncoder(os.Stdout).Encode(map[string]any{
+    "status": "indexing",
+    "ready_estimate_s": 30,  // from daemon CodeStats if available
+    "fallback_hint": "Use Grep/Glob until indexing completes; rerun 'ox code search' afterward.",
+})
```

Agents handle structured statuses; they retreat from stderr errors. Same pattern
for `no code index found`.

**Patch locations:** `cmd/ox/code.go:114`, `cmd/ox/code_insights.go:84`,
`cmd/ox/code_activity.go:45`, `cmd/ox/code_prs.go:63`.

### R8 — Emit a one-line latency / scope signal on stderr  [low impact / low effort]

```
matched 247 results in 12ms via codedb (1.2M symbols, dirty overlays: 2)
```

Calibrates the agent on every call without bloating context (stderr, not
stdout). Aligns with `agent-ux-principles.md` "context efficiency" guidance.

### R9 — Auto-include DSL hint in `guidance` when query is bare  [low impact / low effort]

In `compactSearchResults`, when `len(parsedQuery.Filters) == 0` and the query
is a single token (i.e. the agent is using `ox code` like a grep), append to
the `guidance` field:

```
"For caller/callee lookup, try: calls:<name> or calledby:<name>.
 For symbol defs: type:symbol. For PR/issue text: type:pr / type:issue."
```

Just-in-time DSL discovery for agents that already chose to call the tool.

### R10 — Promote `activity` and `prs` in `ox code --help`  [low impact / low effort]

Both subcommands have `Short` strings that read as internal-tooling
(`Assemble GitHub activity clusters for the fact extractor` — that's a pipeline
description, not an agent-visible value prop). Reword:

```
activity → "Recent GitHub activity (PRs, issues, commits) over a time window"
prs      → "List pull requests ranked for triage (stalled, age, activity)"
```

### R11 — Rename consideration (long-tail, lower priority)

`ox code` reads ambiguously. Consider an alias `ox find` or `ox where` (no
breakage; `ox code` stays). Agents pattern-matching on verbs will guess `ox find
<symbol>` long before `ox code search "<symbol>" type:symbol`.

---

## Proposed Instructions Diffs (concrete patches)

### Patch A — `.claude/rules/ox-code.md` (new file)

```markdown
---
name: ox-code
description: When to use ox code instead of grep/ripgrep/Glob/Read
---
# Rule: When to use `ox code` instead of grep

This repo has a CodeDB index served by the ox daemon. `ox code` queries it.
Grep/ripgrep/Glob still exist for cases the index cannot serve, but the index
is the **default** for the cases below.

## Decision tree

```
Agent intent                                | First tool to try
--------------------------------------------+----------------------------
"Where is function X defined?"              | ox code search "X" type:symbol
"Who calls X?"                              | ox code search "" calledby:X
"What does X call?"                         | ox code search "" calls:X depth:2
"Find PR / issue mentioning Y"              | ox code search "Y" type:pr | type:issue
"TODO / FIXME comments in this repo"        | ox code search "" type:comment ckind:todo
"What changed about Y last month?"          | ox code search "Y" type:commit after:<date>
"Diff search: when did string Z appear?"    | ox code search "Z" type:diff
"Open PRs blocked / stalled"                | ox code prs --sort stalled
"What's hot in this repo right now?"        | ox code insights
"Exact string in one specific file"         | Grep / Read (faster, no index hop)
"File listing by glob"                      | Glob (no index value here)
"File doesn't exist in the index yet"       | Read directly
```

## DSL cheatsheet

`type:{code,symbol,diff,commit,comment,pr,issue}` ·
`repo:<n>[@<rev>]` · `file:<glob>` · `lang:<id>` ·
`author:<name>` · `before:<date>` · `after:<date>` ·
`message:<text>` · `calls:<name>` · `calledby:<name>` ·
`returns:<type>` · `depth:1..10` ·
`confidence:{extracted,inferred,ambiguous}` · `ckind:<kind>` ·
`state:<pr_state>` · `OR` · `/regex/` · negate with `-` prefix.

Run `ox code search --help` for the full grammar.

## Anti-patterns

- Calling `Grep` on `internal/` before checking `ox code search` —
  symbol lookup is what the index is for.
- Calling `Read` on 4+ files to trace a call chain —
  `calls:` / `calledby:` does this in one query.
- Ignoring `ox code insights` when planning multi-file changes —
  it surfaces contention before you collide with another worktree.

## Fallback policy

If `ox code` returns 0 results, the index may be stale or the symbol may be in
a path the indexer doesn't cover. Re-run with broader filters (`type:code`,
drop `lang:`, drop `file:`). If still empty, fall back to Grep on a hunch and
report it — empty results from `ox code` are diagnostic, not authoritative.
```

### Patch B — `cmd/ox/agent_prime_xml.go:127-136`

Replace the `<code-search>` banner with the demonstrate-don't-prescribe version
in R1 above.

### Patch C — `cmd/ox/code.go:86-90` (codeCmd Long)

Expand to the multi-example version in R2 above.

### Patch D — `cmd/ox/code.go:100-103` (codeSearchCmd Long)

Add full DSL grammar + 5–8 example queries.

### Patch E — `claude-plugin/skills/ox/SKILL.md`

Add a "Searching code, history, PRs" section listing `ox code search`,
`ox code insights`, `ox code prs`, `ox code activity` with example invocations.

### Patch F — `extensions/claude/commands/ox.md`

Append a `## Search Code` section mirroring Patch E.

### Patch G — `internal/prime/guidance.go:73-87`

The `<commands>` table currently has 2 rows for `ox code` (`search` + `insights`).
Add `prs` and `activity`. Add a row per ADR-019 verb wrapper if R3 lands.

---

## Optional API Changes

These are larger and shouldn't block the instruction edits above.

| Change | Why | Effort |
|---|---|---|
| `ox code callers/callees/defs/refs` verb wrappers (R3) | Verb-mode discoverability for ADR-019 | medium |
| Structured indexing-status JSON (R7) | Agents handle JSON status; abandon on stderr errors | small |
| Stderr stats one-liner (R8) | Calibrate agent on latency | trivial |
| `--snippet N` flag + bump default 120→200 (R6) | Stop training "ox returns less than grep" | trivial |
| `--check-index` (exit code only) | Lets agents probe readiness in 1 line | trivial |
| `number:<n>` filter in DSL | Enables `ox code pr <number>` verb | small |
| Auto-DSL-hint when query is bare (R9) | JIT discovery | small |
| Demote `code activity` from "fact extractor" wording | Currently reads as internal-only | trivial |

---

## Optional Instrumentation

This investigation worked from code-reading and the verbatim prime-banner the
agent receives. To *quantify* the discoverability gap (rather than reason about
it from first principles), one cheap addition would help:

**Per-session tool-use log**: a hook that records, per ox-tracked session, the
ratio of `ox code search` invocations to `Grep` / `Glob` / `Read`-of-large-files
invocations. The instance store already tracks `CumulativeContextTokensBySource`
(`cmd/ox/agent_prime.go:599`). Adding a `tools_used` counter keyed by tool name
would expose the actual usage curve and let us A/B the banner rewrite (R1)
against the status quo.

Out of scope for this worktree — propose as a follow-up.

---

## What's NOT recommended

- **Don't escalate the prime banner to ALL CAPS / harsher MUST language.** That
  fights symptom not cause; agents that drift do so because they don't know
  what `ox code` uniquely does, not because the instruction wasn't loud enough.
- **Don't make `ox code search` the default for *all* "find this string"
  intents.** Grep is faster for exact-string-in-known-file. The banner should
  reserve grep for that, not eliminate it.
- **Don't auto-disable Grep/Glob from the agent's toolset.** That breaks the
  fallback path and trains the agent to distrust the harness.
- **Don't write a long human-facing doc as the primary fix.** The agent does
  not read `docs/human/`. Fixes must land in: the prime banner, `--help` text,
  skill files, and `.claude/rules/`. (This document is for the maintainer; the
  agent-visible fixes are the patches it proposes.)

---

## Summary table

| Recommendation | Surface | Effort | Impact |
|---|---|---|---|
| R1 — Demonstrate-don't-prescribe prime banner | `cmd/ox/agent_prime_xml.go:127` | low | high |
| R2 — DSL + examples in `--help` | `cmd/ox/code.go` Long fields | low | high |
| R3 — Verb wrappers (`callers`, `callees`, `defs`) | new `code_verbs.go` | medium | high |
| R4 — `.claude/rules/ox-code.md` | new rule file | low | medium |
| R5 — `ox code` in shipped skill | `claude-plugin/skills/ox/SKILL.md` | low | medium |
| R6 — Snippet truncation 120→200 | `cmd/ox/code.go:205` | low | medium |
| R7 — Structured indexing-status JSON | 4 call sites | low | medium |
| R8 — Stderr stats one-liner | `cmd/ox/code.go` search/insights | trivial | low |
| R9 — JIT DSL hint when query is bare | `cmd/ox/code.go:226` | low | low |
| R10 — Reword `activity`/`prs` Short | 2 call sites | trivial | low |
| R11 — `ox find` / `ox where` alias | rootCmd | low | low (long-tail) |

R1 + R2 + R4 + R5 are the recommended initial batch — they are all instruction
edits, low risk, and they hit the dominant failure mode (framing + missing DSL
visibility) before any CLI-surface change.
