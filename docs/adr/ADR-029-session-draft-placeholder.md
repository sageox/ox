# ADR-029: Session draft placeholders

**Status:** Accepted
**Date:** 2026-08-08
**Supersedes:** nothing
**Related:** ADR-016 (session summarization delegation), ADR-020 (session pause/resume),
`.claude/rules/cache-only-design.md`, `docs/specs/session-pr-issue-linkage.md`

---

## Context

A session recording is invisible to the SageOx server until `ox agent session stop` runs the
full upload pipeline. But agents are instructed to put `SageOx-Session:
https://<endpoint>/c/<ses_id>` in PR bodies *during* the session, and the same URL goes into
commit trailers via `prepare-commit-msg`.

Until the session ends, that link resolves to nothing. There is no way to tell a correctly
wired setup from a broken one, which makes every new integration look broken during the
window when a user is most likely to be checking.

A second, related gap: `ox agent session abort` discards local data, but there was no notion
of anything the CLI had already published, so "aborted" had no server-side counterpart beyond
a best-effort notification.

### What already existed

`POST /api/v1/sessions/<id>/started` (`cmd/ox/session_notify.go`) fires at `StartRecording`.
It is fire-and-forget, bounded at 750 ms, gated on the `attribution.session` toggle, and
**every failure is silent**. There is a matching `.../aborted`.

That channel is strictly *faster* than a draft (t=0 vs. turn 2 plus a sync cycle). What it is
not is *durable* or *observable*: a dropped notification is indistinguishable from a server
lag, and nothing retries it.

## Decision

Publish a **`meta.json`-only draft placeholder** into the git-tracked
`<ledger>/sessions/<name>/` at response-turn 2, marked `draft: true`, carrying zero turn data.
Supersede it wholesale at session stop. Retract it on abort.

This adds a durable, retryable, observable channel alongside the ephemeral notification, and
is the degenerate first case of real-time session recording — the direction this is headed
anyway.

### The shape is forced, not chosen

`.claude/rules/cache-only-design.md`: the git-tracked `<ledger>/sessions/<name>/raw.jsonl`
**must** remain an LFS pointer. Real bytes there cause two documented catastrophes (2026-04-25
Phase 2 incident, PRs #559–#564):

- LFS linkage breaks and the ledger rejects **every future push, for every teammate**, with
  `LFS objects are missing`.
- The daemon's pointer-stub skip stops applying, so anti-entropy re-finalizes live sessions
  and clobbers good summaries.

So a draft contains `meta.json` and nothing else. Three properties follow for free:

| Property | Why it follows |
|---|---|
| **Privacy is structural** | `lfs.DraftInput` has no field that can hold transcript-derived text. Not a title (which is derived from the user's first message), not a summary, not a preview. A draft *cannot* leak turn content, by type. |
| **Daemon anti-entropy already ignores it** | `detectInDir` requires a `raw.jsonl`; a draft has none. The explicit `IsDraft()` skip we added makes that intentional rather than incidental. |
| **No LFS objects** | Nothing to upload, orphan, or reconcile. |

### The provisionality invariant

> **While `meta.draft == true`, every file in that session directory is provisional, and
> finalize purges the directory wholesale before writing anything.**

This is what handles the case we do not control: the SageOx server may summarize a zero-turn
draft, write `summary.json` / `summary.md`, and push them; a finalize-time `git pull --rebase`
folds those into our working tree. A whitelist of "files the server might write" rots the
first time the server writes something new. Deleting the directory cannot.

The `ses_` id is read **before** the purge — the draft's `meta.json` is a durable carrier of
it, and for a recording whose `raw.jsonl` header predates the `SessionID` field it is the
*only* carrier. `supersedeDraftForFinalize` exists so that ordering is a function signature
rather than a convention repeated at four call sites.

### Ledger index mutation is a guarded chokepoint

This is the design lesson that cost the most to learn, and it generalizes beyond drafts.

Every other ledger index writer in the CLI is followed by `pushLedger` →
`gitutil.PushWithRetry` → `IsSafeForGitOps`, so the rebase guard rides along for free. **Draft
writes deliberately do not push** — that is the entire point — so they fell straight through
it.

Without the guard, this happens: the daemon's `git pull --rebase` conflicts on
`sessions/<name>/meta.json` (the documented steady state — one ledger sat 341 ahead / 1055
behind for 13 days with 281 such conflicts). The rebase stops mid-replay. A Stop hook fires,
atomically overwrites the conflicted file, `git add` **marks the conflict resolved**, and
`git commit -- <pathspec>` **succeeds on the detached rebase HEAD, consuming the replay step**.
The next `rebase --continue` reports success and the commit being replayed is silently gone —
a teammate's finalize, a murmur, a plan, imported team data.

So: `prepareDraftLedgerWrite` is the single gate every draft index mutation passes. It
validates the session name, validates the ledger target, waits out a blue-green GC swap, and
refuses when the clone is mid-rebase. **The guard belongs at index-mutation time, not at push
time.**

Two more things that gate discovered:

- `git commit -- <pathspec>` commits the **working tree**, not the index. A concurrent
  finalize rewriting the same `meta.json` would get committed under a `session-draft:`
  subject. `assertDraftStillStaged` re-verifies both worktree-vs-index equality and
  draft-ness immediately before committing.
- `deriveLedgerPath` returns `filepath.Dir` for *any* path whose parent is named `sessions`,
  which includes the XDG cache and the legacy in-repo fallback — i.e. **the user's own project
  root**. Its three prior callers only built IPC payloads, so a wrong answer was harmless.
  This is the first caller that runs `git commit`. `validateDraftLedgerPath` requires the
  target to be this project's resolved ledger.

## Design decisions

| Decision | Choice | Rationale |
|---|---|---|
| Draft contents | `meta.json` only | Forced by the LFS invariant; buys privacy and daemon-safety free |
| Turn signal | The `Stop` hook | It fires exactly once per completed response turn. `handleAfterTool` also fires on `PostToolUse`, so counting there counts tool calls |
| Publish turn | 2 | Turn 1 is often a one-shot question that never earns a PR link; publishing there puts a ledger commit in the path of every trivial interaction |
| Refresh | Counters only, every 10 turns | Load-bearing twice over: a climbing `turn_count` is the "it's working" signal the feature exists to provide, and `updated_at` is the *only* way the orphan reaper can tell a live draft from a dead one |
| Who pushes | The daemon's existing sync cycle | `pushLedger` is a secret scan + credential refresh + LFS reconcile + 3-attempt rebase loop. Paying that on a turn boundary is what this design avoids. Precedent: `pushMurmurCommits` |
| Daemon push safety | Only when **every** unpushed commit is a draft commit | `git push` moves the whole branch, and this path has neither the secret gate nor `ReconcileLFS`. Narrower than the murmur equivalent, deliberately |
| Daemon unreachable | Local commit, no push | The next `pushLedger` from any path carries it. Never spawn a detached child |
| Draft marker | One persisted bool + `IsDraft()` | `omitempty` ⇒ absent key means not-a-draft ⇒ every existing `meta.json` stays byte-identical |
| Status vocabulary | One **derived** `StatusDraft` | Without it a draft reads as `StatusUploaded`: live recordings render as finished, and `session abort <name>` refuses. Does not touch `StopReason`, which is a precedence lattice for *terminal* states |
| Config | `session.draft` = `on` \| `off` | User config, not an env var. Numeric tuning stays constants — a public numeric key is permanent API for a knob nobody asked for |

## Fail-safe directions

Every site that reads a possibly-unreadable `meta.json` picks a direction. The rule is
**whichever direction is non-destructive when wrong**:

| Site | Unreadable meta ⇒ | Why that direction |
|---|---|---|
| `PreservedSessionIDAndDraft` | fatal | Cannot tell whether it held a `ses_` id we would rotate |
| `isSessionInLedger` | finalized (refuse abort) | Points the user at `session delete` rather than deleting what we cannot classify |
| `resolveSessionForAbort` | refuse the ledger dir | Returning it means `os.RemoveAll` on a tracked dir with no `git rm`: the next pull restores it, the real recording is untouched, and the user is told it was discarded |
| `deleteDraftFromLedger` | refuse | Same |
| `findOrphanedSessions` | uploaded (skip) | Retrying an unclassifiable upload risks clobbering a finalized session; skipping defers to a human |
| `findOrphanedDrafts` | not an orphan | A false positive deletes a live session's placeholder |
| daemon `detectInDir` / `DetectOrphanedForAgent` | **skip** | Falling through reaches `recoverRawFromSessionFile`, which writes real bytes to the tracked `raw.jsonl` and breaks LFS for the team |
| `ledgersearch.isDraftSession` | not a draft (index it) | Fail-open; a malformed file must not hide a real session from search |

## Consequences

**Good.** `/c/<id>` resolves mid-session, durably and retryably. `ox session status` gains
`draft_state` (`disabled` / `pending` / `published` / `stale` / `failed`) and
`conversation_url`, so a silently-failed publish on *either* channel is now diagnosable —
which is a large part of the actual fix for "it seems broken." The draft is an extra durable
carrier of the `ses_` id. Abort now has something concrete to retract.

**Cost.** Every consumer that treats `sessions/<name>/meta.json` existing as "this session is
real" must now call `IsDraft()` first — roughly fifteen call sites, and every future one.
`.claude/rules/session-draft.md` exists to make that discoverable. Ledger commit volume rises
by roughly 1 + turns/10 per session.

**Known gaps.** `commitAndPushLedger` still uses a bare `git commit` (whole index); drafts make
its co-staging window routine rather than theoretical, and it should get the same pathspec
treatment in a follow-up. `pushMurmurCommits` has the secret-gate hole this change declined to
propagate. `.recording.json` is now gitignored, but that does not untrack markers already
committed in existing ledgers.

## Alternatives considered

**Instrument the existing notification instead.** Genuinely tempting, and cheaper: persist a
`notified_at` on `RecordingState`, re-POST from the same Stop hook when unacked, include
`turn_count` in a heartbeat. Zero git, zero new consumers to teach. Rejected because the
notification is not durable — nothing survives a process exit mid-flight — and because the
git channel is the stepping stone to real-time session recording, which is where this is
going regardless. Worth revisiting if the consumer-awareness tax proves higher than expected.

**Blank versions of every session file.** The original shape proposed. Rejected: a blank
`raw.jsonl` in the tracked directory breaks LFS linkage team-wide and defeats the daemon's
pointer-stub skip.

**A new IPC message so the daemon does the write.** Rejected as ~8 files of daemon plumbing
(message type, payload, client method, service interface, callback wiring, handler, plus a new
traversal-validation trust boundary) for a best-effort placeholder. Reusing the existing sync
cycle for the *push* gets the same latency win with one function.

**Selective purge at finalize.** Rejected: a whitelist of server-authored filenames rots the
first time the server writes a new one.
