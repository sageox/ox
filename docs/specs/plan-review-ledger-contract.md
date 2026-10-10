# Plan review: Ledger concurrency and compatibility contract

**Status:** candidate, not frozen (#1287). It freezes in #1288 once the module
implements and tests the mechanisms in 3.5 and 3.6 *and* a consuming host has
proven the same I/O assumptions on its own storage. Follow-up owners: #1288
(shared module), #1289 (CLI adoption), #1291 (browser outbox), #1294
(certification).

**Goal:** supported concurrent reviewers never lose feedback and never repair
Git conflicts by hand. This spec records what the CLI does today, names the
mechanisms found that lose, hide, block or broaden review data, and fixes the
contract choices the shared module implements.

**Executable half:**

| Where | What it pins |
|---|---|
| `internal/plan/testdata/review-contract/<fixture>/` | Shared fixtures: a plan dir (`plan/`) and the merged review `ox plan feedback show --json` returns for it, projected to the fields every host must agree on, the reviewer's label and note included (`expect.json`) |
| `TestReviewContractFixtures` (`internal/plan/feedback_test.go`) | The v1 reader against every fixture |
| `TestPlanReviewDurability_RacingClonesKeepEveryRecord` (`cmd/ox/plan_review_durability_test.go`) | Rounds and resolutions from three clones survive the push rebase |

---

## 1. Inventory

### Where review state lives

| Path (under `data/plans/<dir>/` unless noted) | Shape | Written by |
|---|---|---|
| `feedback/round-<UTC ns>-<id or rand8>.json` | one immutable file per round | `SaveFeedback` |
| `feedback/resolutions/<UTC ns>-<rand8>.json` | one immutable file per resolution (unreleased main) | `AppendResolution` |
| `feedback/resolutions.json` | legacy shared array; main reads, never writes | v0.20.0 and earlier `AppendResolution` |
| `feedback/remaps.json` | shared array, read-modify-write | `appendRemaps`, from `Save` |
| `events.jsonl`, `meta.json` | shared lifecycle log and snapshot, read-modify-write | `AppendPlanEvent` (`/approve`, lifecycle verbs), `Save` |
| `plan.md`, `plan.html`, `annotations.json`, `companions/` | authoring files, plain `os.WriteFile` | `Save`, `CopyCompanions` |
| `<ledger>/.git/ox-plan-push-pending/data__plans__<dir>.json` | one retry marker per plan dir | `recordPlanPushOutcome` |

Plan dirs sit under `data/`, which the Ledger's push and pull resolve
automatically (`ledger.AutoResolvePrefixes`), keeping the replayed local side
(`gitutil.ResolveRebaseAcceptTheirs`). Unique file names never conflict; shared
files do.

### Writers

| Writer | Reached by | Write | Commit / retry |
|---|---|---|---|
| `SaveFeedback` with id | `POST /feedback`, `ox plan feedback apply` | atomic temp+rename; under a per-plan lock, skipped when a round with that id exists (id compared, payload not) | whole plan dir; marker on failure |
| `SaveFeedback` without id | `POST /reopen` | atomic; random suffix; no idempotency | whole plan dir; marker |
| `AppendResolution` | `POST /accept`, `ox plan feedback resolve` | atomic; random suffix; no idempotency | whole plan dir; marker |
| `appendRemaps` | `Save` carrying HTML | read-modify-write; resets an unreadable file | `sharePlanDir`: whole plan dir, **no marker** |
| `AppendPlanEvent` | `POST /approve`, `ox plan approve/work/…` | read-modify-write of `events.jsonl`, then `meta.json` | `/approve`: whole dir + marker; CLI verbs: `sharePlanDir`, no marker |
| `Save` | `ox plan save/render`, hooks | `plan.md`, `plan.html`, `annotations.json` via non-atomic `os.WriteFile`, no repo lock | `sharePlanDir`, no marker |

Every review commit runs `commitPlanLocalCtx`: `git add --sparse -- <plan dir>`
(deletions included; only a large `plan.html` awaiting upload is excluded),
then `CommitLedgerSnapshot(…, <plan dir>)`. The snapshot keeps unrelated staged
Ledger paths out of the commit, but everything uncommitted *inside* the plan
dir rides along.

### Readers

| Reader | A read it cannot complete |
|---|---|
| `LoadAllFeedback` | skips an unreadable round (log line); keeps the earliest of same-id rounds, **whatever their payloads** |
| `CorruptFeedbackRounds` | names unreadable rounds; only `ox plan feedback show` calls it |
| `LoadResolutions` | corrupt legacy array: hard error; corrupt per-entry file: skipped, log line only |
| `LoadRemaps` via `AssembleReview` | unreadable: merges without remaps, log line only |
| `AssembleReview` callers (page, render, await, list, prime) | a hard error renders no review overlay, counts 0 open, or waits until timeout; only the digest printers report it |
| `awaitSnapshot` | returns `approved` before looking at open feedback |

All readers read the worktree, so records not yet committed or pushed are
included.

### Retry effects

- A commit or push failure in `commitAndPushPlanDir` writes one marker per
  **plan dir**; `ox agent prime` starts `ox plan flush-pending`, which re-stages
  the plan dir *as it is at retry time*, commits and pushes.
- Any later success for that plan dir, from any operation, clears the marker.
  The clear runs after `planCommitMu` is released, so a success can clear a
  marker another operation wrote after it (narrow race).
- `sharePlanDir` (save, render, lifecycle verbs) writes no marker at all.
- Markers live in `.git/`; a re-clone loses them.

---

## 2. Hazards

| # | Mechanism | Effect | Shown by | Owner |
|---|---|---|---|---|
| H1 | Shared review files (`resolutions.json`, `remaps.json`, `events.jsonl`; `meta.json` by the same rule, not reproduced) conflict across clones; `data/` resolves keep-local | the first publisher's entries vanish from the remote tip, and from that clone after it pulls; an approval can disappear | repro | #1288 / #1289; the lifecycle log needs its own per-entry layout |
| H2 | Review commits stage the whole plan dir | a reviewer's round publishes half-written `plan.md`, `meta.json`, companions | repro | #1289 |
| H3 | Recovery keyed by plan dir, not operation | the retry publishes authoring edits made after the failure; any success clears the marker | repro | #1289 |
| H4 | Same id, different payload | write time: answered `duplicate` (`apply`: "already applied"), so the sender treats its different payload as saved; read time: the later payload is hidden with no diagnostic | fixture `same-id` | #1288 |
| H5 | An autostash conflict on a shared plan file (daemon `pull --rebase --autostash`; ox auto-repairs only `sessions/*/meta.json`) | the next review commit stages the conflicted file, the snapshot refuses its markers, every retry fails the same way until a human repairs Git | repro | #1289 + Ledger |
| H6 | Failed reads not reported | a damaged resolution silently reopens its item; a damaged `remaps.json` silently moves marks back; damaged rounds are reported by `feedback show` only | fixture `corrupt-records` | #1288 |
| H7 | Reads during a replay | mid-rebase the worktree lacks locally committed records; the v1 reader answers as if complete (a re-raised item shows resolved) | repro | #1288 / #1289 |
| H8 | Reads are unbounded | `os.ReadFile` on every record; `apply --from` reads any size | code | #1288 |
| H9 | Writers are not confined create-only publishers | temp+rename replaces an existing entry; review paths are not checked for symlinks (since #1309 a link committed to the Ledger checks out as a plain file; one made locally is still followed) | code | #1288 / #1289 |
| H10 | Daemon GC re-clone captures dirty state before a clone that can take minutes, then deletes the old clone; nothing written in between is captured, and plan writes do not wait on `.gc-swap-lock` | review files written, or committed but not pushed, during the clone are deleted with their retry markers | code: `internal/daemon/sync_gc.go` phase 0 vs swap | Ledger (daemon) |
| H11 | Session-finalize and GitHub-sync pushes push whatever is unpushed | a review commit the CLI's pre-push gate refused can be published by the daemon | code: `session_finalize.go`, `github_sync.go` vs `.claude/rules/daemon-git.md` | Ledger (daemon) |
| H12 | `SquashUnpushed` (over 100 unpushed commits) commits the whole index | staged-but-uncommitted review files join an unrelated squash commit | code: `internal/gitutil/squash.go` | Ledger |
| H13 | `CommitLedgerSnapshot` refuses every commit while any index entry is unmerged | an exact-set review commit is still blocked by an unrelated conflict (relevant to H5's fix) | code: global check in `CommitLedgerSnapshotPreserving` | #1289 + Ledger: decide whether record commits may be built from HEAD plus frozen blobs |

**Reproductions** (real clones of one Ledger with a bare remote, publishing
through `commitAndPushPlanDir`; one-off, not kept as tests because they assert
today's bugs):

- **H1:** two clones each append a different entry to the same shared file
  (legacy `resolutions.json`; `remaps.json`; `events.jsonl` via `approved` on
  one clone and `worked` on the other) and publish in turn. The remote keeps
  only the second publisher's file.
- **H2:** uncommitted edits to `plan.md`, `meta.json` and a new companion, then
  `POST /feedback`. The `plan:` commit carries all three beside the round.
- **H3:** a push fails, `plan.md` is edited, the remote comes back and
  `flush-pending` runs. The retry publishes the round and the later edit.
- **H5:** an uncommitted `remaps.json` edit meets a teammate's `remaps.json`
  edit through `pull --rebase --autostash`. The next review commit fails on
  conflict markers, and so does every `flush-pending` retry.
- **H7:** a rebase stopped at a `break` between two local commits, the second
  re-raising a resolved item. `AssembleReview` shows the item resolved without
  error.

Browser-side hazards (outbox poisoned by a deterministic 400/413, an outbox
resent unchanged while newer marks wait, `/accept` and `/reopen` with no
outbox, no reviewer, and no idempotency) belong to #1291.

Two rounds with the same id and the same payload are *not* a hazard on main:
they assemble once and an acceptance time alone never reopens an item (fixture
`same-id`). Distinct records from racing clones all survive with nothing left
to repair (`TestPlanReviewDurability_RacingClonesKeepEveryRecord`).

---

## 3. Contract choices

### 3.1 Logical operation and scope

- One logical operation is one action that produces **exactly one** record:
  submit a round, reopen an item, resolve or accept an item.
- Identity is `(plan, kind, operation id)`. *Plan* is the plan's `plan_id`
  from `events.jsonl`, else its directory name, never the typed slug (slugs
  can be ambiguous, #1225). *Kind* is `round` or `resolution`.
- The id is minted once by whoever freezes the request and is never re-minted
  on retry: the browser outbox for live submits, `ContentRoundID` for an
  export without one, the CLI for `resolve`. Charset `[A-Za-z0-9_-]{8,64}`.
- An operation's write set is its one record path. A submission never writes
  remaps, lifecycle events, `meta.json` or authoring files.

### 3.2 Equivalent and conflicting payloads

- Same operation means same `(plan, kind, id)`. Same payload means the same
  digest of the **frozen request**: v2 records carry `request_sha256` over the
  exact bytes the client froze; v1 records are compared with the
  `ContentRoundID` canonicalization (reviewer + items; unknown fields ignored),
  taken after each item's empty reviewer is filled from the round's reviewer
  as `SaveFeedback` stores it, so a request and its stored copy compare equal.
- Host materialization (`created_at`, slug, per-item reviewer stamp,
  authenticated identity) is outside the digest, so an acceptance timestamp
  never creates another logical submission.
- **Equivalent:** every physical copy is kept; it assembles once, as the
  earliest acceptance (v1 rule).
- **Conflicting:** every copy is kept and never rewritten; assembly still uses
  the earliest (v1 rule) **and** every reader reports `conflicting_operation`
  with the paths; a writer answers a conflicting retry `conflict`, never
  `duplicate`, so the client keeps its draft.

### 3.3 Immutable, content-derived paths

v1 readers must keep reading v2 records: main reads `feedback/round-*.json`
and `feedback/resolutions/*.json`; v0.20.0 reads every `feedback/*.json`
except `resolutions.json`.

- Rounds: `feedback/round-<ts>-<id>.<digest>.json`.
  Resolutions: `feedback/resolutions/<ts>-<id>.<digest>.json`.
- `<ts>`: acceptance time, as v1 names rounds; UTC, fixed width
  `20060102-150405.000000000`. Copies of one request then sort in acceptance
  order, so the earliest acceptance is the one assembled (fixture `same-id`)
  and file order stays assembly order.
- `<digest>`: the first 16 hex characters of SHA-256 over the record file's
  exact bytes. The same path therefore always holds the same bytes, an
  add/add of identical bytes merges cleanly, and different bytes never meet
  at one path, so nothing is left for keep-one-side resolution.
- `.` cannot occur in an id, so `<id>.<digest>` splits unambiguously. A v1
  writer will not recognize a v2 copy by name and may write a second copy;
  read-time dedup by id still assembles it once.
- Remaps move to the same per-entry shape (`feedback/remaps/…`), read beside
  the legacy file. v1 readers do not see them, so a moved mark shows at its
  original anchor, the documented degraded state.

### 3.4 Frozen-byte checks

- **Write:** create-only. An existing path with equal bytes is the receipt
  `already_published`; different bytes are `conflict` and nothing is replaced.
- **Read:** a v2 record whose bytes no longer hash to its name is reported
  `altered` and still assembled; the capture is incomplete.
- **Recovery:** a pending entry holds each path with its SHA-256; the retry
  re-hashes before staging and never publishes changed bytes under the
  original operation.
- **Publication:** reported only when the remote tip's blob at each path has
  the recorded hash.

### 3.5 Confined create-only publication

1. Walk from the plan dir to the record's directory one component at a time,
   each opened relative to its parent's handle without following a symlink
   (`openat` with `O_NOFOLLOW|O_DIRECTORY`), creating the missing ones. A
   symlink or non-directory fails the publication, including one swapped in
   after an earlier check. `os.Root` alone is not enough: it follows a symlink
   that stays inside the root.
2. Write a dot-prefixed temporary relative to that directory handle with
   `O_CREATE|O_EXCL|O_NOFOLLOW`, fsync, close.
3. `linkat(2)` it to the record name on the same handle. It fails rather than
   replace any existing name, live or dangling symlinks included. Fsync the
   directory, remove the temporary.
4. On `EEXIST`, apply the write check in 3.4.

Readers skip dot-prefixed names, so a temporary is never a record. A spike
during #1287 (macOS, Go 1.27) confirmed the outcomes with an `os.Root` plus a
check of each component: no overwrite, no write through a symlinked name or
directory inside or outside the plan, no traversal, nothing written outside
the plan, and one record left by 16 concurrent publishers. That version
leaves a window between check and use, which the handle-relative walk above
closes. #1288 implements it, with those cases as its tests.

### 3.6 Bounded capture

- Reads the worktree with the same no-follow walk as 3.5, so unsynced records
  are included and a component swapped for a symlink between listing and read
  is a failed read, not followed.
- Every entry under `feedback/` and `feedback/resolutions/` is either a known
  record or a reported failure: unrecognized, not a regular file, over a limit,
  undecodable, gone after listing, or replaced between listing and read.
- **Limits:** 10,000 records, 8 MiB per record, 64 MiB per capture; new
  writers refuse to store a record over 8 MiB. v1 writers have no cap: `ox plan
  feedback apply --from` reads any size, and a stored round outgrows its
  request (indented JSON, the round's reviewer copied into every item). One
  1 MiB request of minimal items, the review server's cap
  (`reviewBodyLimit`), was stored as 7.3 MB (6.9x), an example rather than a
  maximum. A v1 record over a limit is reported, never read past silently,
  and the capture is incomplete. Sampled Ledgers held 12 plans with review
  data: largest record 8.9 KB, at most 9 rounds on a plan, largest review
  subtree 21 KB.
- **Complete** only when nothing failed, the listing before and after the read
  is identical (names, sizes, mtimes, inodes), and no rebase or merge is in
  progress in the Ledger. Three attempts, then incomplete.
- An incomplete capture is never authoritative. A consumer must not report
  "no open feedback", show an item resolved, or approve on one; it shows the
  failures.

The #1287 spike confirmed sustained churn yields incomplete, a single change
is absorbed by the next attempt, and a capture taken at H7's `break` is
incomplete where the v1 reader answers. #1288 implements it, with those cases as its tests.

### 3.7 Exact-set commit and recovery (#1289)

- Stage and commit exactly the operation's write set, through the existing
  `CommitLedgerSnapshot` and repo lock.
- Key pending entries by `(plan, operation)`, holding paths, hashes and, once
  committed, the commit. Retry only that set; clear only that entry, only on
  verified remote bytes. Whole-plan authoring recovery stays separate.
- Acknowledge local ownership (the browser may clear its outbox) only after
  the record and its pending entry are both durable.
- H13 decides whether that is enough under H5; if the Ledger keeps refusing
  all commits during an unrelated conflict, record commits need a path built
  from HEAD plus the frozen blobs.

### 3.8 Retained v1 assembly

Unchanged, and pinned by `TestReviewContractFixtures`:

- latest mark per (canonical anchor, reviewer), in file order;
- latest resolution per canonical anchor, by its `at`;
- open when raised after that resolution;
- the remap chain is applied at read time; records on disk are never rewritten.

---

## 4. Compatibility

Measured on the official `ox_0.20.0_darwin_arm64` release asset (checksum
verified, commit `5428a78a`) and on a build of main at `651ac29a` (unreleased;
it also reports 0.20.0). Each fixture was installed as a plan in a scratch
Ledger and read with `ox plan feedback show <slug> --json` and
`ox plan render <slug> -o page.html`, with `HOME` and every XDG dir in a scratch
dir, `SAGEOX_DAEMON=false` and `DO_NOT_TRACK=1`. Both commands agreed on every
fixture.

| Fixture | v0.20.0 | main |
|---|---|---|
| `resolution-layouts` | h2 **open**: per-entry resolutions are invisible | as `expect.json` |
| `same-id` | h1 **reopened** by the repeated acceptance; h3 from the conflicting payload shown | as `expect.json`; h3 hidden (H4) |
| `corrupt-records` | same items; the torn round is not reported (bare-array JSON) | reports the torn round; resolution and remaps failures unreported (H6) |
| `unknown-fields` | as `expect.json` | as `expect.json` |

| Writer | v0.20.0 | main |
|---|---|---|
| `feedback resolve` | rewrites the shared legacy array (H1) and drops unknown fields of every existing entry | writes a per-entry file; legacy array untouched |
| `feedback apply` twice | two rounds; drops `id` and unknown fields | one round; keeps `id`; drops unknown fields |
| `remaps.json` (from code; `remap.go` is unchanged since v0.20.0) | read-modify-write, drops unknown fields, resets an unreadable file | same |
| `show --json` shape | bare array | `{items, corrupt_rounds}` |

Neither build rewrites a round or a per-entry resolution, so those records and
their unknown fields survive on disk. Mixed teams today: v0.20.0 users see
items resolved by newer clients as open, can see a retried round reopen a
resolved item, and write the shared legacy array, so their resolutions are
subject to H1.

**First fully supported release: none yet.** main reads every fixture as
`expect.json` says but is not released, and still carries H1–H9. A release is
fully supported when it reads every fixture as `expect.json` says, its writers
keep unknown fields and write no shared review file, H1–H9 are fixed, and
conflicts and failed reads are reported. #1294 records that release by
repeating the measurement above against its binary.

---

## 5. Consuming-host probes

A host that reads or writes the same records proves, on its own storage:

1. **Reads:** for every fixture, the items and `corrupt_rounds` in
   `expect.json`, and it reports the gaps v1 leaves silent (H4, H6) as
   conflicts and failures (3.2, 3.6).
2. **Create-only publication:** its write path fails instead of replacing an
   existing path, and refuses a symlink entry (Git mode `120000`) anywhere on
   the record path.
3. **Lost responses:** a retried request finds the committed operation by
   `(plan, kind, id)` before writing, and answers by 3.2.
4. **Bounded capture:** a truncated listing, a lagging replica or a concurrent
   change yields incomplete, never an empty or resolved result.

The contract freezes in #1288 when the module's tests cover 3.5 and 3.6, the
consuming host's probes pass on its storage, and both sides agree on every
`expect.json`. Consuming-host qualification is private-owner follow-up.
