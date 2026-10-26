# Plan review: Ledger concurrency and compatibility contract

**Status:** frozen (#1288). `pkg/planreview` implements and tests 3.5 and 3.6,
and a consuming host passed the §5 probes on its own storage. The three points
that host raised are settled in 3.2 (identity on retries), 3.6 (per-host
bounds) and §5 (what a host proves). The CLI adopted the module in #1289
(§1.1), and the browser's outbox in #1291 (3.9). #1384 added whole-plan
marks (3.10). Follow-up owner: #1294 (certification).

**Goal:** supported concurrent reviewers never lose feedback and never repair
Git conflicts by hand. This spec records what the CLI does today, names the
mechanisms found that lose, hide, block or broaden review data, and fixes the
contract choices the shared module implements.

**Executable half:** the Go module `github.com/sageox/ox/pkg/planreview`
(`pkg/planreview/`, its own `go.mod`; standard library plus
`golang.org/x/net/html`).

| Where | What it pins |
|---|---|
| `pkg/planreview/testdata/review-contract/<fixture>/` | Shared fixtures: a plan dir (`plan/`) and what assembling it yields (`expect.json`): the merged items, projected to the fields every host must agree on (the reviewer's label and note included), every failure by path and reason, and every conflicting operation; `limits` when the fixture needs its own |
| `pkg/planreview/testdata/anchors.json` | The review page's anchor, label and markable-element vectors; the anchors are goldens from a JS engine running review.js. `packages/plan-review` is checked against them: anchors and labels in node, the page in Chrome (`TestBrowser_DocumentToolsMatchSharedPageVectors`) |
| `TestContractFixtures` (`pkg/planreview/review_test.go`) | Every fixture through two transports (`Capture` and a plain read of every file) and the shared assembler |
| `TestPublish_*`, `TestCapture_*` (`pkg/planreview/local_test.go`) | 3.5 and 3.6 |
| `TestReviewContractFixtures` (`internal/plan/feedback_test.go`) | The CLI's reader, `plan.ReadReview`, against every fixture but `oversized` (which sets limits of its own) |
| `TestPlanReviewDurability_RetryPublishesOnlyTheOriginalRecord` (`cmd/ox/plan_review_durability_test.go`) | 3.7: a failed commit and a failed push, then dirty authoring files, another operation's success and a restarted retry; each review commit carries one record and each entry clears only on its own record |
| `TestPlanReviewDurability_RacingClonesAndLostResponses` (same file) | Rounds and resolutions from three clones survive the push rebase; a retried round is one logical submission, on the clone that stored it and on one that never saw it |
| `packages/plan-review/test/state.test.mjs`; in Chrome (`cmd/ox`, tag `browser`) `TestBrowser_UnstorableReviewIsNeverSent`, `…LostAnswerAndAnEditDuringSendDeliverOnce`, `…TwoTabsKeepEachOthersMarks`, `…HostControlsAroundAFramedPlan`, `…ResolveOfferedOnlyToReviewersWhoMay` | 3.9: the browser's outbox |
| `TestRebind_MovesAMarkOnlyWhenTheMoveIsUnambiguous` (`pkg/planreview`); in Chrome `TestBrowser_WholePlanCommentReachesTheLedger` | 3.10: whole-plan marks |

The CLI requires the released module (`pkg/planreview v0.1.0`): `go install
github.com/sageox/ox/cmd/ox@<version>` refuses a `go.mod` with a `replace`
directive. §1 records the CLI as #1287 found it; §1.1 is the CLI since #1289.

---

## 1. Inventory

This is the CLI as #1287 found it. §1.1 describes it since #1289.

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

### 1.1 Since #1289

The CLI reads and writes review records only through `pkg/planreview`.

| Operation | Record | Operation id |
|---|---|---|
| `POST /feedback` (a submission or a reopen), `ox plan feedback apply` | round | the request's `id`, minted by the page's outbox (3.9); else random (`/feedback`) or `ContentRoundID` (`apply`) |
| `POST /accept` | resolution, `verified` only | the request's `id`, minted by the page's outbox |
| `ox plan feedback resolve` | resolution | minted by the CLI |
| `Save` with HTML (author only) | one remap record per move, `feedback/remaps/` | derived from the move; none written from an incomplete read |

- **Write** (`publishReviewRecord`, `cmd/ox/plan_commit.go`): under a lock per
  operation, this clone's own recovery entry for the operation answers first,
  then `Prior` over a capture (a duplicate is answered with the stored copy,
  as committed when HEAD holds it; a conflicting one is a 409 and nothing is
  written). The recovery entry, holding the record's path, SHA-256 and bytes,
  is written durably before the record is published create-only, and only
  then is the operation reported saved. An entry that cannot be read blocks
  its operation, since it may hold the only copy; a write that fails after its
  record reached the disk keeps the entry for the retry. `apply` freezes an
  export canonically, so whitespace or key order never makes a retry a
  different operation.
- **Commit:** the record's bytes are staged as a blob at its one path and
  committed by `CommitLedgerSnapshot` with that path as the only pathspec, so no
  other index entry or worktree file reaches a review commit.
- **Publish:** `pushLedger`, then the remote-tracking tip must hold the
  record's blob at its path; only that verification clears the entry and
  reports `pushed`.
- **Retry:** entries live in `<ledger>/.git/ox-plan-review-pending/`, one per
  `(plan, kind, id)`. `ox plan flush-pending` (started by `ox agent prime`)
  restores a record the worktree lost, then commits, pushes and verifies each
  entry on its own, and stops at the first rejected push. A restore happens
  only under the repo lock and never while a rebase is in progress: a rebase
  replaying the record's commit has the record off disk, and a copy written
  then stops the replay for good. An entry whose plan dir is gone (`ox plan
  backfill` renames plan dirs, records included) is cleared once the remote
  tip holds its bytes anywhere among the plans, and kept otherwise. Plan-dir
  markers remain for authoring commits (the whole dir) and lifecycle events
  (their files). `ox doctor` (`plan-pending-writes`) lists every entry and
  marker still waiting, with its age and why it has not landed (its plan dir
  is gone, it cannot be read, or it is committed but not on the remote). Its
  fix runs on every `ox doctor`, as the session upload retry does, and is the
  retry `ox plan flush-pending` runs, which clears only an entry whose bytes
  the remote tip holds and a marker whose plan dir is gone.
- **Read** (`plan.ReadReview`): `Capture` of the worktree, the Ledger's git dir
  passed for the replay check, then `Assemble`. A clone with `core.autocrlf`
  checks records out with CRLF endings, so the worktree is not byte-exact
  there: a record whose name-pinned digest matches only its bytes with LF
  endings is read with LF endings. `feedback show` (text and `--json`),
  `review await`, the digest, `ox plan list`'s table, prime's count and the
  rendered page all report an incomplete read and an operation stored with
  differing content: the JSON `complete` is false for either, and the list
  and prime surface such a plan even with nothing open.
- **Lifecycle** (`commitPlanEvent`, `cmd/ox/plan_commit.go`): `/approve` and
  the lifecycle verbs append their event and commit only `events.jsonl` and
  `meta.json`, in one hold of the repo lock, so a pull never finds them half
  written and an author's unsaved files never ride along. While the Ledger is
  mid-rebase, locked, or holds unmerged entries they record nothing, and
  `/approve` answers 503. A failed commit or push, from the browser or a CLI
  verb, leaves a plan-dir marker owing only those files (`files`); a
  lifecycle push never clears a marker owed for the whole dir, and no success
  clears a failure recorded after its own attempt began. A verb that records
  nothing new still commits and pushes, retrying an earlier event, and says
  so when that left the plan unshared. With no Ledger checkout the event is
  recorded and reported not committed, and no git command runs. Session stop
  (`reconcileProducedPlansAtStop`) records the session's id and outcome in
  `meta.json`, outside the repo lock, and commits only `events.jsonl` and
  `meta.json`, with the same file-scoped marker. While a whole-dir marker is
  pending for the plan it commits nothing: the plan's latest save is not yet
  shared, and that retry carries `meta.json` with the rest of it.
- **Saves** (`savePlanArtifacts`): a large plan.html, stamped as `Save` will
  write it (`plan.SavedHTML`), is uploaded first, before the repo lock is
  taken: the upload's timeouts run to minutes on a stalled network, and while
  the lock is held the daemon's sync skips its cycles and other Ledger writes
  wait on it. Then, under the lock, a save writes nothing while the Ledger is
  mid-rebase, locked, or holds unmerged entries; otherwise the prior
  revision's snapshot, the writes (companion pages included), the pointer swap
  and the commit run in that one hold, so a daemon pull never finds a save
  half written. The push follows outside it. The pointer replaces plan.html
  only while the file holds exactly the uploaded bytes; when the store did not
  take the page, or the page changed after its upload, the save commits
  nothing. With no store reachable it commits everything but the page. Each of
  these, and a failed commit or push, leaves a whole-dir marker in a Ledger
  checkout, which `ox plan flush-pending` retries with the dir as it is then:
  it uploads a page still awaiting upload before it takes the lock, and while
  the page stays unstored it commits and pushes the rest of the dir and keeps
  the marker.
- **Whole-index commits** (`commitLedgerSnapshot` in `cmd/ox`: the commit
  that uploads a session, at stop and in `ox doctor`'s upload retry, the
  automatic doctor sweeps `ledger-clean-workdir`, `ledger-cache-tracked` and
  `ledger-rej-tracked`, and the `--fix`-only GitHub data migration) commit
  everything but `data/plans/`, through
  `CommitLedgerSnapshot` with an exclude pathspec, and the sweeps stage nothing
  there. A plan file stays as it was, staged or not, as do its retry marker
  and review entries: the plan's own writers and their retries commit it.
  Plan files alone make `ledger-clean-workdir` commit nothing. The `--fix`-only
  session-id backfill commits only the `meta.json` files it rewrote, and H12's
  squash commits the tree of the commits it collapses, so neither carries a
  staged plan file (#1442). The mass-deletion guard does not count removed
  `*.rej` and `*.needs-summary` files (#1443).
- **Summary publication** (#1355): lock, stage and commit only the summary, updated session metadata and required ignore cleanup. A no-op snapshot still attempts publication. Selected maintenance retries require a clean index before automatic rebase; normal pushes and default daemon retry policy are unchanged.
- **Plan changes no retry owns** (`ox doctor`, just before
  `plan-pending-writes`): a plan dir with uncommitted changes, no plan retry
  marker, and changes outside the review records an entry lands. If its
  `events.jsonl` holds more `created` or `revised` events than HEAD's, the
  save ran and never committed (a crash, an older ox, a lost marker):
  `plan-stranded-saves`, automatic, gives it a whole-dir marker and this run's
  retry publishes it. Otherwise the change was never saved:
  `plan-unsaved-edits` lists it in plain `ox doctor` output, and publishes it
  as it is (a whole-dir marker, then the retry) only when
  `ox doctor --fix-slug=plan-unsaved-edits` is confirmed at a terminal or with
  `--yes`. `ox agent prime` retries only what is already queued.
- **Pushes** (`pushLedger`) ignore their caller's cancellation: a cancel
  during the pull-rebase that a rejected push runs kills git and skips its
  abort, leaving the clone mid-rebase. `PushWithRetry`'s own timeouts bound
  the push.
- **Autostash conflicts on save files** (`gitutil.ResolveAutostashConflicts`):
  when the daemon's pull re-applies its autostash over a teammate's change to
  a file a save writes (`plan.md`, `meta.json`, `plan.html`, `annotations.json`,
  `hero.svg`, `companions/`), the local copy is kept, as a rebase of `data/`
  keeps the local commit. Upstream's copy stays in history, and the stash entry
  stays in `git stash list`. `events.jsonl` and review records still refuse.
- **Event logs merge:** `data/plans/*/events.jsonl` is `merge=union` in each
  clone's `.git/info/attributes` (`kb.MergeUnionPaths`, rewritten before every
  push and pull), so two clones' appends merge, upstream's lines first, in a
  rebase and in an autostash re-apply. Union is safe because no writer edits
  a line once appended: a line gets its `session_id`, if it has one, when it
  is written, and session stop records the session only in `meta.json`. An
  edited line next to another clone's append would come out of the merge
  twice.

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
| H13 | `CommitLedgerSnapshot` refuses every commit while any index entry is unmerged | an exact-set review commit is still blocked by an unrelated conflict (relevant to H5's fix) | code: global check in `CommitLedgerSnapshotPreserving` | decided in #1289 (3.7); Ledger owns the refusal |
| H14 | The daemon's sparse-checkout refresh (each sync cycle, daemon startup) and `ox doctor --fix`'s cone repair rewrite the index outside the repo lock | an ox commit holding the lock fails on `index.lock` and leaves its files uncommitted (16 of 60 lifecycle commits in a stress run) | stress run | Ledger (daemon) |

**Since #1289** (§1.1): the CLI fixes H2–H4 and H6–H9, and H1 for review files,
which it no longer writes to any shared file. `events.jsonl` stays shared but
merges (`merge=union`), so H1 no longer drops events, and its autostash
re-apply no longer conflicts; this needs no per-entry layout, and a reader
sees the same file format. Lifecycle writes commit under the repo lock and
refuse while the Ledger needs repair, so they leave no plan file uncommitted
for a pull to conflict on. H14 is fixed: all three refreshes take the repo
lock (`ledger.RefreshSparseCheckout`). The blob fetch a widened cone starts
in a blobless clone fails on a stalled transfer (`gitutil.GitHTTPTimeoutFlags`),
and a refresh still running after `sparseRefreshTimeout` (10 minutes) is
ended, so neither holds that lock for long. Still open:

- `meta.json` stays keep-local on a rebase conflict, so its status can lag
  `events.jsonl`; readers fold the log (`plan.CurrentStatus`) instead of
  trusting it.
- H5 remains only where picking a side would drop entries: `events.jsonl`
  without its union attribute, and review records.
- H10 and H11 are unchanged.

**Since #1442:** H12 is fixed: `SquashUnpushed` commits the unpushed tip's tree,
never the index.

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
  Two v2 rounds must match on both, so a request digest copied onto edited
  marks is a conflict, not a duplicate.
- Host materialization (`created_at`, slug, per-item reviewer stamp,
  authenticated identity) is outside the digest, so an acceptance timestamp
  never creates another logical submission.
- **Identity:** an operation belongs to the reviewer stamped on it. The
  caller's authenticated identity stays outside the request digest, and a copy
  is the same operation only with the same payload *and* the same stamped
  reviewer. A writer answers `already_published` only when the stored copy's
  stamped reviewer is the caller; a retry under another account is a
  `conflict`, never told that someone else's record is its saved review.
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
- Remaps move to the same per-entry shape,
  `feedback/remaps/<ts>-<id>.<digest>.json`, read after the legacy file. The
  id is `m` plus 16 hex characters of SHA-256 over `from`, NUL, `to`, so one
  move computed by two clones shares an id. v1 readers do not see them, so a
  moved mark shows at its original anchor, the documented degraded state.
- **Record fields.** A record keeps the v1 shape so v1 readers read it: the
  frozen request's fields, unknown ones included, with the host's materialized
  fields over them. A round sets `id`, `slug`, `created_at` (the acceptance
  time), `reviewer` (stamped on the round and on every item, replacing any the
  request named) and `request_sha256`, and normalizes each item's `status`. A
  resolution sets `id`, `at`, `reviewer` and `request_sha256`. Records are
  indented JSON with sorted keys; a writer refuses one over 8 MiB.
- **Requests a writer refuses:** an item without an anchor, and a key that
  differs from a known field only in case (`Items`, `ſtatus`). Go's
  `encoding/json` matches field names case-insensitively, so such a key would
  be validated as the field and then stored beside it, where a reader decodes
  whichever sorts last. A duplicate key counts once, as its last value, for
  validation and the record alike.

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
   each opened relative to its parent's handle without following a symlink,
   creating the missing ones. A symlink or non-directory fails the
   publication, including one swapped in after an earlier check. `os.Root`
   alone is not enough: it follows a symlink that stays inside the root. So
   each component is checked with `Lstat`, opened, and kept only when both the
   opened directory and a second `Lstat` are that same directory
   (`os.SameFile`).
2. Write a dot-prefixed temporary relative to that directory handle with
   `O_CREATE|O_EXCL` (which also refuses a symlink at the name), fsync, close.
3. `linkat(2)` it to the record name on the same handle. It fails rather than
   replace any existing name, live or dangling symlinks included. Remove the
   temporary, fsync the directory.
4. On `EEXIST`, apply the write check in 3.4.

Readers skip dot-prefixed names, so a temporary is never a record. `Publish`
implements this, in the standard library (`os.Root.Link`; Windows skips the
directory fsync it cannot do). Its tests are the #1287 spike's cases: no
overwrite, no write through a symlinked name or directory inside or outside
the plan, no traversal, nothing written outside the plan, and one record left
by 16 concurrent publishers.

### 3.6 Bounded capture

- Reads the worktree with the same no-follow walk as 3.5, so unsynced records
  are included and a component swapped for a symlink between listing and read
  is a failed read, not followed.
- Every entry under `feedback/`, `feedback/resolutions/` and `feedback/remaps/`
  is either a known record or a reported failure: unrecognized (another
  directory included), not a regular file, over a limit, undecodable, altered,
  gone after listing, or replaced between listing and read.
- **Limits:** each host passes its own; the semantics do not change. Records
  count in path order, and exceeding a bound makes the capture incomplete,
  never silently shorter. The defaults are 10,000 records, 8 MiB per record
  and 64 MiB per capture; new writers refuse to store a record over 8 MiB. A
  host that pays one request per record may lower the record and capture
  bounds to fit its request timeout, keeping 8 MiB per record so it reads
  anything a writer stores. `Assemble` applies the bounds to any inventory, so
  parsing stays bounded whatever the transport did. v1 writers have no cap: `ox plan
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

`Capture` implements this; its tests are the #1287 spike's cases: sustained
churn yields incomplete, a single change is absorbed by the next attempt, and
a capture taken while a rebase is in progress (H7) is incomplete where the v1
reader answers.

### 3.7 Exact-set commit and recovery (#1289)

- Stage and commit exactly the operation's write set, through the existing
  `CommitLedgerSnapshot` and repo lock.
- Key pending entries by `(plan, operation)`, holding the record's path,
  SHA-256 and exact bytes. Retry only that set; clear only that entry, only on
  verified remote bytes. Whole-plan authoring recovery stays separate.
- Acknowledge local ownership (the browser may clear its outbox) only after
  the record and its pending entry are both durable.
- **H13, decided in #1289:** record commits keep the existing primitive and
  its global refusal. During an unrelated conflict the record and its entry
  stay durable in the clone, the operation is reported saved but not
  committed, and the first retry after the conflict clears publishes it. No
  reviewer action is needed and nothing is lost. A commit built from HEAD plus
  the frozen blob would land while git itself refuses to commit; that changes
  the Ledger's commit contract for every writer and belongs to its owner. H5
  also arises less often since #1289: rounds, resolutions and remaps are no
  longer shared files, so only authoring files can conflict.

### 3.8 Retained v1 assembly

Unchanged, and pinned by `TestReviewContractFixtures` and `TestContractFixtures`;
on `many-rounds` the CLI's v1 reader and the module assemble the same items:

- latest mark per (canonical anchor, reviewer), in file order;
- latest resolution per canonical anchor, by its `at`;
- open when raised after that resolution;
- the remap chain is applied at read time; records on disk are never rewritten.

`Assemble` folds the remaps once for all anchors; folding each anchor on its
own costs anchors × remaps, minutes for a 1 MiB round against a 1 MiB
`remaps.json`.

**What the Ledger bytes cannot prove.** Anyone who can push to the Ledger can
write any review file. A forged copy of another reviewer's operation is
reported as a conflict, but when it sorts first it is the copy assembled, and
nothing in the bytes says who wrote a record. A resolution's `at` is its
writer's clock, so one dated far in the future keeps its item closed until
then. Closing either needs an authority outside the Ledger (push rules, or
receipts signed by the host); this contract does not provide one.

### 3.9 Browser outbox (#1291)

`mountHostControls` in `@sageox/plan-review` is the one browser writer, on
ox's page and in a hosted viewer (`packages/plan-review/src/state.ts`):

- **Keys:** one storage key per draft and per operation, under
  `ox-review:<account>:<plan>:`, so tabs never rewrite each other's keys and
  another account never sees or sends them. ox's page uses the plan's
  directory as `<plan>`: it also names the page's port, so it holds through a
  legacy plan's first save, which mints its plan id. ox's local
  review has no sign-in: its account is `local` and the request names the
  reviewer. A hosted viewer passes its signed-in account and stamps the
  caller itself.
- **Freeze before send:** Submit, Reopen and Accept each mint an id and store
  the frozen request before anything is sent; what storage refuses is never
  sent.
- **Delivery:** oldest first, one at a time, the frozen bytes on every
  attempt, so a mark edited after Submit is stored after the copy Submit
  froze. It stops at the first operation that fails or finds the host
  offline; one refused for good (400, 409, 413) is dropped, its drafts kept.
- **Receipts:** an operation clears only on a receipt naming its id: from
  ox's server `saved: true` (record and recovery entry on disk, 3.7), from a
  hosted one its publication. Its drafts clear only where unedited since.
- **Reopen** is a one-item `request-change` round carrying the item's
  section, label and words. **Accept** is a `verified` resolution, the only
  state `/accept` takes.
- **Upgrade:** drafts and the outbox that pages before 0.3.0 kept under
  `ox-plan-fb:<slug>` move to the new keys; a waiting submission keeps its
  bytes, so its resend is the same operation.
- **Resolving:** Accept and Reopen are offered only when the host mounts the
  controls with `resolve` (#1383); ox's page always does.

### 3.10 Whole-plan marks (#1384)

A mark on the plan as a whole (its direction, scope or readiness) has the
anchor `plan` (`WholePlanAnchor`), an empty section and the label
`Whole plan`.

- **Assembled like any mark:** the latest per reviewer; a resolution naming
  `plan` closes it; a later round raises it again (fixture `whole-plan`).
- **On no element:** `Rebind` never moves it, and the document tools never
  paint or place it. The review controls list it first in the rail, without
  the "text changed" tag, and open its composer from the rail.
- **Released CLIs** read and resolve it. Measured on v0.23.0 with this
  fixture: `ox plan feedback show` and the agent's digest list the open mark
  as `[comment] (plan) Whole plan @Avery — Agree with the split.`, `--json`
  carries both marks, and `ox plan feedback resolve <slug> plan` resolves
  them. Its review page lists both in the rail as notes whose text changed,
  and the open one in the banner of notes no longer anchored.

### 3.11 Drawn regions (#1293)

A drawing is a round item anchored `q-<region id>` that carries its record
under `region` (`plan-review-drawn-context.md`, section 7; fixture
`region-items`).

- **Requests:** a round with a drawing lists its items under `marks`, with
  `items` empty, a shape every earlier release refuses. `ParseRoundRequest`
  checks each region (`testdata/drawn-context/regions.json`) and refuses a
  drawing under `items`, a drawing whose anchor is not `q-<its id>`, and items
  under both keys. A request without a drawing is unchanged.
- **Records:** the items are stored under `items`, and `marks` is not kept.
  A region is stored as the same JSON value the request carried (3.3), and it
  is part of what two copies of a round must agree on (3.2).
- **Assembly:** unchanged. Each drawing has its own anchor, so assembly never
  merges two, a resolution closes only the one it names, and `Rebind` never
  moves a `q` anchor. `Item.Region` carries the record to the reader.

---

## 4. Compatibility

The tables describe older builds. Current test results and release dependencies
are in [the certification matrix](../guides/plan-review-certification.md).

Measured on the official `ox_0.20.0_darwin_arm64` release asset (checksum
verified, commit `5428a78a`) and on a build of main at `651ac29a` (unreleased;
it also reports 0.20.0). Each of the four #1287 fixtures was installed as a plan in a scratch
Ledger and read with `ox plan feedback show <slug> --json` and
`ox plan render <slug> -o page.html`, with `HOME` and every XDG dir in a scratch
dir, `SAGEOX_DAEMON=false` and `DO_NOT_TRACK=1`. Both commands agreed on every
fixture.

| Fixture | v0.20.0 | prototype `651ac29a` |
|---|---|---|
| `resolution-layouts` | h2 **open**: per-entry resolutions are invisible | as `expect.json` |
| `same-id` | h1 **reopened** by the repeated acceptance; h3 from the conflicting payload shown | as `expect.json`; h3 hidden (H4) |
| `corrupt-records` | same items; the torn round is not reported (bare-array JSON) | reports the torn round; resolution and remaps failures unreported (H6) |
| `unknown-fields` | as `expect.json` | as `expect.json` |

| Writer | v0.20.0 | prototype `651ac29a` |
|---|---|---|
| `feedback resolve` | rewrites the shared legacy array (H1) and drops unknown fields of every existing entry | writes a per-entry file; legacy array untouched |
| `feedback apply` twice | two rounds; drops `id` and unknown fields | one round; keeps `id`; drops unknown fields |
| `remaps.json` (from code; `remap.go` is unchanged since v0.20.0) | read-modify-write, drops unknown fields, resets an unreadable file | same |
| `show --json` shape | bare array | `{items, corrupt_rounds}` |

Neither measured build rewrites rounds or per-entry resolutions, so their
unknown fields survive on disk. Mixed teams with v0.20.0 users see
items resolved by newer clients as open, can see a retried round reopen a
resolved item, and write the shared legacy array, so their resolutions are
subject to H1.

**First fully supported release: none certified yet.** Current `feedback apply`
preserves unknown fields and drawing records
(`TestRunPlanFeedbackApply_StoresAnExportedDrawingWhole`). The released
Go module is v0.4.0 and the browser package is 0.6.0 (protocol 3); the latest
released CLI at the 2026-10-20 audit is v0.23.0, whose page predates drawing
writers. H1–H9 describe the older builds. #1294 still needs recovery fixes,
owner review, a fully capable CLI release and consuming-host test results.

---

## 5. Consuming-host probes

A host that reads or writes the same records proves, on its own storage:

1. **Reads:** a host proves its transport: for every fixture, its inventory
   holds every file under `plan/feedback/` byte for byte, and it reports its
   own failure classes. The items, failures and conflicts in `expect.json`
   then come from running `Assemble` over that inventory, which reports the
   gaps v1 leaves silent (H4, H6) as conflicts and failures (3.2, 3.6); no
   host re-implements assembly.
2. **Create-only publication:** its write path fails instead of replacing an
   existing path, and refuses a symlink entry (Git mode `120000`) anywhere on
   the record path.
3. **Lost responses:** a retried request finds the committed operation by
   `(plan, kind, id)` before writing, and answers by 3.2.
4. **Bounded capture:** a truncated listing, a lagging replica or a concurrent
   change yields incomplete, never an empty or resolved result.

The contract froze in #1288: the module's tests cover 3.5 and 3.6, and the
consuming host's probes passed on its storage. That host then agrees on every
`expect.json` by running the released module over its own inventories.
Consuming-host qualification is private-owner follow-up.
