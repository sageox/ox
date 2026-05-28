# Session ↔ Commit Linkage

**Audience:** AI coworkers and engineers working on ox session, commit-trailer, or git-hook code.
**Status:** Phase A + Phase B of ox-bxo2 (Session↔Commit Linkage Hardening) shipped.
**Companion docs:** [`session-pr-issue-linkage.md`](./session-pr-issue-linkage.md) (PR/issue mapping, follow-up epic).

---

## TL;DR

- **Forward link (commit → session):** the `SageOx-Session: <url>` trailer in each commit message. Injected by the `prepare-commit-msg` git hook while a recording is active.
- **Reverse index (session → commits):** `SessionMeta.ProducedCommits []string`. Populated by `post-commit` during the active recording; kept honest across in-recording rewrites by `post-rewrite`. Folded into `meta.json` at session stop / recover.
- **Source of truth on disagreement:** the trailer wins. `ProducedCommits` is a fast structured index over what is already discoverable by grepping commit messages.
- **Closed sessions are intentionally NOT mutated by post-rewrite.** Staleness is a soft signal in `ox doctor`, not auto-healed.

---

## Why this exists

The naive premise that "ox sessions store commit SHAs → a rebase silently breaks them" was almost the opposite of how ox actually worked before this work:

1. Sessions did not store commit SHAs at all (no `CommitSHA`, no `HeadCommit`, no list of produced commits).
2. The only commit↔session linkage was a `SageOx-Session: <url>` trailer in the commit message, injected by `prepare-commit-msg`.
3. Because that trailer lives inside the commit message, git preserves it across most rewrites for free (amend, vanilla rebase, cherry-pick).
4. Reverse direction — "what commits did this session produce?" — was unanswerable.

Phase A (this spec + the trailer-survival test suite) locks down what the existing trailer guarantees. Phase B adds a structured reverse index so `ox session view` and `ox query` can answer the reverse-direction question.

---

## Rewrite-survival matrix

| Operation | Trailer survives? | `ProducedCommits` updated? | Notes |
|---|---|---|---|
| `git commit --amend` (no message edit) | ✅ Yes | ✅ Yes — `post-rewrite` rewrites SHA in place | Most common rewrite; trivially preserved |
| `git rebase` (non-interactive) | ✅ Yes | ✅ Yes — each replayed SHA rewritten via mapping | Preserved on per-commit basis |
| `git rebase -i` reword | ⚠️ Depends on editor | ⚠️ Depends | User editor may strip trailer manually |
| `git rebase -i` squash / fixup | ⚠️ Source trailers concatenated into combined message | ✅ `post-rewrite` collapses adjacent duplicates → single entry | Multiple `SageOx-Session:` lines in one commit body is valid but unusual |
| `git cherry-pick` | ✅ Yes | ❌ (only the source branch's recording knew; not auto-attached to other branches) | Trailer copies along; reverse index is per-recording |
| `git reset --hard` + recommit (no active recording) | ❌ No | ❌ No | Documented loss — see "Loss boundaries" |
| `git merge --squash` + `git commit -m` | ❌ No (combined message doesn't run prepare-commit-msg if `-m` provided) | ❌ No | Local analog of GitHub squash-merge |
| GitHub squash merge (server-side) | ⚠️ Depends on repo's squash-message setting | ❌ No (no local hook fires) | The biggest real-world loss case; see follow-up issue C2 |
| `git filter-repo` / `filter-branch` | ❌ Likely no | ❌ No | History-rewrite tools rebuild commits from scratch |
| Commits made AFTER `ox session stop` | ❌ No trailer injected | N/A | Documented limitation |

The test suite in `cmd/ox/hooks_commit_msg_rewrite_test.go` covers every row above that is testable inside a local git repo. GitHub-side squash-merge is covered by the follow-up mapping epic, not by this spec.

---

## Source of truth on disagreement

When `ProducedCommits` and the commit-message trailer disagree, the trailer wins. Reasoning:

- The trailer is durable: it lives inside the commit object, propagates with the commit through clones, rebases, and forks.
- `ProducedCommits` lives inside a per-recording `meta.json`. If a rebase happens after session stop, the reverse index goes stale (closed sessions are not auto-mutated — see D3 below).
- Therefore the reverse index is a cache of "what we knew at recording time," not an authoritative record.

Anything that consumes both (e.g. a future reconciler) should treat the trailer as ground truth and use `ProducedCommits` only as a fast index. If a SHA appears in `ProducedCommits` but not in `git log` for the current branch, it is unreachable — surface as soft signal, do not delete.

---

## Design decisions

### D1 — In-recording tracking via `post-commit`

The `prepare-commit-msg` hook fires BEFORE git assigns a SHA, so it cannot populate `ProducedCommits` itself. `post-commit` fires after the SHA is assigned, which is the right injection point.

Implementation: `cmd/ox/hooks_post_commit.go:runHooksPostCommit`. Resolves the active recording via the same lookup the trailer injector uses (agent-specific via `SAGEOX_AGENT_ID`, workspace fallback for human commits, subagent prefers parent). Appends HEAD SHA via `git rev-parse HEAD`. Idempotent: a second invocation on the same HEAD does not duplicate.

### D2 — In-recording rewrite tracking via `post-rewrite`

Git invokes `post-rewrite` after `amend` and `rebase` with a stdin stream of `<old-sha> <new-sha>[ <extra>]` lines. The handler reads the mapping, rewrites matching `ProducedCommits` entries in place, and collapses adjacent duplicates (the squash case).

Implementation: `cmd/ox/hooks_post_rewrite.go:runHooksPostRewrite`. Squash/fixup leaves N entries all pointing at the same new SHA; we collapse those to one entry so `ox session view` reflects the resulting history, not the pre-rewrite history.

### D3 — Closed sessions are NOT mutated by `post-rewrite`

If a user closes a session, then rebases the produced commit range later, `ProducedCommits` in the now-archived `meta.json` becomes stale.

**v1 policy: accept staleness, surface as a soft signal in `ox doctor`.** Reasoning:

- Mutating ledger history per-rewrite undermines the "ledger is an archive" mental model.
- Requires scanning every closed session's `meta.json` on every rewrite — non-trivial on large ledgers.
- The forward trailer-based link still survives the rebase for free; users can still navigate from new commits to sessions.

A future opt-in policy for mutable closed-session `ProducedCommits` is tracked separately (follow-up issue C1 under `bd ox-bxo2`).

### D4 — Trailer remains source of truth for commit → session

See "Source of truth on disagreement" above. `ProducedCommits` is a derived, complementary index. Do not consume it as authoritative.

### D5 — Schema migration

`SessionMeta.ProducedCommits` and `RecordingState.ProducedCommits` are added as `omitempty` fields. Old sessions without the field round-trip unchanged; reading an absent field yields nil, treated equivalently to an empty list. No on-disk migration. An opt-in backfill (scan `git log` for `SageOx-Session:` trailers, populate `ProducedCommits` retroactively) is tracked as follow-up C3.

---

## Loss boundaries

The trailer-survival matrix above identifies the documented loss cases. None of them are bugs — they are intrinsic to the trailer-based design. Mitigations:

| Loss case | Mitigation |
|---|---|
| GitHub squash-merge stripping trailers | Move to server-side reconciler (mapping epic, follow-up). Sticky PR comment maintained by SageOx GitHub App via webhook. |
| `reset --hard` + recommit | None feasible at the linkage layer; user explicitly threw away history. |
| Commits after session stop | Documented as expected. If users want all-day coverage, they should run `ox session start` continuously. |
| `filter-repo` / `filter-branch` | None; these are intentional history-rewrite tools and trailer loss is the smallest of their effects. |

The `ox doctor` checks (`checkSessionTrailerRatio`, `checkSessionProducedCommitsStaleness`) surface these losses to the user as soft signals so the loss is visible, even if not auto-healed.

---

## Doctor signals

Two soft-signal checks live in `cmd/ox/doctor_session_linkage.go`:

1. **Trailer coverage on recent commits.** Scans the last 50 commits on the current branch, counts `SageOx-Session:` trailer presence, emits a warning if the ratio is below 40%. Catches GitHub squash-merge configs that strip trailers, `--no-verify` habits, and uninstalled hooks.
2. **Closed-session ProducedCommits reachability.** For each closed session under `<ledger>/sessions/`, checks whether each SHA in `ProducedCommits` is reachable via `git cat-file -e`. Reports unreachable count. Documents the D3-deferred staleness.

Neither check fails. Both are advisory.

---

## Code map

| Concern | File |
|---|---|
| `SessionMeta.ProducedCommits` schema | `internal/lfs/meta.go` |
| `RecordingState.ProducedCommits` schema | `internal/session/recording.go` |
| Hook installation (3 hooks, idempotent) | `cmd/ox/hooks_git.go` |
| Trailer injection (`prepare-commit-msg`) | `cmd/ox/hooks_commit_msg.go` |
| Reverse index append (`post-commit`) | `cmd/ox/hooks_post_commit.go` |
| Reverse index rewrite (`post-rewrite`) | `cmd/ox/hooks_post_rewrite.go` |
| Fold into `SessionMeta` on stop | `cmd/ox/agent_session.go:1285` |
| Render in `ox session view --text` | `cmd/ox/session_view_text.go:renderProducedCommits` |
| Render in `ox session view --json` | `cmd/ox/session_show.go:convertStoredSession` |
| Doctor soft signals | `cmd/ox/doctor_session_linkage.go` |
| Trailer-survival tests | `cmd/ox/hooks_commit_msg_rewrite_test.go` |
| Reverse-index handler tests | `cmd/ox/hooks_post_commit_test.go`, `cmd/ox/hooks_post_rewrite_test.go` |
