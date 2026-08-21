---
name: ox-session-review
description: >-
  Audit every session in the project ledger for quality, then offer cleanup and
  regeneration. Carries the operational knowledge from the 2026-04-25 cleanup
  (the cache-only invariant and a failure-mode watch-list) that no single ox
  subcommand backs. Use when the user asks to "review the ledger sessions",
  "audit session quality", "clean up bad session summaries", "regenerate session
  summaries", invokes the session-review workflow, or wants to find and fix sessions with
  missing/poor titles, empty summaries, or broken metadata.
---

<!-- Keep this file thin on behavioral guidance that belongs in `ox` CLI JSON
     output (guidance field). Native skills are portable wrappers; ox serves all
     AI coworkers. This skill is intentionally richer than most because
     the audit + regeneration flow is not backed by a single ox subcommand. -->

This skill carries the operational knowledge from the 2026-04-25 cleanup
(see PRs #559–#564 and bd ox-b917, ox-9o29, ox-4ncz, ox-1i3k). Read the
**Failure-mode watch-list** before acting — it is the difference between
a routine audit and re-causing the same incident.

## Failure-mode watch-list (READ FIRST)

When operating on the ledger, watch for and handle each of these
explicitly. Silent skipping is not allowed — surface them in the report.

| Mode | Symptom | Right action |
|---|---|---|
| **Cache-only invariant break** | After hydration, the in-place `<ledger>/sessions/<name>/raw.jsonl` is real content (not a ~140B LFS pointer). | Stop. Do not run any ox command that may `git add` the session dir. Investigate which code path wrote in-place — likely a regression of `.claude/rules/cache-only-design.md`. |
| **Phantom LFS OID** | `ox session download` returns HTTP 404 from the LFS Batch API for an OID referenced in `meta.json`. | Unrecoverable from client. `ox session remove <name> --force` and report the lost session in the final summary. |
| **0-byte in-place stub** | `meta.json` references a real OID, but in-place file is 0 bytes (and not a pointer). | Treat as needing download. Confirm `cmd/ox/session_hydrate.go` has the `info.Size() > 0` guard (PR #564). Without it, the resolver will silently return "" forever. |
| **Daemon anti-entropy clobber** | A freshly-pushed good summary gets overwritten with a failure-marker stub minutes later. | The daemon scheduled finalize before our regen landed. Look at `internal/daemon/agentwork/session_finalize.go` `workNoLongerNeeded` re-verify guard (PR #561). If running a long batch regen, prefer a quiet daemon window. |
| **Validation banner on the website** | Site shows "Summary failed content validation: title too short" but the ledger has good content. | Likely browser-side React Query cache (staleTime 5m, gcTime 30m). Verify origin/main HEAD has the right summary; advise hard refresh. Do not "fix" by re-pushing. |
| **LLM regenerate hangs / asks for permission** | Regenerate spawns Claude Code and times out without writing. | Pass `--permission-mode bypassPermissions` to the headless invocation (PR #560 / regenerate flow). |

## The cache-only invariant (assert this before and after every batch)

Per `.claude/rules/cache-only-design.md`:

- For any session synced from the ledger, `<ledger>/sessions/<name>/raw.jsonl`
  MUST stay an LFS pointer (size ~140B, `lfs.IsPointerFile == true`).
- Hydrated full content lives only at
  `<ledger>/.sageox/cache/sessions/<name>/raw.jsonl` (gitignored).
- All readers route through `cmd/ox/session_content.go:openSessionContent`.

**Assertion to run before declaring a batch complete:**

```bash
# From the ledger root. Should print 0.
find sessions -name 'raw.jsonl' -size +1k -not -path '*/.*' | wc -l
```

Any non-zero count means hydrated bytes leaked to the in-place path —
the next `commitAndPushLedger` will glob them into a regular blob and
break LFS linkage. Stop and investigate before pushing.

## Phase 1 — Scan & Score (read-only)

1. Run `ox session list --all`.
2. Resolve the ledger sessions directory:
   - Read `.sageox/config.json` for `repo_id` and `endpoint`.
   - Sessions live at
     `~/.local/share/sageox/<endpoint-slug>/ledgers/<repo_id>/sessions/`.
3. For each session directory:
   1. Read `meta.json` — capture `entry_count`, `title`, `summary`,
      `files`, `created_at`.
   2. Read `summary.json` if present — capture `title`, `summary`,
      `key_actions`, `outcome`, `diagrams`, `aha_moments`,
      `sageox_insights`.
   3. Do NOT hydrate `raw.jsonl` during scan — hydration only happens in
      Phase 4 for sessions the user explicitly approves for
      regeneration. (For 100+ session ledgers, eager hydration is
      prohibitively slow and risks the failure modes above.)
   4. Score into the first-matching bucket below.

## Quality Buckets (first match wins)

### Removal Candidates
- `entry_count` is `0` or missing AND `files` manifest is empty/missing.
- Session outcome is `"failed"` AND `entry_count < 5`.
- Only skill-wrapper activity with nothing between session start and stop.
- Phantom-OID sessions surfaced during a previous Phase 4 run (track
  these — they cannot be regenerated).

### Meta Repair
- `files` manifest empty/missing BUT `entry_count > 0` — meta needs
  repair after hydrating + re-scanning raw.jsonl for actual file writes.

### Missing/Poor Summary
- No `summary.json`.
- `summary.json` has empty `title`, `summary`, or `key_actions`.
- Summary matches the stats-only fallback pattern (e.g., "N user
  messages, N assistant responses"; any summary where the body is < 80
  chars and contains only digits + "messages"/"responses").
- Title fails the website's content-validation gate (e.g., < 4 words or
  matches "Summary failed content validation: title too short").
- `diagrams` or `aha_moments` empty on a session with `entry_count > 50`.

### Poor Title
- Empty, or matches generic patterns: `"Session recording"`, date-only,
  fewer than 4 words, or doesn't reflect content.

### OK
- Passes all checks.

## Phase 2 — Report

Present grouped tables (Removal, Meta Repair, Missing/Poor Summary, Poor
Title) plus a one-line `OK: N healthy` tally. Surface systemic patterns
(e.g., "91/153 sessions have empty titles — likely a distiller bug") so
the user can file upstream bugs rather than just masking symptoms.

Always include in the report:
- A count of sessions where in-place raw.jsonl is NOT a pointer (should
  be 0; non-zero is an invariant break — surface loudly).
- Daemon liveness check: `ox doctor` daemon section. If a long batch is
  planned, mention whether the daemon will be racing.

## Phase 3 — User Confirms Actions

ASK the user what to act on. Never auto-delete or auto-regenerate. Offer
per-bucket choices plus a "top-N by entry_count" option to target
highest-value sessions first when buckets are large.

## Phase 4 — Execute Approved Actions

### Removals
```bash
ox session remove <session_name> --force
```

### Meta Repair
1. Hydrate: `ox session download <session_name>` (writes to cache, not
   in-place — verify after).
2. Re-scan hydrated `raw.jsonl` for Write/Edit tool calls; collect
   unique file paths.
3. Regenerate summary (see below) — the summary pipeline will also
   repopulate `files`.

### Summary Regeneration — Full-Session Processing

**Step 4a — Hydrate.** `ox session download <session_name>`. After,
verify the in-place file is still a pointer: `ls -la
<ledger>/sessions/<name>/raw.jsonl` should show ~140B. Real bytes go to
`<ledger>/.sageox/cache/sessions/<name>/raw.jsonl`.

**Step 4b — Token-Optimize (pre-summarize stage).**

Before feeding raw.jsonl into the summarization LLM, run a token-
optimize pass. For now this is a **no-op pass-through** — copy
raw.jsonl unchanged — but the long-term intent is documented so future
implementers (or a future `ox session token-optimize` subcommand) know
the contract:

```text
# token_optimize(raw_jsonl) -> optimized_jsonl
#
# Goal: reduce token footprint 5-10x while preserving all signal needed
#       for title/summary/key_actions/aha_moments/diagrams/insights.
#
# Intended removals / compressions:
#   - Duplicate tool_result blocks that repeat file contents already shown.
#   - Large Read tool_result bodies (keep first/last ~40 lines + elision).
#   - Base64 image payloads (keep MIME + dimensions only).
#   - Binary blobs, minified JS/CSS, compiled output in tool_results.
#   - Repeated system-reminder boilerplate (keep first occurrence).
#   - Verbose stack traces (keep top frame + ellipsis).
#   - Bash outputs with progress bars / spinners / ANSI — strip to final state.
#   - Redundant tool_use inputs that mirror the prior assistant message verbatim.
#
# Must preserve verbatim:
#   - All user turns (intent signal).
#   - Assistant turns with reasoning / decisions / explanations.
#   - Every Write/Edit tool_use input (path + diff — drives `files` manifest).
#   - First + last 3 entries (chapter boundaries).
#   - Error messages and their immediate resolution turns.
#
# Long-term: pipe raw.jsonl through a cheap model (haiku-class) for this
# compression pass, THEN pipe into a reasoning model for the actual summary.
# For now: no-op copy.
```

Implement inline: create a private per-run scratch dir via `mktemp -d` and
copy `raw.jsonl` from cache → `$scratch/optimized-<session>.jsonl` unchanged.
Never use a fixed shared path like `/tmp/ox-optimized-<session>.jsonl` —
concurrent reviews of the same session would race on it (same rule as the
no-shared-path warning in Step 4e). Remove the scratch dir when the review
ends. Log: "token_optimize: no-op pass-through (N bytes in / N bytes out)".

**Step 4c — Chunked Full-Session Analysis.**

Split the optimized jsonl into ordered chunks sized to fit a single LLM
context window (target ~60k tokens; roughly 1500-2000 lines depending on
turn size). Per chunk produce:

```json
{
  "chunk_index": N,
  "local_actions": [...],
  "local_aha_moments": [...],
  "local_topics": [...],
  "local_files_touched": [...],
  "local_errors_or_decisions": [...]
}
```

**Step 4d — Synthesize Final Summary.**

Feed the ordered partials (plus first 50 and last 50 lines of raw.jsonl
for opening/closing context) into a final synthesis pass producing the
canonical summary JSON:

```json
{
  "title": "Short descriptive title reflecting actual work (5-10 words)",
  "summary": "One paragraph executive summary covering motivation, approach, and outcome",
  "key_actions": ["Concrete action 1", "Concrete action 2"],
  "outcome": "success|partial|failed",
  "topics_found": ["topic1", "topic2"],
  "chapter_titles": ["Phase 1: ...", "Phase 2: ..."],
  "aha_moments": [
    {
      "seq": 7,
      "role": "user|assistant",
      "type": "question|insight|decision|breakthrough|synthesis",
      "highlight": "Key text from this moment",
      "why": "Why this mattered for the session's trajectory"
    }
  ],
  "diagrams": [
    {
      "title": "Data flow / architecture / state machine title",
      "type": "flowchart|sequence|state|architecture",
      "mermaid": "graph TD\n  A[Start] --> B[End]",
      "why": "What this diagram clarifies about the session"
    }
  ],
  "sageox_insights": [
    {
      "kind": "decision|convention|gotcha|followup",
      "insight": "Concrete reusable insight other coworkers would benefit from",
      "evidence_seq": 42
    }
  ]
}
```

**`diagrams` and `sageox_insights` MUST NOT be hardcoded empty.**
Generate them when the session has material worth capturing:
- **Diagrams**: any architectural change, data flow, state machine,
  pipeline, or non-trivial refactor. Mermaid syntax compatible with
  GitHub. Skip only if genuinely trivial (e.g., typo fix).
- **sageox_insights**: decisions, conventions discovered, gotchas that
  tripped the session, and followups other coworkers should inherit.
  Skip only if no reusable knowledge.

If invoking a headless LLM (Claude Code in `-p` mode), it must never hang
on a tool-permission prompt no one will answer — but session content is
untrusted input, so do NOT grant blanket permissions on the host. Run it
with tools disabled or a read-only allowlist (e.g. `--allowedTools ""`, or
`--allowedTools "Read"` if it must re-read chunk files); the summarize
prompts need nothing else. Use `--permission-mode bypassPermissions` ONLY
when the process runs inside an isolated sandbox/container — never
unsandboxed.

**Step 4e — Push.**
1. Pipe the synthesized JSON to push-summary via stdin. Do NOT write to `/tmp/` or any shared path — multiple agents may run concurrently on the same machine and race on shared filenames; macOS tmpfs GC can also reap the file between write and read.
   ```bash
   echo "$summary_json" | ox session push-summary --file - --session-dir <full_ledger_path>
   ```
2. Verify `"success": true`.

**Step 4f — Post-batch invariant check.**
After a batch run, before any `git push`, confirm:
- `find <ledger>/sessions -name 'raw.jsonl' -size +1k -not -path '*/.*' | wc -l` is `0`.
- `git status` in the ledger shows only `summary.json` / `summary.md` /
  `meta.json` modifications, never `raw.jsonl`.

If either check fails, do not push. Diagnose which step wrote in-place.

## Edge Cases

- **Empty ledger**: report and exit.
- **All sessions OK**: report and exit.
- **Hydration failure (404)**: phantom OID. Surface for removal, do not
  retry endlessly.
- **Hydration failure (network)**: log, skip, continue; list at end.
- **Very large session (raw.jsonl > 20k lines)**: still process fully
  via chunking — never fall back to edge-only sampling. If resource-
  constrained, warn and offer to defer rather than produce a low-
  quality summary.
- **Systemic distiller bugs**: if scan reveals patterns ("all sessions
  from user X have stats-only summaries", ">50% empty titles"), surface
  as likely upstream bug and suggest `bd create` for a root-cause fix
  instead of only per-session regeneration.

## Reusable prompt for ledger reviews in other repos

When asked to review a different ledger, lead with:

> Audit every session in this project's ledger for quality.
> Read `.claude/rules/cache-only-design.md` and
> `.claude/rules/lfs-no-git-lfs-binary.md` first — both invariants
> apply during this audit. Run the session-review skill flow:
> scan read-only, report by bucket, confirm with me before any
> removals or regenerations, and run the post-batch invariant check
> before pushing.
