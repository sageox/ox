# COE: Daemon Git Sync — FETCH_HEAD Race, Latched Recovery, and git-lfs Divergence

**Incident window:** 2026-09-02 (latent since 2026-02-16; see "How long it lived")
**Status:** Draft for team discussion
**Timezone:** All timestamps Pacific Daylight Time (PDT, UTC-7)
**Issues filed:** bd epic `ox-baz5` (`ox-baz5.1` … `ox-baz5.6`), `ox-zis2`
**Plan of record:** `ox plan view 2026-09-02-daemon-git-sync-five-fixes` (HTML in the ledger)

## Immediate symptoms

A single `ox status` in `sageox-monorepo` produced three things that looked like data corruption and were not.

**Symptom A — ledger sync fails with a git error nobody had seen before.**

```
pull failed: fatal: Cannot rebase onto multiple branches.: exit status 128
```

**Symptom B — a red, confirm-required error that outlives the problem.**
`ox status` and `ox daemon status` showed *ledger is stuck in a broken rebase state. Run
'git rebase --abort' manually or 'ox doctor --fix' to recover* — while the ledger was on
`main`, clean, up to date, and had synced 19 seconds earlier. `git rebase --abort` had
nothing to abort. Only a daemon restart clears it.

**Symptom C — the SageOx Internal team context had stopped syncing.**
`ahead 3, behind 534`, one modified attachment, four orphan `autostash` entries, and the
session-start warning *team_xlcr6yzpec has diverged from remote and rebase failed*.
`ox doctor --fix` reported nothing about it.

A and B share one cause; C is independent. All three were found in the same session because
the investigation of A led through the daemon's recovery path (B) and then into the only
other diverged clone on the machine (C).

---

## What happened (issue timeline)

Reconstructed from `ox daemon logs`, the ledger's `.git/FETCH_HEAD`, and live reproduction
against the real ledger clone.

### Cascade 1: two fetches, one `FETCH_HEAD`

| When (PDT) | Actor | What happened |
|---|---|---|
| 11:27:11.2 | `ox status` | Auto-starts daemon 66708 for the monorepo. The daemon immediately starts **three** concurrent activities on the same ledger clone: the sync scheduler, a doctor pass (`autofix RunNow`), and codedb indexing. `ox status` itself is still running its own ledger status check. |
| 11:27:15.5 | daemon sync | `git fetch --quiet` completes (1.86 s). |
| 11:27:15.5–16.5 | second fetcher | A second `git fetch` on the same clone overlaps the window between the daemon's explicit fetch and the fetch that `git pull` performs internally. git opens `FETCH_HEAD` with `O_TRUNC` and appends — **there is no lock on `FETCH_HEAD`**. Both writers land. The file now has six lines instead of three: `main` appears **twice** as a merge-eligible head. |
| 11:27:16.5 | daemon sync | `git pull --rebase --autostash --quiet` reads `FETCH_HEAD`, counts two merge heads, refuses: `fatal: Cannot rebase onto multiple branches.` **Symptom A.** |
| 11:27:16.6 | daemon recovery | The pull-failure handler assumes a conflict. `gitutil.IsRebaseInProgress()` returns true — because the **other** process's `rebase-merge/` directory exists — so it runs `git rebase --skip`. That fails: `Unable to create '.git/index.lock': File exists` (held by the other process). |
| 11:27:16.8 | daemon recovery | `AuditAndAbort` → `git rebase --abort` — same `index.lock` collision. The handler records `IssueTypeRebaseStuck` (severity error, `RequiresConfirm`). **Symptom B.** Sync enters backoff. |
| 11:28:16 | daemon sync | Retry. The fetch rewrites `FETCH_HEAD` cleanly; the pull succeeds. The ledger is healthy. **The error is not cleared** — no code path clears `IssueTypeRebaseStuck`. It stays until the daemon is restarted. |

**Reproduction (on the real ledger, read-only apart from `FETCH_HEAD`):**

```
2 concurrent `git fetch`                → FETCH_HEAD lines=6, merge_heads=2   (5 / 5 trials)
`git fetch` spam racing `pull --rebase` → rc=128 "Cannot rebase onto multiple branches"  (2 / 6 trials)
```

The unserialized fetchers on one clone at the time of the incident:

| Site | Caller | Since |
|---|---|---|
| `internal/daemon/sync_managed.go:252` | sync scheduler pull cycle | 2026-02-16 (v0.1.0) |
| `internal/ledger/ledger.go:331` (`checkSyncStatus`) | CLI ledger status | 2026-02-16 (v0.1.0) |
| `internal/daemon/sync_gc.go:265` (`ledgerSyncWedged`) | GC / wedge probe, own schedule | 2026-07-19 (`e95eb20e`) |
| `cmd/ox/doctor_ledger_git.go:404,476` | `ox doctor --fix` | — |

`gitutil.MinFetchHeadAge` (30 s) is a *dedup heuristic* — it skips a fetch if one happened
recently. It cannot stop a fetch that starts inside another fetch's window. `HasLockFiles`
cannot see this either: `FETCH_HEAD` has no lock file to detect.

### Cascade 2: a file that can never match `HEAD`

Independent of cascade 1. Observed on the SageOx Internal team-context clone.

| Fact | Detail |
|---|---|
| The team-context repo ships `.gitattributes` | `discussions/**/attachments/**/*.jpg filter=lfs …` — server-side, deliberate (ADR-085 in the owning service; attachments are served through the API's LFS resolver). |
| The user has git-lfs installed globally | `~/.gitconfig`: `filter.lfs.required=true`, `filter.lfs.smudge=git-lfs smudge`, `filter.lfs.process=git-lfs filter-process`. |
| One committed attachment is a **nested pointer** | Index blob: a pointer to a **132-byte** object (`oid f18c9d…`, `size 132`). 132 bytes is the size of another pointer. The known #2885 shape. |
| git-lfs smudge unwraps **one** layer on checkout | Worktree gets the inner pointer (`oid 10239e…`, `size 2497503`). Worktree ≠ index, always. |
| Every daemon cycle | `pull --rebase --autostash`: stash the "modified" file → re-checkout `HEAD` → smudge → modified again → `cannot rebase: You have unstaged changes` → autostash left behind. **+1 orphan stash per cycle.** |
| Last successful sync | 2026-09-01 19:00 UTC. First orphan autostash 2026-09-02 11:21. 534 commits behind by 12:00. |

`ox doctor` has a check for exactly this (`checkDoubleEncodedLFSPointers` +
`restoreRawLFSPointers`, `cmd/ox/doctor_team.go`, added 2026-08-14 in #768). Its inputs were
verified to match this clone. It did not fire: the "Team Context" doctor section completes in
~23 ms on both repos — it never scans the clone. The per-team loop iterates
`localCfg.TeamContexts`, which does not include this clone.

### Cascade 3 (found while documenting): a half-cloned ledger that commits nothing

The ox repo's own ledger had `Clone failed: git clone failed: fatal: early EOF` at session
start. It was left with `.git/HEAD → refs/heads/.invalid`, a `refs/heads/.invalid` file,
and every tracked file staged as new. Every commit since fails with `cannot lock ref 'HEAD':
reference already exists`. `ox plan save` warned *commit/push failed, deferring*; sessions
recorded in this repo are not reaching the ledger. `ox doctor` flags it (`Ledger clean
workdir (commit failed)` with auto-fix) but its fix fails with `No such ref: HEAD`.
Tracked as `ox-baz5.6`; not analysed further here.

---

## Root causes

Three independent code defects and two amplifiers.

| # | Defect | Class |
|---|---|---|
| 1 | No cross-process mutual exclusion on git operations against a managed clone. Daemon goroutines and CLI processes fetch the same clone concurrently. | concurrency |
| 2 | The pull-failure recovery ladder acts on *any* rebase directory, not one it created — running `rebase --skip` / `--abort` on a concurrent process's in-flight rebase. | ownership |
| 3 | `IssueTypeRebaseStuck` is raised and never cleared. | state hygiene |
| 4 | ox git invocations inherit the user's global git-lfs clean/smudge filters. ox's own policy (ADR-007) is to never depend on the git-lfs binary — but it only enforced that on the *push* side (`StripLFSConfig`), not on checkout. | insulation |
| 5 | The doctor check written for #4's failure mode does not run against the clone that has it. | detection |

## Why it survived — the actual lesson

Bug 1 has been in the code since v0.1.0 (2026-02-16). Bugs 2 and 3 since 2026-04-01
(#392). Bug 4 since `NewNetworkCmd` landed on 2026-06-08 (#649). None of them is subtle
once you look. They survived seven months because each one hid the others:

1. **The error message pointed away from the cause.** *Cannot rebase onto multiple branches*
   reads as a branch-config problem. The clone's config was correct. Nobody would think
   "two fetches overlapped" from that string.
2. **The transient became permanent.** A retry one minute later succeeded — but the red
   `IssueTypeRebaseStuck` error stayed on `ox status`, with instructions to run a git command
   that did nothing. Every user who saw it concluded "my ledger is corrupt", ran the
   suggested command, saw no change, and restarted the daemon. The restart cleared the
   symptom and destroyed the evidence.
3. **The recovery path made it worse and looked like a different bug.** The `index.lock`
   collision in the abort path reads as "a git process crashed", which is a documented,
   self-healing condition (`RemoveStaleLockFiles`). It was actually the daemon fighting a
   live process.
4. **The last line of defence was silent.** `ox doctor` is the project's stated backstop
   ("detects and repairs every known failure mode"). For cascade 2 the check existed and
   passed. A passing check is more misleading than a missing one.
5. **The symptom looked user-side.** git-lfs is the user's install; `.gitattributes` is the
   server's; the nested pointer is upstream content. Every ingredient of cascade 2 is
   "not ox's fault" — which is exactly why ox has to insulate against all of them.

The generalisable rule: **a daemon that repairs failures must be able to tell its own
in-flight operations from someone else's, and must clear every issue it raises.** Both
failures here are the same shape as the April COE (silent autostash-pop conflict committed
to the ledger): a `--quiet` git pipeline whose error path assumes it is the only actor.

---

## Actions

| Action | Issue | Owner | Priority | Status |
|---|---|---|---|---|
| Per-clone `flock` around fetch / pull / rebase at all four sites; re-fetch-and-retry once on the exact `multiple branches` error | `ox-baz5.1` | Ryan | P1 | [PR #868](https://github.com/sageox/ox/pull/868) — merged (`a7466f08`) |
| Recovery ladder acts only on a rebase this pull started | `ox-baz5.2` | Ryan | P2 | [PR #868](https://github.com/sageox/ox/pull/868) — merged (`a7466f08`) |
| Clear `IssueTypeRebaseStuck` and the sync-suspended warning on the next successful pull | `ox-baz5.3` | Ryan | P2 | [PR #868](https://github.com/sageox/ox/pull/868) — merged (`a7466f08`) |
| Every ox git invocation passes `-c filter.lfs.smudge=cat -c filter.lfs.clean=cat -c filter.lfs.process= -c filter.lfs.required=false` | `ox-baz5.4` | Ryan | P1 | not started |
| `ox doctor` walks every team-context clone the daemon syncs; nested-pointer fixture | `ox-baz5.5` | Ryan | P2 | not started |
| Atomic ledger clone; doctor repairs `HEAD → .invalid` without discarding staged content | `ox-baz5.6` | — | P1 | not started |
| ADR-007 addendum (checkout-side insulation) and ADR-030 (per-clone serialization) | this PR | — | — | this docs PR |

## Post-merge: what review caught in the fix itself

Two rounds of CodeRabbit review on PR #868 found the *same bug class this COE describes*,
twice more, inside the fix's own first draft:

- **`fixLedgerBranchBehind`** (`cmd/ox/doctor_ledger_git.go`, part of `ox doctor --fix`) ran
  `pull --rebase --autostash` plus the LLM-conflict-resolve/`AuditAndAbort` ladder completely
  outside `WithRepoLock` — the exact unlocked-pull shape cascade 1 describes, just in a
  fourth call site nobody had traced through yet. It also lacked the D3 own-rebase guard.
  Both fixed in the round-2 commit.
- **`sync_team.go`** cleared `IssueTypeRebaseStuck` on the wrong key (`ws.ID` instead of the
  `repoName` `pullManagedRepo` actually raised it with) — a latent no-op that would have
  reproduced the "error outlives the problem" symptom for team contexts specifically. Caught
  before merge, not after.

Consistent with "why it survived" above: this failure mode is easy to reintroduce by hand,
one call site at a time, because each site *looks* like ordinary error handling until you
know to ask "does this touch a rebase it might not own, under a lock it might not hold."
Adversarial review closed the gap the first pass missed — not tooling, not a lint rule.

Closing the diff-coverage floor on the merged PR needed two time-boxed exceptions
(`.config/coverage-ratchets.json`, expire 2026-10-17, tracked as `ox-baz5.7`) for
pre-existing LLM-resolver/`AuditAndAbort` code that entered the diff only by being
re-indented, plus 10 new tests for everything genuinely new — real lock contention from a
goroutine, not mocks, `-race` clean.

## Manual recovery (what was done on the affected machine)

- Monorepo ledger: one clean `git fetch` + `git pull --rebase`. Nothing else needed — the
  clone was never actually broken. The stale error cleared on daemon restart.
- Team context: restore the raw index pointer with filters bypassed, then rebase with
  filters bypassed, then drop the orphan autostashes:

  ```bash
  T=~/.local/share/sageox/sageox.ai/teams/team_xlcr6yzpec
  NOLFS="-c filter.lfs.smudge=cat -c filter.lfs.process= -c filter.lfs.required=false"
  git -C "$T" $NOLFS checkout -- PATH_TO_ATTACHMENT  # replace with the path `git status` reports as modified
  git -C "$T" $NOLFS pull --rebase --autostash
  # drop only the orphan autostash entries the loop above produced — never
  # `stash clear`, which would also delete any unrelated stash a teammate has
  # legitimately pending. Re-querying `stash list` each iteration (rather than
  # dropping from a pre-captured list) sidesteps index shifting after each drop.
  while ref=$(git -C "$T" stash list | grep -im1 autostash | cut -d: -f1); [ -n "$ref" ]; do
    git -C "$T" stash drop "$ref"
  done
  ```

  This is precisely what `restoreRawLFSPointers` does; it is here because the doctor check
  that should have invoked it did not run (`ox-baz5.5`).

## References

- `docs/coes/2026-04-07-multi-node-write-conflicts.md` — same failure class (silent `--quiet` git pipeline, error path assumes a single actor)
- ADR-007 — Direct LFS Without git-lfs CLI (push-side insulation; amended by this incident)
- ADR-030 — Per-clone serialization of git operations (new)
- `.claude/rules/daemon-git.md`, `.claude/rules/lfs-no-git-lfs-binary.md`
- gastownhall/beads #4176 / #6080 / #6092 — unrelated bd migration failures hit in the same session; documented in the team rule `agents/rules/beads-dolt-v53-migrated.md`
