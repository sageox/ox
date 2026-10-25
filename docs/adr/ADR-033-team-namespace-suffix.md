# ADR-033 — Team Context content is namespaced by a `-team` SUFFIX, and ownership moves from the name to a stamp

**Status:** Accepted — 2026-09-23.

**Amends:** ADR-031 §1 (the reserved-namespace table) and §4 (reclaim inside a reserved namespace) · ADR-032 §"where add-on content lands" · `docs/specs/skill-management.md`

---

## Context

ADR-031 reserved `sageox-team-*` for content projected out of a Team Context, and
it did the job it was reserved for: one stable gitignore glob per agent root, and
an ownership claim ox could make on sight.

It also renamed every team skill out from under its own documentation, and that
cost was not priced in.

**An agent derives a skill's slash name from its DIRECTORY, not from the `name:`
in its frontmatter.** ADR-031 §7 recorded this for ox's own skills — *"`/ox-plan`
becomes `/ox-cli-plan`. The slash name derives from the directory, so keeping the
short form while prefixing the path is not possible"* — and accepted it, because
ox authors those skills and can rename them to match.

Ox does **not** author team skills. It copies the team's bytes verbatim, on
purpose (`teamsource.go`: *"the only way to remove it is to rewrite the team's
file, which ox does not do"*). So a team skill named `fork-scout` installed as
`sageox-team-fork-scout`, while the description ox had just copied — the text the
agent reads, unchanged — still told the reader to type `/fork-scout`. The
observed state in this repository at the time of writing:

| | |
|---|---|
| Directory on disk | `.claude/skills/sageox-team-fork-scout/` |
| Its own frontmatter | `name: fork-scout` |
| What the agent lists and resolves | `sageox-team-fork-scout` |
| What the skill's own description says to type | `/fork-scout` |

Ox's own surfaces had already concluded the prefix was noise and were **stripping
it for display** in both `ox skills list` and `ox skills status` — printing
`fork-scout` beside a namespace where only `/sageox-team-fork-scout` resolved.
The diagnostic was honest about the name and wrong about the invocation.

Cross-references between team skills are wrong for the same reason, and no
mechanism could have fixed them: ox cannot rewrite a description without rewriting
the team's file.

## Decision

**Everything ox projects out of a Team Context — skills and rules alike — is
named `<name>-team`. Ownership is proved by an in-band stamp and by git, never by
the name.**

### 1. The namespace table, amended

| Namespace | Owner | In git |
|---|---|---|
| `ox-cli-*` (and the exact name `ox-cli`) | the ox binary | ignored |
| `*-team` | a team context | ignored |
| `sageox-team-*` | **legacy only** — swept, never written | ignored for one release |
| `sageox` | the committed on-ramp skill | **committed** |
| anything else | the user | never touched |

A suffix keeps everything the prefix bought at the file level. `skills/*-team/`
and `rules/*-team.md` are stable globs, so the committed `.gitignore` still never
churns when a team adds or drops a skill — which was the actual requirement, and
the reason a per-name ignore list was rejected outright.

**Rules take the suffix too**, becoming `<slug>-<hash>-team.<ext>`, even though
rules have no slash surface and lose nothing to a prefix. One rule — *everything
from Team Context ends in `-team`* — is cheaper to explain than a correct
per-artifact exception, and explaining it is the recurring cost.

### 2. The name no longer authorizes destruction

ADR-031 §4 let ox reclaim anything inside a reserved namespace on sight. That was
sound for `sageox-team-*`: ox declared that string and told people to stay out of
it, so a directory wearing it was ox's by contract.

**`-team` cannot carry that argument.** It is ordinary English. `notify-team`,
`onboard-team`, and `deploy-team` are names a person may reasonably have chosen,
and those bytes are gitignored and absent from the lockfile — so an overwrite is
unrecoverable, and a sweep is a deletion with no undo anywhere.

So `IsReclaimableName` and `IsReservedName`, which ADR-031 kept as separate
predicates over identical namespace sets precisely so they could diverge later,
now genuinely diverge:

- **`IsReservedName`** — classify, gitignore, report — matches `*-team`.
- **`IsReclaimableName`** — authorize destructive overwrite — does **not**.

### 3. The stamp comes back, for skills

ADR-031 §6 made `.sageox/cache/skills-state.json` machine-local and disposable.
That was right, and it means the inventory cannot be the only ownership record:
after a cache wipe, a gitignored projection has no proof of authorship left at
all. The prefix used to be that proof.

Team **Rules** already carried an in-band stamp for exactly this reason. Team
**Skills** now carry the same thing on their `SKILL.md`:

```
<!-- ox-team-skill-sha256:<64hex>; managed by ox from Team Context; edit the source skill, not this projection. -->
```

It covers every byte above it, so a local edit invalidates the claim rather than
inheriting it. Only the manifest is stamped — references and assets may be any
format, including bytes a trailing HTML comment would corrupt — and the manifest
is what claims the directory.

This reverses `docs/specs/skill-management.md`'s *"New projections contain clean
canonical content with no ownership stamp"* for team skills. That sentence was
written when the prefix carried the proof; it cannot survive the prefix.

### 4. A skill git tracks is neither written nor deleted

Ox asks git, once per plan, which skill directories are tracked, and refuses to
touch one it does not own by contract. A tracked path was committed by a human
deliberately; overwriting it produces an uncommitted diff against somebody's
committed work with nothing scheduled to revisit it — the failure Team Rules
already guarded against and skills did not.

**The guard covers every removal path, not just the write path.** Being recorded
in the lockfile proves ox WROTE a file; it says nothing about whether somebody has
since committed it. A team skill that was installed, committed, and then retired
upstream — or filtered out by a `repos:` change — is exactly that shape, and
retiring it would stage a deletion in somebody's index. So `retireManagedFiles`,
the legacy sweep, and `orphanedTeamFiles` all consult the same tracked set.

The question is put to git itself (`rev-parse --is-inside-work-tree`, then one
`git ls-files` for every skill root), not to a stat for `.git` at the project
root: a project nested below the work-tree root has no `.git` of its own, and a
stat-based check would report "not a repository", hand back an empty set, and
fail open precisely where the guard matters.

Two exemptions, both deliberate: a reclaimable name (`ox-cli-*`, legacy
`sageox-team-*`) is still tracked in any repository that has not yet run the
ADR-031 untrack migration, and the committed on-ramp is tracked by design.

### 5. The migration sweeps itself

`sageox-team-*` stays reclaimable-by-name, so every directory from the old scheme
is absent from desired state on the first reconcile and is swept — no migration
command, no user action. Both namespace shapes stay in the ignore block for one
release, because a repository reconciling across the change holds files under both
for a few seconds, and that is exactly the window in which somebody runs
`git add -A`.

## Consequences

**Good.** A team skill is invoked by the name its author gave it, so its own
description is finally correct. `ox skills list` and `ox skills status` can print
the real directory name instead of a prettier one that does not resolve. Ownership
became evidence rather than assertion, which closes a class of silent overwrite
that the prefix was hiding rather than preventing. One namespace rule now covers
every artifact ox projects.

**Costs.** Every `sageox-team-*` path changes, once. Team skill manifests are no
longer byte-identical to the team's source — they carry the stamp. Each plan makes
one `git ls-files` call.

**Accepted risk.** A hand-authored skill named `*-team` is now gitignored, and
silently: it works locally and simply never reaches a teammate. No glob can
distinguish it from a projection, so `ox doctor`'s `team-namespace-collisions`
check reports it with both remedies (rename, or `git add -f`). Ox will not rename
a human's directory or stage into their index to fix this.

## See also

`docs/specs/skill-management.md` · `docs/specs/skill-activation-design.md` ·
ADR-031 · ADR-032 · ADR-023
