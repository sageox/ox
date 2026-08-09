---
paths:
  - cmd/ox/session*.go
  - cmd/ox/agent_session*.go
  - cmd/ox/doctor_session*.go
  - internal/lfs/**
  - internal/session/**
  - internal/daemon/**
---

# Session Draft Placeholders (ADR-029)

A **draft** is a `meta.json`-only directory committed to
`<ledger>/sessions/<name>/` partway through a live recording, so
`https://<endpoint>/c/<session_id>` resolves for links already circulating in
PR bodies and commit trailers. It carries **no turn data** and is superseded
wholesale at session stop.

Full rationale: [docs/adr/ADR-029-session-draft-placeholder.md](../../docs/adr/ADR-029-session-draft-placeholder.md).

---

## The one rule

> **Any code that treats "`sessions/<name>/meta.json` exists" as "this session
> is real" MUST call `meta.IsDraft()` first.**

Before drafts, that directory existing meant the session was finalized and
uploaded. It no longer does. Every consumer that skipped a session, marked it
done, indexed it, repaired it, or refused to delete it on that basis is now
wrong for live recordings unless it branches on `IsDraft()`.

Read `.Draft` directly only inside `IsDraft()`. The helper is nil-safe, and
nil is the normal result of an unreadable `meta.json` on every one of these
paths.

## A draft contains meta.json and NOTHING else

Forced by the LFS invariant in [cache-only-design.md](cache-only-design.md):
real bytes on the git-tracked `raw.jsonl` break LFS linkage and make the
ledger reject **every future push, for every teammate**.

Three writer-side guards enforce it, and none is redundant:

| Guard | Refuses |
|---|---|
| `lfs.validateDraftShape` (via `Validate` → every writer) | a draft meta with a populated `files` manifest or any summary text |
| `lfs.WriteDraftSessionMeta` | a directory that already holds `raw.jsonl` |
| `sessionArtifactsToStage` | returns nothing for a draft, so its legitimately-empty manifest cannot fall through to the `*.jsonl`/`*.md` glob |

`lfs.DraftInput` deliberately has **no field that can hold transcript-derived
text** — no title, no summary, no preview. That is the privacy guarantee, and
it is enforced by a test that fails when a field is added.

## Ledger index mutation is a guarded chokepoint

Drafts are the first ledger index writer that deliberately does **not** push,
so they miss the `IsSafeForGitOps` check that rides along with
`PushWithRetry` for every other writer. Mid-rebase, an unguarded `git add`
marks the conflict resolved and the following commit consumes the replay step
— silently destroying whatever was being replayed.

**Every draft git write goes through `prepareDraftLedgerWrite`**, which
validates the session name, validates the ledger target, waits out a
blue-green GC swap, and refuses mid-rebase. Adding a fifth write path means
calling it, not copying three of its four checks.

## Banned patterns

```go
// BANNED — a draft makes this true for a LIVE recording
if _, err := os.Stat(filepath.Join(ledgerPath, "sessions", name, "meta.json")); err == nil {
    continue // "already uploaded"
}

// BANNED — reads .Draft directly; panics on the nil meta that an
// unreadable meta.json produces
if meta.Draft { ... }

// BANNED — fails OPEN. An unreadable meta on a daemon path falls through to
// recoverRawFromSessionFile, which writes real bytes to the tracked raw.jsonl
if _, isDraft, err := lfs.PreservedSessionIDAndDraft(dir); err == nil && isDraft { continue }

// BANNED — a new git write path that skips the chokepoint
exec.Command("git", "-C", ledgerPath, "add", "--sparse", "--", relPath)

// BANNED — purging before reading the ses_ id; the draft meta CARRIES it
purgeDraftSessionDir(ledgerPath, name)
id, _ := lfs.PreservedSessionID(sessionDir) // always "" now
```

## Correct patterns

```go
// Classify, nil-safe, with the fail-safe direction chosen deliberately
meta, err := lfs.ReadSessionMeta(sessionDir)
if err == nil && meta.IsDraft() { continue }

// Daemon paths fail CLOSED — skipping is cheap, recovering onto a tracked
// raw.jsonl breaks LFS for the team
if _, isDraft, err := lfs.PreservedSessionIDAndDraft(dir); isDraft || err != nil { continue }

// Finalize: read the id, THEN purge — one helper so the order can't drift
preservedID, wasDraft, err := supersedeDraftForFinalize(ledgerPath, sessionName)

// Any preserve-unowned-fields RMW must clear the markers explicitly
next := current
next.ClearDraft()
```

## Fail-safe directions

Each site picks a direction for an unreadable `meta.json`. **Pick the one that
is non-destructive when wrong**, and say why in a comment:

- **Skip / refuse** on daemon detection, abort resolution, and draft deletion —
  being wrong means recovering bytes onto a tracked path, or deleting a
  directory we could not classify.
- **Treat as uploaded** in doctor's orphan sweep — retrying an unclassifiable
  upload risks clobbering a finalized session; skipping only defers to a human.
- **Fail open** in `ledgersearch` — a malformed file must not hide a real
  session from search.
- **Fatal** in `PreservedSessionIDAndDraft` — we cannot tell whether it held a
  `ses_` id we would rotate, and rotating 404s links already in PR bodies.

## When adding a new consumer

1. Does it enumerate `<ledger>/sessions/`? Add the `IsDraft()` branch.
2. Does it write to the ledger index? Route through `prepareDraftLedgerWrite`.
3. Does it read a `ses_` id from a session dir it will then modify? Read first.
4. Add a test with a **negative control** — a non-draft fixture that still
   produces the behavior. Without one, a draft-skip test passes trivially,
   because a meta-only directory already does nothing on most paths.
