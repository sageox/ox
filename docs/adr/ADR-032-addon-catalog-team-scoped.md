# ADR-032 — The Add-on Catalog is team-scoped: one selection per team, overwritten on update

**Status:** Accepted — approved by Ryan Snodgrass on 2026-09-21 as path-location and data-ergonomics owner. Four rulings were made explicitly: **(D1)** catalogs are Team Context scoped, never per-repository; **(D2)** add-on-supplied context ships inside a skill, not as a free-standing artifact type; **(D3)** add-on artifacts co-mingle with hand-authored ones and carry provenance in a lock; **(D4)** an add-on update overwrites team edits, with Team Context git history as the undo.

**Date:** 2026-09-21 · **Supersedes:** nothing · **Amends:** ADR-031 (reserved namespaces and gitignored materialization), `docs/specs/skill-management.md` (which is written project-scoped throughout)

---

## Context

`ox` learned to ship optional skills, and PR #1013 gave that a public surface: `ox skills catalog | install | uninstall`, selecting catalog content **into one repository** and recording the choice in that repository's `.sageox/skills.lock.json`.

Three reviewers — a usability expert, a developer-tools expert, and a principal engineer — independently reached the same conclusion: the architecture is sound but the ownership model is backwards. A team's playbooks are a property of the team, not of whichever repository someone happened to be standing in. Repository-scoped selection means the same skill is chosen N times, drifts N ways, and is retired N times — or, far more likely, never retired at all.

The surface was **unreleased** (0.16.0 ships none of it), so the cost of withdrawing it was zero and the cost of shipping it was a deprecation cycle over a model we already knew was wrong.

Separately, the catalog conflated two populations that have opposite lifecycles: the mandatory `ox-cli-*` runtime relays and the committed `sageox` on-ramp, which must follow the installed binary automatically, and optional team knowledge, which must stay pinned until a human changes it. One `Bundle.Default` boolean was carrying that whole distinction.

## Decision

**An add-on is the unit of distribution. A catalog is a catalog of add-ons. `ox addons` changes what the team owns; `ox sync` distributes everything the team owns. The two never overlap.**

### D1 — Catalog operations write Team Context only

`ox addons install | update | remove` write into the Team Context checkout and nothing else. A product repository carries **no** add-on selection and can never make one. `ox sync` is origin-agnostic: it transports and converges add-on-installed and hand-authored Team Context content through one pipeline, and it **never** checks the catalog for newer versions.

There is no `ox addons sync` and no `ox skills sync`, now or later. One synchronization concept, one command, one promise:

> `ox addons` chooses and updates what the team owns.
> `ox sync` distributes everything the team owns.

Consequently `ox skills catalog`, `ox skills install`, and `ox skills uninstall` are withdrawn before release. `ox skills list | status | approve | revoke | publish` remain — they are diagnostics, capability approval, and hand-authored publishing, none of which select catalog content.

### D2 — Add-on-supplied context lives inside a skill

An add-on carries **skills** (with their own `references/`, `assets/`, `scripts/`) and **rules**. There is no free-standing "context" artifact type and no `docs/add-ons/` directory.

Three reasons, in order of weight:

1. **Context with no skill has no activation trigger.** An agent never learns *when* to read a loose document. Context bundled into a skill is discovered exactly when the skill is, which is the entire premise of the Agent Skills progressive-disclosure layout.
2. **The layout already exists.** `extensions/skills/<name>/references/` is validated and materialized today; `post-cutoff` ships that way.
3. **The proposed path did not work.** `teamdocs.DiscoverDocs` skips directories (`internal/teamdocs/discover.go:43`), so anything under `docs/add-ons/` would have been invisible to the team-docs catalog, to `ox agent prime`, and to the convergence coordinator — a silent nothing, not an error.

`agents/tools/` is deferred for the same class of reason: there is no tool artifact kind and no handler behind one, so the path would be a promise with no mechanism behind it. (A `KindTool` enum value existed when this was written, but it had no producer and no handler; the PR that ratified this ADR deleted it rather than ship an unreachable promise.)

### D3 — Add-on artifacts co-mingle; the lock carries provenance

Add-on-installed skills land in `agents/skills/<name>/` and rules in `agents/rules/<name>.md` — the same roots hand-authored team content uses. There is no `agents/add-ons/<addon>/` namespace.

Ownership is recorded in a committed lock, not inferred from the path:

| Path | Role |
|---|---|
| `<team-context>/.sageox/add-ons.lock.json` | committed; per add-on: `source`, `addon`, `version`, `digest`, `installed_at`, owned `paths[]`, per-file digests |
| `<team-context>/agents/skills/<name>/` | canonical Team Skills, add-on-installed or hand-authored |
| `<team-context>/agents/rules/<name>.md` | canonical Team Rules, add-on-installed or hand-authored |

This buys three things a namespaced root would not:

- **Almost no consumer changes.** `internal/teamconverge` carries an `Origin` record with `Addon`, `AddonVersion`, and `Digest` fields on every artifact and outcome, so the reporting path is already add-on-shaped. The `OriginAddon` constant and the `ResolveOrigin` discovery seam were written ahead of add-ons and deleted in this PR, because nothing could populate them and an unreachable seam is not a design benefit. Add-ons reintroduce both in the PR that needs them — one constant and one resolver function — after which discovery, prime, `ox sync` rendering, and `ox skills status` need nothing further. The substance of this bullet is unchanged: no new namespace and no new discoverer.
- **No fourth reserved namespace.** Add-on skills are Team Skills, so they project into repositories under the existing `sageox-team-` prefix (ADR-031 §1) and are covered by the existing `skills/sageox-team-*/` ignore glob. The per-name exact-ignore mechanism that unprefixed catalog names required (`unprefixedCatalogSkills`) was retired rather than extended, and is **deleted as of this PR** — it mutated a **tracked** `.claude/.gitignore` on every selection change and self-healed only on a `repairTrackedSetup` apply.
- **One collision rule.** An add-on file colliding by name with an existing **hand-authored** file is a namespace collision, not an edit: install refuses it by name, mutating nothing. Only paths the lock already owns are ever overwritten.

### D4 — Updates overwrite; Team Context git history is the undo

**Add-on-installed files are not yours to edit.** `ox addons update` overwrites every owned path unconditionally and removes owned paths the new version drops. There is no three-way merge, no conflict state, no `ox addons diff`, no `--keep-team`, no `--accept-upstream`. Someone who wants to customize copies the file to a new, hand-managed name.

This inverts the edit-preserving design the tickets originally specified, deliberately. The recovery story is better and far simpler: Team Context is a git repository, so an overwritten edit is one `git log -p` away, in a history teammates already share. A merge algorithm would have bought a worse version of what git already provides, plus a conflict state a human has to learn.

It also makes the two layers consistent rather than contradictory. ADR-031 §4 overwrites inside reserved namespaces in a product repository *because those files are gitignored*, so a preserved edit becomes permanent invisible drift. Team Context files are committed, so an edit there is visible — but the remedy is the same, because in both layers git, not ox, is the thing that remembers.

The per-file digests in the lock therefore exist for **honesty, not resolution**: they let `ox addons` say *"you modified this; the next update will overwrite it"* before the update runs.

### D5 — Two path consequences that fail silently if missed

Both were found by reading the code, and neither produces an error:

- **The Team Context `.sageox/.gitignore` is deny-all.** `internal/gitserver/gitignore.go:26-29` writes `*`, `!.gitignore`, `!sync.manifest` and nothing else. `add-ons.lock.json` must be added to that allow-list, or the team's add-on selection is never committed and never reaches a teammate — with no message anywhere.
- **An untracked file in a Team Context checkout wedges GC permanently.** `isCheckoutClean()` treats any porcelain output as dirty and blocks blue-green reclone forever. Everything add-ons write must be tracked or ignored, never merely present.

Sparse checkout needs no change: `.sageox/`, `agents/`, and `docs/` are already in the served `sync.manifest`, and `internal/manifest.EnsureRequiredIncludes` floors `agents/` client-side (GH #862).

### D6 — Platform assets and add-ons are separate populations

`extensions/skills/` keeps the `ox-cli-*` relays, the lifecycle commands, and the committed `sageox` on-ramp: ox runtime assets that follow the installed binary automatically and are never browsed, versioned, or pinned by a team. `extensions/add-ons/` is the catalog: optional, team-selected, pinned to an exact version and digest until someone runs `ox addons update`.

## Alternatives considered

- **Repository-scoped selection** (what PR #1013 implemented). Rejected: selection duplicated per repository drifts per repository and is retired nowhere.
- **A separate `ox addons sync` / `ox skills sync`.** Rejected: two sync verbs force a user to know which one distributes add-on content versus hand-authored content, a distinction the product promises they never need.
- **Mutable catalog refs** (`@latest`, a branch name). Rejected: an update that cannot be reproduced cannot be audited or rolled back.
- **Partial application on conflict.** Rejected: a half-applied add-on version is a state no one can name. An update either lands whole or changes nothing.
- **Edit-preserving three-way merge** (the original ticket specification). Rejected by D4 in favor of overwrite plus git history.
- **`agents/add-ons/<addon>/` namespacing.** Rejected by D3: on-disk provenance is not worth teaching a second root to every discoverer, and it does not solve same-name collisions between two add-ons anyway.
- **A fourth reserved prefix for add-on content.** Unnecessary once add-on skills are Team Skills; `sageox-team-` already covers the projection.

## Consequences

**Good.** One selection per team instead of one per repository. Retirement becomes possible, because there is exactly one place to retire from. The convergence pipeline needs one function, not a new subsystem. No new reserved namespace, and the tracked-`.gitignore` churn of unprefixed catalog names goes away with the surface that required it.

**Costs.** `ox skills catalog | install | uninstall` are withdrawn before anyone could use them, so the only way to select catalog content is `ox addons`, which does not exist yet — there is a window in which `post-cutoff` ships in the binary and nothing can select it. A team that hand-edits an add-on file loses that edit on the next update and must recover it from Team Context history.

**The mechanism ships behind a flag.** Every `ox addons` entry point — the command, its
sync leg, and any reconcile path that reads an add-on file — must consult
`flags.Get().AddonsEnabled` and no-op when it reports false. It already gates the `ox
sync` help text, which is why that help must not advertise a catalog this build cannot
reach.

The flag **defaults off** (`flags.Defaults()`), is overridable by `FEATURE_ADDONS` for
local work, and can be turned on centrally through remote settings — a rollout lever
rather than a developer-only escape hatch. That asymmetry is deliberate: mature
capabilities in that struct default *on*, and add-ons does not, because a selection model
is the hardest feature to take back. Once a repository or a team records a choice,
withdrawing the feature orphans the file that recorded it — exactly the situation
`ox skills catalog | install | uninstall` created and this ADR is cleaning up. Landing
the successor dark means the next partial merge cannot repeat it.

Delete the flag once `ox addons` is complete and rolled out; a permanent flag is a
permanent branch of untested behaviour.

**Accepted risks.** Overwrite-on-update is only as forgiving as Team Context history is reachable; if a team never pulls, the pre-update content still exists but nobody is looking at it. `ox addons` must therefore say plainly, before it overwrites, which owned files were modified.

## Third-party authorship (forward-looking; not a new ruling)

**An add-on's author is not necessarily SageOx, and not necessarily the team
installing it.** Three populations are anticipated: add-ons SageOx publishes,
add-ons a team authors for itself, and — in future — **add-ons published by third
parties**. The catalog is a catalog *of add-ons*; it does not assume who wrote
them.

That is the substantive reason the unit is called an **add-on** rather than a
pack or a bundle. "Add-on" is the register developers already hold for
*optional capability supplied by someone else* — browser add-ons are the
canonical case. A name that implied first-party authorship would have to be
renamed the day the third population arrives.

**Nothing here loosens the trust boundary, and third-party content must not get
its own path.** Add-on-supplied skills are Team Skills (D2), so they arrive over
the same pull path as any other Team Context content and land in the same roots
(D3). Consequences that follow, and that the add-on command must honor rather
than special-case:

- **Third-party bytes are untrusted input.** They route through the existing
  digest-pinned approval gate — `ox skills approve`, with runnable scripts a
  second, separate decision behind `--allow-scripts`. An add-on install may not
  imply either grant. Nothing on the pull path verifies signatures today, which
  is precisely why that gate exists.
- **Overwrite-on-update (D4) interacts with approval, correctly.** An update
  replaces owned bytes wholesale, so a previously approved add-on skill whose
  content changed returns to *needing approval* — the pin names bytes, not names.
  That is the desired behavior for third-party content, and the add-on command
  must surface it rather than silently re-approving.
- **Provenance is already recorded.** D3's lock carries `addon`, `version`, and
  `digest` per owned path, so "who supplied this file, at which version" is
  answerable without a new namespace. That record is what makes attribution,
  retirement, and — if an add-on is withdrawn — revocation possible.
- **Trust is a team-level decision, which is why the catalog is team-scoped
  (D1).** Adopting a third party's add-on is a judgment about the team's
  direction, made once, visible in Team Context git history and reviewable in a
  pull request. A per-repository selection would have scattered that decision
  across repositories where nobody reviews it.

Naming the populations now costs nothing and stops the first third-party add-on
from arriving as a special case.

## Related decisions

- **ADR-031** — amended. Its reserved-namespace model, gitignored materialization, and split lock/state are the foundation this builds on; D3 and D4 extend its ownership reasoning to a second layer rather than replacing it.
- **ADR-030** (per-clone git serialization) — aligns. The Team Context add-on transaction runs under the same `gitutil.WithRepoLock` discipline.
- **ADR-023** (two-layer skill injection) — aligns, unchanged.
- ox surfaced **ADR-014** (adapter shared packages), **ADR-027** (consultation-attribution privacy), **ADR-028** (knowledge bubbles), *ephemeral mode*, and *whisper/murmur* as candidates. None apply: they share vocabulary ("package", "context", "team") but no decision surface with this one. Recorded here so a reviewer can tell considered-and-rejected from missed.

## References

- `docs/specs/skill-management.md` — written project-scoped throughout; amended by D1, rewrite tracked as `ox-hvnc.9`
- `internal/teamconverge/types.go` — `Origin` (its `Addon`/`AddonVersion`/`Digest` fields survive; `OriginPack` and `KindTool` were deleted as unreachable in the PR that ratified this ADR)
- `internal/gitserver/gitignore.go` — the Team Context ignore allow-list of D5
- `internal/teamdocs/discover.go` — the directory-skipping reader of D2
- Beads: epic `ox-hvnc` (Add-on Catalog), epic `ox-tzzg` (unified convergence), `ox-xfku` (end-to-end lifecycle proof)
- PR #1013 — the withdrawn repository-scoped surface
