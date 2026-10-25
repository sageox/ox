# Rule: When to use `ox code` instead of grep

This repo has a CodeDB index served by the ox daemon. `ox code` queries it.
Grep/ripgrep/Glob still exist for cases the index cannot serve, but the index
is the **default** for the cases below.

## Decision tree

Verb-mode wraps the DSL — reach for verbs first; drop into `ox code search`
only when the intent doesn't match a verb.

```
Agent intent                                | First tool to try
--------------------------------------------+-------------------------------------------
"Where is function X defined?"              | ox code defs X
"Who calls X?"                              | ox code callers X
"What does X call?"                         | ox code callees X --depth 2
"Where is X referenced in code text?"       | ox code refs X
"What changed in path Y?"                   | ox code log Y --author <n> --after <date>
"Find PR / issue mentioning Y"              | ox code search "Y" type:pr   (or type:issue)
"TODO / FIXME comments in this repo"        | ox code search "" type:comment ckind:todo
"Diff search: when did string Z appear?"    | ox code search "Z" type:diff
"Open PRs blocked / stalled"                | ox code prs --sort stalled
"What's hot in this repo right now?"        | ox code insights
"Recent GitHub activity (PRs/issues/commits)" | ox code activity --since 7d
"Exact string in one specific file"         | Grep / Read   (faster, no index hop)
"File listing by glob"                      | Glob          (no index value here)
"File doesn't exist in the index yet"       | Read directly
```

## DSL cheatsheet

```
type:{code,symbol,diff,commit,comment,pr,issue}
repo:<n>[@<rev>]         -repo:<n>
file:<glob>              -file:<glob>
lang:<id> / language:    -lang:<id>
author:<name>            -author:<name>
before:<date>            after:<date>
message:<text>           -message:<text>
calls:<name>             calledby:<name>      # ADR-019 resolved call graph
returns:<type>           depth:1..10
confidence:{extracted,inferred,ambiguous}    # ADR-019
ckind:<kind>             state:<pr_state>
select:{repo,file,symbol,symbol.<kind>}
count:<N>                case:yes
patterntype:{literal,keyword,regexp}
OR                       # boolean across groups
/regex/                  # forced regex
-<filter>                # negate any filter
```

Run `ox code search --help` for the authoritative grammar.

## Anti-patterns

- Calling `Grep` on `internal/` before checking `ox code search` —
  symbol lookup is what the index is for.
- Calling `Read` on 4+ files to trace a call chain —
  `calls:` / `calledby:` does this in one query.
- Ignoring `ox code insights` when planning multi-file changes —
  it surfaces contention before you collide with another worktree.
- Treating "0 results from `ox code`" as the final answer when the index
  may be stale — broaden filters first.

## Fallback policy

If `ox code` returns 0 results, the index may be stale or the symbol may be
in a path the indexer doesn't cover. Re-run with broader filters
(drop `lang:`, drop `file:`, switch `type:symbol` → `type:code`). If still
empty, fall back to Grep on a hunch and report it — empty results from
`ox code` are diagnostic, not authoritative.

## Index health

If `ox code search` errors with "code index is currently being built",
the daemon is rebuilding. Use Grep until rebuild completes, then return to
`ox code`. Check progress with `ox code status`.

If the agent has just edited files, the daemon attaches dirty overlays
automatically (see `db.AttachAllDirtyIndexes()` in `cmd/ox/code.go`) — your
own uncommitted edits are searchable within seconds.
