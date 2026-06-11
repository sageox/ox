# COE: Multi-Node Write Conflicts & Cascade Failures

**Incident window:** 2026-04-06 (cleanup through 2026-04-09)
**Status:** Draft for team discussion
**Timezone:** All timestamps Pacific Daylight Time (PDT, UTC-7)
**Issues filed:** [#438](https://github.com/sageox/ox/issues/438), [#439](https://github.com/sageox/ox/issues/439), [#441](https://github.com/sageox/ox/issues/441), bd `ox-bvb`, `ox-cs2`, `ox-kq8`

## Immediate symptoms

Two seemingly unrelated symptoms surfaced during the same investigation window. They turned out to share a single underlying cause class.

**Symptom A — Uncommitted `memory/.session-facts/` files after `ox distill`.**
On a remote machine, `ox status` showed a mix of *untracked* and *modified* files under `memory/.session-facts/<date>/*.jsonl`. Files like `memory/.session-facts/2026-02-13/2026-02-13T14-56-ajit-OxmoZK.jsonl` were staged as **modified** — raising the question of why historical summaries were being touched at all.

**Symptom B — `ox index github` unmarshal failure.**
On the same remote machine:

```
unmarshal PR file for backfill failed
  path=…/data/github/2026/04/01/pr/409.json
  error="invalid character '<' looking for beginning of object key string"
```

The PR cache file contained content starting with `<` — investigation later revealed this was the start of a `<<<<<<< Updated upstream` git merge conflict marker that had been silently committed into the file 4 days earlier.

---

## What happened (issue timeline)

The actual chronological sequence of events in the system, reconstructed from `git log`, ledger commit history, and the findings of the two debug sessions (preserved verbatim in [Appendix A](#appendix-a-debugging-timeline)). Both symptoms surfaced on 2026-04-06, but the underlying corruption began days earlier and went undetected.

There are **two parallel cascades**, each with its own mechanism. They share a class of root cause but they did not produce each other.

### Cascade 1: `409.json` — multi-node thrash → silent stash-pop conflict

The corruption of `data/github/2026/04/01/pr/409.json` happened on **2026-04-02**, four days before anyone noticed. Reconstructed from `git log` of the file in the ledger repo plus the per-commit content diff captured during the investigation:

| When (PDT)                          | Author / Node                 | Commit    | What happened                                                                                                                                                                                                                                                  |
| ----------------------------------- | ----------------------------- | --------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 2026-04-01 16:35:24                 | Ajit Banerjee (remote daemon) | `3bea6e5` | `SyncPRs` creates `data/github/2026/04/01/pr/409.json` with `comments=1`. First write of this cache key.                                                                                                                                                       |
| 2026-04-01 16:50:23                 | Ajit Banerjee (remote daemon) | `242e1da` | `SyncPRs` re-syncs the same PR. State unchanged, but the rewrite is a fresh-snapshot replacement, not a merge — and **silently truncates `comments` from 1 → 0** (the #438 comment-drop bug, manifesting in the wild for the first time). Net `−9, +2`.       |
| 2026-04-02 07:50:19                 | Galex Yen (local node)        | `174e37b` | A *different node* fetches PR 409 fresh from the API and writes the full content (`comments=38`, `commits=2`). Net `+274`. The deterministic filename pattern means both nodes resolve the same path; whoever writes last wins.                                |
| 2026-04-02 09:22:05                 | Ajit Banerjee (remote daemon) | `0c45f84` | **The fatal sequence — see box below.** Commit `0c45f84` lands with `<<<<<<< Updated upstream` / `=======` / `>>>>>>> Stashed changes` markers literally embedded inside the JSON. **The corrupt file is now in the shared ledger.**                            |
| 2026-04-02 09:22 → 2026-04-06 13:12 | (latent)                      | —         | The corrupt `409.json` sits in the ledger for **4+ days**. Routine syncs touch other files; nothing has needed to read PR 409 specifically, so the corruption is invisible.                                                                                    |
| 2026-04-06 13:12                    | Galex (local)                 | —         | Galex runs `ox index github`. The backfill loop reads `data/github/2026/04/01/pr/409.json` and `json.Unmarshal` fails with `invalid character '<' looking for beginning of object key string`. **Symptom B observed**, 4 days late.                            |

**The fatal sequence at `0c45f84` (2026-04-02 09:22 PDT, Ajit's remote node), reconstructed from the investigation:**

1. In-process `SyncPRs` runs and rewrites `409.json` to the working tree. The PR's `state` is unchanged from the previous sync, so the comment-drop bug (#438) strips `comments` again. The working tree now has a divergent dirty version of `409.json`.
2. The daemon's pull cycle fires next, running `git pull --rebase --autostash --quiet` (`internal/daemon/sync_managed.go:215`).
3. `--autostash` stashes the dirty `409.json` before the rebase.
4. The pull brings in upstream content from another node — Galex's restored version with comments and commits, committed at 07:50.
5. After the rebase succeeds, autostash pops. The pop tries to apply the dirty (stripped) version on top of the pulled (full) version. **Conflict.** Git writes `<<<<<<< Updated upstream` / `=======` / `>>>>>>> Stashed changes` markers into `409.json` in place.
6. `--quiet` suppresses git's stash-pop conflict warning. **No code path between the autostash pop and the next commit checks for `<<<<<<<` markers.**
7. The next `git add data/github/` blindly stages the conflict-marked file.
8. Commit `0c45f84` is created with the conflict markers literally embedded inside the JSON.
9. Push to the shared ledger. Other nodes start pulling the corrupted file.

The corruption mechanism is the *interaction* of two independent bugs: the comment-drop bug (#438) producing a divergent dirty file, AND the daemon's silent autostash-pop pipeline committing the conflict. Either bug alone would have been survivable.

### Cascade 2: `.session-facts/` — manual cleanup → regeneration → parser fallback

The `.session-facts/` symptom was a **separate cascade** triggered three days before observation, with a different but related set of causes:

| When (PDT)             | Actor / Node                       | What happened                                                                                                                                                                                                                                                                                                                                                                                                                         |
| ---------------------- | ---------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| (predates incident)    | distill                            | `cmd/ox/distill_sessions.go:86-100` writes a placeholder JSONL marker when a session has zero extractable facts (so `scanPendingSessions` skips it on the next run), but **never calls `commitMemoryFile()`**. These markers exist as untracked files in working trees, accumulating across runs.                                                                                                                                  |
| 2026-04-03 ~14:01      | Ryan (remote node, manual command) | Runs `ox doctor recover sessions` on the remote machine as part of a session-summary cleanup. (Per out-of-band conversation; the in-session investigation could only speculate about *who* triggered it.) Ledger commit `f2eb49d` restores **302 sessions** from LFS stubs and stamps them with `.needs-summary` markers.                                                                                                            |
| 2026-04-03 14:41–15:37 | Daemon anti-entropy (remote)       | Detects the `.needs-summary` markers and re-enqueues them for LLM summarization via `claude -p`. A mass "finalize session" pass runs over **58 sessions**.                                                                                                                                                                                                                                                                          |
| 2026-04-03 14:41–15:37 | `claude -p` subprocess (remote)    | On the remote machine, `claude -p` cannot complete the summarization. Instead of structured JSON, it returns **agentic planning text**: `"I need permission to run commands and write files. The summary JSON is ready — here's what I found in the session..."`. The summarizer's JSON parser fails.                                                                                                                              |
| 2026-04-03 14:41–15:37 | Summarizer fallback                | The fallback path dumps the raw LLM output into the `summary` field, sets `quality_score: 1` and `score_reason: "unparsable LLM output, defaulting to upload"`, and propagates the bad-but-uploaded artifact downstream. The session-fact extraction pipeline later rewrites the corresponding `.session-facts/*.jsonl` files because the upstream `summary.json` content changed. The rewrites get `git add`'d but never reach a clean commit. |
| 2026-04-03 → 2026-04-06 | (latent)                          | The mix of *untracked* no-events markers (from the predating distill bug) and *modified* regeneration-victim markers (from the Apr 3 cascade) sits in the working tree on the remote machine.                                                                                                                                                                                                                                       |
| 2026-04-06 17:29       | Galex (remote)                     | Galex runs `ox status` on the remote machine and sees the mix: untracked `.session-facts` markers alongside *modified* `.session-facts` markers for old sessions like `2026-02-13T14-56-ajit-OxmoZK.jsonl`. The added-vs-modified asymmetry is what makes the symptom confusing — it's the signature of two different bugs intersecting on the same directory. **Symptom A observed.**                                              |

> **Data loss.** Cascade 2 was not just confusing git state — it overwrote real content. **58 session `summary.json` files had their `summary` field silently replaced** with the literal string `"I need permission to run commands and write files. The summary JSON is ready — here's what I found in the session..."` (the parser-fallback dump of `claude -p`'s permission-error planning text). The original LLM-generated summaries were pushed to the shared ledger as the new "current" version. Downstream daily / weekly / monthly distill outputs consumed the corrupted summaries before anyone noticed. **The original content survives in git history**, but the corrupted summaries propagated through fact extraction and distill artifacts before the fix in PR #460 (`254a554`) landed. As of this draft, no recovery sweep has been run to restore the affected sessions from history.

The two cascades share a class of root cause (single-writer-per-repo assumptions, silent commitment of unchecked working-tree state) but they have different triggers: 409.json was triggered by an automated sync race, the .session-facts cascade was triggered by a manual cleanup command run by a human on the remote machine.

### Fix and cleanup

| When (PDT)             | Action                                                                                                                                       |
| ---------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| 2026-04-06 16:51–16:56 | bd issues `ox-0js` / `ox-bvb` / `ox-2kg` filed; GitHub issues **#438**, **#439**, **#441** filed in a 27-second burst.                       |
| 2026-04-06 17:26       | `ox-0js` (content-hash filenames + backward compatibility) lands; 12,279 tests pass.                                                          |
| 2026-04-06 20:05       | **#438** closed.                                                                                                                              |
| 2026-04-06 23:00       | **#439** and **#441** closed.                                                                                                                 |
| 2026-04-07 15:36       | `a9eca44` — `ox doctor: auto-commit ledger changes` deletes the legacy `409.json` (among other cleanups).                                     |
| 2026-04-09 10:00       | `18a3408` — bulk rename of all legacy `<num>.json` → `<num>-<hash>.json` files (the content-hash migration sweeping the rest of the ledger). |

The bd issues `ox-cs2` (`.session-facts` deterministic filenames) and `ox-kq8` (`extractSessionFacts` has no lookback window) remain **open** as separate work items.

---

## 5 Whys

### Symptom A — uncommitted/modified `.session-facts/`

**Q1: Why did we have dirty files in the team context working tree?**
Because they weren't committed. Two distinct populations were both present and being conflated by the symptom:
- *Untracked* placeholder markers: distill writes a "no-events" marker file (so `scanPendingSessions` skips the session next time) but at the time of the incident the marker write path did not call the commit helper. The file landed on disk via `os.WriteFile`, never made it into a commit, and the next `git add --sparse memory/` from a different code path silently picked it up.
- *Modified* tracked markers: existing committed `.session-facts/*.jsonl` files were rewritten by the Cascade 2 regeneration pass (see Q6 / Q7). The rewrites got `git add`'d but never reached a clean commit, because the upstream `summary.json` they depended on was itself the broken parser-fallback garbage.

**Q2: Why weren't they committed?**
Because we don't consistently route every memory write through a single commit helper — discipline is convention-only. The codebase has a canonical helper (see Q3), and the *distill* paths now use it consistently, but other write paths bypass it. The clearest example today is `cmd/ox/memory_put.go:166-178`, which inlines its own `git add --sparse` + `git commit` pair without the unstage-on-failure protection. There's no compile-time guard, no lint rule, and no shared interface — it's "remember to call the helper or write the same thing yourself."

**Q3: Is there a common mechanism for ensuring commits happen, and is it broken?**
There is. **`commitMemoryFile(tcPath, relPath, commitMsg)` at `cmd/ox/distill_write.go:230`** is the canonical helper — `git add --sparse` → `git commit` → on commit failure, `git reset HEAD --` to unstage. At the time of the incident it was incomplete in two ways that have since been patched in [PR #447](https://github.com/sageox/ox/pull/447) (`870e640 fix(distill): unstage files on commit failure and commit empty markers`):
- It did not unstage the file on commit failure. A failed commit (hook rejection, "nothing to commit" misclassification, etc.) left the file in the index, where the next daemon `git pull --rebase --autostash` would stash it — feeding directly into Cascade 1's failure mode. The fix is now visible in the file at lines 250-256, and the comment is explicit: *"unstage the file so a failed commit doesn't leave dirty index state that persists across daemon --autostash sync cycles."* That comment is the patch, not the bug.
- The no-events marker path in `cmd/ox/distill_sessions.go` did not call `commitMemoryFile` at all. It now does, at line 103, with a follow-up `os.Remove` cleanup at lines 105-107 if the commit fails. Same fix, same PR.

So the helper existed but had two known holes. Both holes were a consequence of the same design gap: the helper was opt-in, and individual code sites could (and did) write their own variant.

**Q4: Would anti-entropy have resolved this?**
**No.** The daemon's anti-entropy and `ox doctor` cycles do detect-and-repair certain things, but not uncommitted memory files. The closest analogues all miss this case:
- `cmd/ox/doctor_session_uncommitted.go:80` runs `git add --sparse sessions/ && git commit` on uncommitted state — but only for the `sessions/` directory in the ledger, not `memory/` in the team context.
- `internal/ledger/github_migrate.go:35-73, 133` deletes files containing `<<<<<<<` markers — but only as part of the GitHub data migration step that runs once, not on routine sync passes.
- The daemon's pull error path at `internal/daemon/sync_managed.go:217-276` checks `git status --porcelain` for `UU` and would catch unmerged files — but only when the pull command itself returns non-zero. With `--quiet` and a successful rebase, the autostash-pop conflict path returns exit code 0, so this code never runs (see Symptom B Q4 below).

There is no daemon pass that says *"look at `memory/` for staged-but-uncommitted files and reconcile them."* The gap is what let the dirty markers accumulate for days.

**Q5: What was missed?**
A routine post-condition guard for *"the team-context working tree should be clean after every distill cycle."* The guard could live in any of several places, none of which exist today: in the daemon's anti-entropy loop, in `ox doctor` as a memory-state checker, in `commitMemoryFile` itself as a post-condition assertion, or as a `git status --porcelain` check at the end of a distill run that fails loudly if anything is staged-but-uncommitted. The closest thing to such a guard was the manual cleanup command Ryan ran on 2026-04-03 — and that's what triggered Cascade 2.

**Q6: Why did the summarization get retriggered for sessions that already had summaries? Why were already-summarized sessions hydrated for resummary?**
This is the question with the least confident answer; the trail goes cold at the manual-command boundary. Best read of the code:
- The `ox doctor recover sessions` flow restores session directories from LFS pointer stubs to local content (commit `f2eb49d`, 2026-04-03 14:01).
- The recovery path uses `.needs-summary` markers (`internal/session/summary_marker.go:20 WriteNeedsSummaryMarker`) to signal that a session was recovered with stub artifacts and needs LLM summarization. `cmd/ox/agent_session_recover.go:127 recoverFromCache` is one of the writers.
- The daemon's anti-entropy then detects the markers via `FindSessionsNeedingSummary` (`summary_marker.go:53`) and re-enqueues each session into the `SessionFinalizeHandler` work queue for `claude -p` summarization.
- **Critically: there is no idempotency check in this pipeline.** `missingArtifacts` (`session_finalize.go:1140-1149`) only checks file *existence*, not content validity. Nothing checks "does this session already have a valid, non-stub `summary.json`? if yes, don't re-enqueue." The marker mechanism is the trigger, and the trigger has no veto.

**Q7: Why did the summarization get emptied — why did 58 sessions lose their original summaries?**
Because the summarizer's parser fallback at `internal/daemon/agentwork/session_finalize.go:631-648` accepted unparsable LLM output as a valid summary and pushed it through the upload path. When `claude -p` returned the literal string `"I need permission to run commands and write files. The summary JSON is ready — here's what I found in the session..."` instead of structured JSON, `ParseSummaryJSON` failed. The fallback constructed a `SummarizeResponse` with the raw output as `summary` and (at the time of the incident) **`QualityScore: 1` and `ScoreReason: "unparsable LLM output, defaulting to upload"`**. With `QualityScore: 1` above the upload threshold (0.3), the disposition path at `session_finalize.go:663` was `QualityUpload`, not `QualityDiscard`, and the corrupted summary got written to the session directory and pushed to the ledger.

The lack of guards, in order of how cheaply each could have stopped the data loss:
- **No "is this session already summarized successfully?" idempotency check** before re-running `claude -p` (Q6).
- **No content-validity check** in the parser fallback. The fallback's job was "salvage what we can from a broken response." There was no separate path for "the response is so broken we should not produce a summary at all."
- **The fallback's default disposition was `upload`, not `retry`, `quarantine`, or `discard`.** The choice of `QualityScore: 1` is what makes this catastrophic — a score of `0` would have triggered discard and the original summary would have been left intact.
- **No "compare the new summary to the existing one" sanity check.** A pre-write diff that flagged "new summary is dramatically smaller and contains agent meta-output" would have caught it.

What's been fixed since:
- [PR #460](https://github.com/sageox/ox/pull/460) (`254a554 fix(session): validate summary content to prevent agent meta-output contamination`) added `ValidateSummaryContent` (called at `session_finalize.go:651-657`) which sets `QualityScore: 0.0` on validation failure. The current parser fallback at line 642-644 also sets `QualityScore: 0.0` directly on parse failure, not 1.0. With `QualityScore: 0.0` < discard threshold (0.1), the session is now removed via `os.RemoveAll(payload.SessionDir)` at `session_finalize.go:672` instead of uploaded.
- The data loss for the 58 sessions overwritten on 2026-04-03 is **not** fixed by these patches. The corrupted summaries were already pushed and consumed by downstream distill artifacts before the fixes landed. The original content survives in git history; recovery has not been attempted as of this draft.

### Symptom B — `ox index github` unmarshal failure on `409.json`

**Q1: Why did we get the unmarshal error?**
Because the `data/github/2026/04/01/pr/409.json` file had been committed to the ledger with `<<<<<<< Updated upstream` / `=======` / `>>>>>>> Stashed changes` git merge conflict markers literally embedded inside the JSON. `json.Unmarshal` saw `<` as the first non-whitespace character on line 11 and rejected the file with `invalid character '<' looking for beginning of object key string`. The conflict-marked file had been sitting in the ledger for **4 days** before any reader tripped over it.

**Q2: Why did that happen — how did conflict markers end up committed?**
A `git stash pop` conflicted, and the post-pop file was `git add`'d and committed without anyone checking the file's contents. The full sequence (reconstructed in the [issue timeline](#cascade-1-409json--multi-node-thrash--silent-stash-pop-conflict) above):

1. In-process `SyncPRs` writes a fresh version of `409.json` to the working tree. The PR's `state` is unchanged, so the comment-drop bug (#438) silently truncates `comments` from 1 → 0. The working tree now has a divergent dirty version of `409.json`.
2. The daemon's pull cycle fires and runs `git pull --rebase --autostash --quiet` (`internal/daemon/sync_managed.go:215`).
3. `--autostash` stashes the dirty `409.json`. The rebase succeeds against an upstream version (from another node) that has the full content.
4. autostash pops, conflicts against the upstream version, and writes `<<<<<<< Updated upstream` / `=======` / `>>>>>>> Stashed changes` markers into `409.json` in place.
5. The next code path that touched `data/github/` (e.g., `CommitAndPushGitHubData` or `ox doctor`'s `git add --sparse -A` at `doctor_ledger_git.go:437`) blindly staged the file. Commit. Push.

So the answer to "why did that happen" is the *interaction* of two independent bugs: the comment-drop bug (#438) producing the divergent dirty file, and the silent autostash-pop pipeline committing the conflict. Either bug alone would have been survivable; together they were a guarantee.

**Q3: Why do we auto-merge the conflict from a stash?**
We don't *intentionally* auto-merge a stash conflict — what happens is more passive than that. `git stash pop` writes the conflict markers in place when it can't apply cleanly, and there's no code path between the stash pop and the next `git add` that checks the working tree for those markers. The "auto-merge" is really *"the next commit silently includes whatever is in the working tree, conflict markers and all."*

The only intentional auto-resolution we have is `gitutil.ResolveRebaseAcceptTheirs` at `internal/gitutil/rebase_resolve.go:26-71`. It runs `git checkout --theirs --` on every conflicted file under safe prefixes and then `git rebase --continue`. **It is called only when both of the following hold** (`internal/daemon/sync_managed.go:217-225`):

1. `git pull --rebase --autostash --quiet` exits non-zero, AND
2. `gitutil.IsRebaseInProgress(path)` returns true

Neither condition holds for the autostash-pop case. The rebase has *already succeeded*; only the post-rebase autostash pop failed. And here's the kicker: with `--quiet` and a stash-pop-only failure, `git pull --rebase --autostash --quiet` actually exits with code 0. The pull *itself* succeeded — git's view is "rebase done, here's a stash-pop conflict for you to deal with, the pull was fine." So the daemon's whole error-handling block at `sync_managed.go:217-276` never runs. Even the `UU` check at line 251-252 (which would have caught the file as unmerged in `git status --porcelain`) is gated inside the error path that doesn't fire.

So we don't auto-merge from a stash — we *silently accept* the conflict-marked working tree as truth, because the only code path that *would* have refused or auto-resolved the conflict requires the pull to have failed first.

**Q4: Why didn't we hit the case where a `.rej` file was generated?**
We *do* have a "stash conflicts aside as `.rej` files" pattern in this codebase — but it lives entirely on the **blue-green GC reclone** path and is never invoked from the routine pull path. The pattern is at `internal/daemon/sync_gc.go:654-668`, in `gcRestoreDiff`:

```go
// try clean apply with 3-way merge
if _, err := gitutil.RunGit(ctx, repoPath, "apply", "--3way", diffFile); err == nil {
    s.logger.Info("gc: restored uncommitted changes", "path", repoPath)
    return nil
}

// fall back to --reject (applies what it can, creates .rej for conflicts)
if _, err := gitutil.RunGit(ctx, repoPath, "apply", "--reject", diffFile); err != nil {
    return fmt.Errorf("git apply failed (diff preserved at %s): %w", diffFile, err)
}
s.logger.Warn("gc: restored uncommitted changes with conflicts (.rej files created)", "path", repoPath)
```

This is the right shape. When the GC reclone has captured the old uncommitted diff to `<repo>.gc-diff` (via `gcCaptureDiff` at `sync_gc.go:550`) and then needs to reapply it onto the freshly-cloned repo, it first tries `git apply --3way`; if that fails it falls back to `git apply --reject`, which **applies what it can and writes the unapplyable hunks to `<file>.rej` files** as a guard artifact. The `.rej` files sit next to the original file as the "set this aside for manual review" signal, and the daemon logs a warning so anti-entropy and `ox doctor` can find them. The original file is *not* corrupted with conflict markers — the conflicting content is preserved as a side file.

**Why didn't the autostash-pop on `409.json` go through this path?** Because `gcRestoreDiff` is only called from `runBlueGreenGC` at `sync_gc.go:451`. The routine daemon pull cycle (`pullManagedRepo` in `sync_managed.go`) does **not** call it. The routine pull just runs `git pull --rebase --autostash --quiet` (`sync_managed.go:215`) and trusts whatever the working tree looks like afterward. There is no `--3way` / `--reject` fallback, no `<file>.rej` generation, no marker scan, no warning logged. The two paths handle "apply might collide with what's already there" with completely different rigor:

| Path                                                                 | Conflict-handling on apply                                                                                                                       |
| -------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| Blue-green GC reclone (`runBlueGreenGC` → `gcRestoreDiff`)          | `git apply --3way` → on failure, `git apply --reject` → `.rej` files generated, warning logged, original file preserved clean                    |
| Routine pull (`pullManagedRepo` → `git pull --rebase --autostash --quiet`) | git stash pop writes `<<<<<<<` markers in place, exits 0 (because the rebase succeeded), no fallback, no marker scan, no warning, next `git add` blindly stages |

So the answer to "why didn't we hit the `.rej` case" is: **the right pattern exists in the codebase, but it's not wired into the routine pull path.** The GC reclone code already knows that "apply might fail, set the conflict aside as `.rej`, log it loudly" is the correct shape for safely reconciling uncommitted changes against new upstream content. The routine pull code was written assuming `--autostash` would handle everything cleanly or fail loudly — neither of which holds for a stash-pop-only failure under `--quiet`.

The other things one might *expect* to be guard artifacts are not generated either:
- **`git stash list` entry** — after a failed stash pop, the stash entry **remains** in the stash list (`refs/stash` still references it). This is the breadcrumb git itself leaves. **No code in the daemon checks `git stash list` after a pull cycle**, so this signal is missed.
- **`git status --porcelain` showing `UU`** — only checked inside the pull-error path that doesn't run when `git pull --rebase --autostash --quiet` exits 0.
- **`<<<<<<<` content scan** — exists, but only in `internal/ledger/github_migrate.go:35-73, 133`, called from the post-#439 GitHub data migration. Not part of routine sync passes.
- **`.git/MERGE_MSG` / `.git/REBASE_HEAD`** — git's own state files. None apply to a stash pop.
- **`.orig` files** — only created by `git mergetool`, not by `git stash pop`.

The fix that landed ([PR #459](https://github.com/sageox/ox/pull/459) / `8d49223` and the content-hash filename work in #439) prevents the conflict from occurring *in the first place* by ensuring the dirty file and the upstream file have *different filenames* — so the autostash pop has nothing to conflict against. That's a correct fix for the specific failure mode. **It does not bridge the structural gap**: the GC path has a `--reject` / `.rej` safety net for "apply might collide", and the routine pull path does not. A different write path that produced a divergent dirty file under a deterministic name (e.g., the bespoke `git add` in `memory_put.go`) would still hit the same trap.

---

## Possible discussion topics

1. What are the multi-node write principles? 
	1. Should we continue with hash and UUID7 for multiple node write?
	2. Should we introduce LLM conflict resolution? When?
2. What is `ox doctor` role in resolving conflicts?
3. We're using `claude -p` in more places now. 
	1. Should we be using other methods to do LLM summarization?
	2. What is the error handling principles?

---

## Appendix A: Debugging timeline

This appendix preserves the moment-by-moment record of *how the investigation unfolded* — wrong-then-right hypotheses, the user/agent exchanges that drove breakthroughs, and the order in which findings were made. The findings themselves have been promoted to the [issue timeline](#what-happened-issue-timeline) above; this section is kept as raw material for the team postmortem discussion and as a reference for future debugging.

### A.1 — Symptom B debug session (`409.json` unmarshal failure)

Session `81775235-…` (`/Users/galex/src/sageox/ox`, 2,497 entries):

- **13:15:47** — Investigation regarding unmarshal warning from `pr/409.json`begins.
- **13:16:13** — First (wrong) hypothesis: *"the file contains HTML (like a 404 page or git smudge filter output) instead of JSON."*
- **13:16:27** — Failing code located at `internal/ledger/github.go:340` (the `Unmarshal` call inside backfill).
- **13:18:51** — *"The file is valid locally but corrupt on the remote machine, so this could be a git sync issue."* — first signal that multiple nodes are involved.
- **13:19:31** — Tool reads the actual file content; line 11 surfaces as `<<<<<<< Updated upstream`.
- **13:19:36** — **Realization:** *"There it is. Line 11 contains `<<<<<<< Updated upstream` — this is a **git merge conflict marker**! Hypothesis confirmed: Git merge conflict markers corrupted the JSON."* The earlier HTML hypothesis is dropped.
- **13:19:37** — Grep across the file finds all three markers: `11:<<<<<<< Updated upstream`, `281:=======`, `283:>>>>>>> Stashed changes`. **The `<` was the start of a stash-pop conflict marker, not HTML.**
- **13:19:55** — Marker labels (`Updated upstream` / `Stashed changes`) identify the source: *"specifically from `git stash pop` or `git pull --rebase --autostash`"*.
- **13:20:46** — **Root cause of `409.json` corruption found.** *"The file has git merge conflict markers from an autostash pop."* A dirty version of `409.json` was autostashed by the daemon's pull cycle, the upstream pull brought in a different version, the stash pop produced a conflict, and the conflict-marked file was then **blindly `git add`'d and committed**.
- **13:20:52** — *"Confirmed — no code anywhere checks for autostash pop conflicts after a successful pull. The `--quiet` flag on `sync_managed.go:215` even suppresses the warning git would emit."* The corruption was guaranteed to be silent on the writing node.
- **15:52:57** — Investigation pivots to *what produced the dirty file in the first place* — `SyncPRs` / `SyncIssues` / `BackfillPR` and the daemon's `github_sync.go`.
- **15:55:08** — Per-commit content diff of `409.json` reveals the upstream cause: a comment-drop pattern in `SyncPRs`. A re-sync of the same PR rewrites the cache file from a fresh fetch and silently truncates fields:

  > `Commit 1 (3bea6e5) - Ajit, Apr 1 16:35: comments=1`
  > `Commit 2 (242e1da) - Ajit, Apr 1 16:50: comments=0`

  This is what produces the dirty working-tree file that the autostash pop later mangles.
- **16:24:05** — User articulates the full chain in their own words: *"later when the SyncPRs is called via doSync which is called by the daemon, it autostashes a dirty file which had zero comments, which is then stash popped and causes the conflict content (which is blindly git added)."*
- **16:51:42** — `bd create ox-0js` — *"Content-hash PR/issue filenames to prevent multi-node write conflicts"* (P1) → became **#439**.
- **16:51:46** — `bd create ox-bvb` — *"Audit shared-resource write patterns for multi-node conflict risk"* (P2).
- **16:52:43** — `bd create ox-2kg` — *"GitHub facts use deterministic filenames vulnerable to multi-node conflicts"* (P1) → became **#441**.
- **16:52:49** — Dependency added: `ox-0js` (the fix) blocks on `ox-94r` (the comment-drop bug, → became **#438**).
- **16:56:28 / 16:56:49 / 16:56:55** — **#438**, **#439**, **#441** filed on GitHub in a 27-second burst.

### A.2 — Symptom A debug session (`.session-facts/` uncommitted files)

Session `666b6639-…` (`/Users/galex/src/sageox/ox`, 650 entries) — same day as Symptom B, ~33 minutes after #438/#439/#441 were filed:

- **17:29:44** — User opens with `ox status` output showing modified `.session-facts` files including Ajit's old session marker. *"At some point this morning, I might have run `ox sync` or `ox index github` or `ox daemon start`…"*
- **17:31:55** — Explore subagent reports: *"multiple code paths where files are written to `memory/.session-facts/` and `git add` is executed, but `git commit` can fail silently"*.
- **17:36:39** — **Bug #1 identified.** *"Empty marker files never committed (explains untracked files). `cmd/ox/distill_sessions.go:86-100` — When a session has no extractable facts, the code writes an empty marker file to prevent re-scanning, but never calls `commitMemoryFile()`."*
- **17:39:37** — After user pushback, clarification: the marker is not literally empty — it's a placeholder JSONL written via `os.WriteFile` at `distill_sessions.go:84-101` when `len(extractedFacts) == 0`. It exists so `scanPendingSessions` skips the session next time.
- **17:41:10** — User raises the **added-vs-modified distinction**: *"`memory/.session-facts/2026-02-13/2026-02-13T14-56-ajit-OxmoZK.jsonl` is also 'empty' tracking file — but it looks tracked"*.
- **17:43:06** — *"Good catch — that breaks my clean split of 'untracked = markers, staged = real facts.' If that file is an empty marker AND it's staged (modified), then something must have called `git add` on it. The marker path at line 86-100 never does."*
- **18:07:42** — Comprehensive cross-link: *"daemons on different nodes can overwrite, causing merge conflicts (as seen with 409.json)"*. **The two symptoms are recognized as the same root pattern.**
- **18:08:49** — `extractSessionFacts` discovered to have no `since` parameter — scans every session every run.
- **18:11:23** — `bd create ox-kq8` — *"Fact extraction has no lookback window — scans full history every run"* (P1).
- **18:13:57** — User pushback drives deeper investigation: *"You didn't answer the question — I told you that some of the staged files also have no extracted facts, and some of the unstaged have no extracted facts. That's not the bifurcation you are making."*
- **18:16:52** — `bd create ox-cs2` — *"Session facts use deterministic filenames vulnerable to overwrite and multi-node conflicts"* (P2). The same pattern from #441 is now identified in `.session-facts/`.
- **18:24:08** — Regeneration timeline reconstructed: *"A mass 'finalize session' operation ran on Apr 3 (14:41–15:37 PDT), regenerating `summary.json` for 58 sessions. … `f2eb49d` at 14:01 PDT — `ox doctor: recover sessions` — restored 302 files from LFS stubs."*
- **18:31:21** — User: *"session we were originally looking at — what content changed? I thought there were no events?"*
- **18:31:26** — User pastes the diff. The summary field literally contains:

  > `"summary": "I need permission to run commands and write files. The summary JSON is ready — here's what I found in the session..."`

- **18:31:41** — *"raw LLM reasoning/planning output that leaked into the summary field … `quality_score: 1` with `score_reason: 'unparsable LLM output, defaulting to upload'`."*
- **18:35:11** — *"For the Feb 13 session, the LLM returned agent-style planning text … instead of JSON. The parser failed, and the fallback dumped raw LLM output into the Summary field."*
- **18:39:07** — Full cascade reconstructed: *"What happened on the remote machine: (1) `ox doctor` recovered sessions from LFS stubs → wrote `.needs-summary` markers; (2) the daemon's anti-entropy detected those markers → enqueued summarization; (3) `claude -p` returned planning text; (4) parser fallback dumped raw text into Summary."*
- **19:25:13** — The *who triggered it* question is left open within the session — *"or something happened on the remote machine (manual command, different code version, etc.) that I can't see from here"*. Resolved later: Ryan ran the cleanup command manually.

---

## Sources

Two Claude Code debug sessions captured the investigation. They have not yet been re-extracted into sageox sessions:

- `~/.claude/projects/-Users-galex-src-sageox-ox/81775235-0aca-4664-82eb-1e68ca4068bd.jsonl` — primary debug session for Symptom B (2026-04-06 13:12 PDT → 2026-04-08 21:15 PDT, 2,497 entries)
- `~/.claude/projects/-Users-galex-src-sageox-ox/666b6639-2497-470e-8e51-43fc63ed24fc.jsonl` — `.session-facts` investigation for Symptom A (2026-04-06 17:29 PDT → 2026-04-08 10:21 PDT, 650 entries)

Git evidence: `git log -- 'data/github/2026/04/01/pr/409.json'` in the ledger repo at `~/.local/share/sageox/sageox.ai/ledgers/repo_019c5812-01e9-7b7d-b5b1-321c471c9777`.
