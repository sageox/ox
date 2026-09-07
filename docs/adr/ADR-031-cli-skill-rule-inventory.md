# ADR-031 — The ox CLI skill & rule inventory: reserved namespaces, gitignored materialization

**Status:** Draft (Proposed) — awaiting Ryan's review as path-location and data-ergonomics owner. Two decisions in this ADR trip the `CLAUDE.md` Required-Review gate ("Path locations", "Data access ergonomics"), and one of them has ox writing a commit into a customer's repository. Implementation has landed behind these decisions so the review has running code to judge; it flips to Accepted on approval.

**Date:** 2026-09-07 · **Supersedes:** nothing · **Amends:** ADR-023 (two-layer skill injection), `docs/specs/skill-management.md`

---

## Context

ox installed its own skills, rules, and commands **into every customer repository** and force-staged them. `gitAddFilesForce` says so in its own comment — *"force-stage files that may be gitignored (hooks, commands, skills, rules)"* — so the installer deliberately defeated `.gitignore`.

Measured in ox's own repository at 0.14.3: **34 tracked ox-owned files**, **14 commits in 90 days** touching them, and **5 orphaned command files** the binary no longer ships that no release ever removed.

Three consequences:

- **Every content-bearing release churns vendor files through a customer's pull requests.** Reviewers read diffs of files nobody on their team wrote.
- **Retirement never happens.** Removing a file means asking a customer to accept a deletion commit, so in practice nobody ever does. The five orphans are the evidence.
- **There is no fan-out.** No machine-wide registry of ox'd repositories exists, so an update reaches a repo only when someone runs ox there.

## Decision

**ox owns a desired-state inventory of the skills and rules it ships. Everything it owns carries a reserved namespace, is gitignored, and is overwritten unconditionally inside that namespace.**

### 1. Reserved namespaces

| Namespace | Owner | In git |
|---|---|---|
| `ox-cli-*` (and the exact name `ox-cli`) | the ox binary | ignored |
| `sageox-team-*` | a team context (reserved now, unimplemented) | ignored |
| `sageox` | the committed on-ramp skill | **committed** |
| anything else | the user | never touched |

The CLI prefix carries `-cli-` rather than a bare `ox-` because people author their own `ox-`-named skills; a bare prefix would claim them. The exact name `ox-cli` is reserved alongside the prefix because the primary rule file is `ox-cli.md`, which `ox-cli-*` does not match — a miss that is silent and would leave that one file in every pull request.

### 2. Materialization is gitignored real files, not symlinks

A central store under XDG, symlinked into each repository, was considered at length and rejected. Symlinked skill entries are documented and working in Claude Code and Codex, so this is a cost decision, not a capability one: it would require inverting six symlink refusals in the only crash-safe installer we have, it **silently no-ops on Windows** (users would get no skills and no error), it dangles in any container with a different `$HOME`, and an editor saving through the link would silently change every repository on the machine while the "user modified it, preserve it" protection saw an intact link.

The benefit it uniquely buys — one write updating every repo — is worth little, because staleness is only observable when an agent *reads* a skill, which is session start, which already runs ox.

### 3. Reconciliation is co-located with the read

- **`ox agent prime`** compares the recorded catalog revision and ox version against the running binary. Match is the common case and costs one lockfile read; a plan is built only on mismatch. Non-blocking: it never waits on the reconcile lock.
- **`ox doctor`** does the full digest-level repair, and owns the git index.
- **the daemon's 30-minute tick** repairs local drift, never installs into an unselected repo, never runs during a recording session, and **never writes a tracked file**.

### 4. Reserved namespaces are overwritten, but unrecognized content is never destroyed

Inside a reserved namespace ox rewrites anything that does not match desired state — no conflict state, no preserve-on-edit.

**This reverses the installer's previous conservative rule, deliberately.** Preserve-on-edit was right while these files were *tracked*: an edit appeared in `git diff`, so it was visible, reviewable, and plausibly deliberate. Once the files are gitignored, a preserved edit becomes **permanent silent drift** — one machine quietly running a different playbook, invisible to git, unrepairable by ox, undiagnosable by a teammate.

**But overwrite is not delete.** A name ox does not ship cannot be "overwritten", only destroyed, and a user directory that happens to match the glob (`ox-cli-mine` is a plausible name for a skill *about* the ox CLI) would be unrecoverable: not in git, not in the lockfile, not in the journal. So ox removes only what it can prove it wrote — a verifying stamp or a recorded digest. Unrecognized content inside a reserved namespace is reported, never swept.

### 5. Ignore rules live in scoped, ox-owned files

`.claude/.gitignore`, `.agents/.gitignore`, `.factory/.gitignore` — committed, and written only into directories that already exist.

A `.gitignore` under `.sageox/` **cannot** reach `.claude/`; ignore files govern only their own subtree. GH #732 ruled ox must not write the developer's root `.gitignore` ("don't pollute developers repos"), and its remedy was "put it in a file ox owns". A short file inside a directory ox already populates is the nearest thing to that remedy which can actually reach these paths.

They are written as a **marked block** (`# >>> ox-managed … >>>`), not as line appends. `sageoxignore.HasEntry` skips comment lines by design, so an append-based writer would re-add its own header on every prime, doctor, and daemon tick — into a committed file, reintroducing exactly the churn this ADR removes.

`.git/info/exclude` was rejected: it is machine-local and never committed, so a teammate's fresh clone has no rule until ox runs there — precisely the window in which vendor files get committed by accident.

### 6. The manifest is split in two

- **Committed** (`.sageox/skills.lock.json`, schema 2): the project's selection — which bundles, which agent targets. Team intent; changes only when a human changes it.
- **Machine-local** (`.sageox/cache/skills-state.json`, gitignored): the catalog revision, the ox version, and the per-file digests this machine materialized.

Without the split the manifest alone would recreate the churn: `source.version` and every digest changed on each release, so a tracked file diffed even though every file it described was invisible to git. Schema-1 manifests migrate on read.

### 7. The command surface folds into skills

The 20 Claude-only `.claude/commands/ox*.md` files become skills carrying `disable-model-invocation: true`. Verified against the Claude Code documentation: a skill directory *is* a slash command, a skill takes precedence over a same-named command (so the transition has no broken window), and the frontmatter key keeps the description **out of context** while leaving the surface user-invocable.

This closes the cross-agent gap ADR-023 recorded — not because Claude reads `.agents/skills` (it does not, and we must not assume it), but because ox already writes a separate `.agents/skills` projection, which a command file could never reach. It also collapses two ownership mechanisms (lockfile-owned skills, stamp-owned commands) into one, which is what makes the orphan class impossible to recur.

**Cost:** `/ox-plan` becomes `/ox-cli-plan`. The slash name derives from the directory, so keeping the short form while prefixing the path is not possible.

### 8. The one commit ox writes — **this is the decision that needs an explicit ruling**

Ignoring a file does nothing while git still tracks it, and there is no way to untrack without a commit. So `ox doctor --fix` writes exactly one, and every guard below exists to make it safe:

- Runs at `FixLevelAuto`. A confirm-gated migration is one that never runs — the five orphans are the proof. `FixLevelAuto` here means **human-initiated and foreground**, not unattended; the daemon never touches the index.
- **Refuses if anything at all is staged.** A bare `git commit` records the entire index, and this codebase has already shipped that bug once — a chore commit that recorded 10,373 unrelated deletions.
- **Refuses during any in-flight operation**, detected against the *resolved* git dir, because `.git` is a file in a linked worktree and a naive stat reports "nothing in flight" mid-merge. Conductor runs every workspace as a worktree.
- **Refuses on detached HEAD** (the commit would be orphaned by the next checkout) and **on an unborn branch** (it would become the root commit carrying the user's initial import).
- **Refuses while a session is recording.** `ox doctor` is routinely run *by* an agent mid-session.
- **Rolls back on failure** — a rejecting pre-commit hook, an unavailable GPG agent, an unset `user.email` — leaving the repository exactly as it was rather than stranding staged deletions the guard would then refuse to clear.
- Carries the ignore files in the same commit, and is revertable.

## Consequences

**Good:** an ox upgrade changes zero tracked files. Retirement becomes possible for the first time. Two ownership mechanisms collapse into one. Codex, Gemini, and OMP gain the lifecycle surface. The `sageox-team-*` namespace is reserved before anything can occupy it, so team sync will never need to touch a customer's ignore file.

**Costs:** every `/ox-*` slash name changes, once. Customization becomes forking to a non-reserved name. One migration commit lands in each customer repository.

**Accepted risks:** Windows has no cross-process lock for the apply path (documented gap, mirroring the existing daemon posture). Whether Codex/Gemini/OMP honor `disable-model-invocation` is **unverified** — they share the `.agents/skills` projection, so if they ignore it, 13 lifecycle skills become model-invocable there; tracked as its own task and gated before that projection is trusted.

## See also

`docs/specs/skill-management.md` · `docs/specs/skill-activation-design.md` · ADR-023 · ADR-030 (per-clone git serialization, which phase 4's team source must run under) · GH #881 · GH #732 · GH #862
