# ADR-028: Knowledge Bubbles Are Curated Syntheses, Not Workspaces

- **Status:** Accepted
- **Date:** 2026-07-28
- **Deciders:** Galex Yen
- **Supersedes:** [ADR-017: KB as the Unifying Workspace Primitive](ADR-017-kb-as-workspace-primitive.md) — in full
- **Relates to (sageox-mono):** ADR-073 (KB Scope & Ownership — Accepted 2026-06-26), ADR-086 (Private Per-User Teams), ADR-097 (Curator Synthesis for Knowledge Bubbles), ADR-057 (Conversation Ontology), ADR-066 (Conversational Distillation Engine)
- **Beads:** epic `ox-ay95` (this ADR); implementation epics `ox-nsf7`, `ox-6hvs`, `ox-gmkd`, `ox-cag9` (label `kb-v2-cli`)

## Context

ox's Knowledge Bubble support was built between April and June 2026 against the
platform worldview of that era: sageox-mono ADR-028/030/035 declared that every
team context and every ledger would migrate into a KB, and ox ADR-017 extended
that locally — "what we called 'the ledger' was always a KB" — making the KB the
workspace primitive that session recording, murmurs, and prime resolve against.

The platform reversed that premise, and ox was never updated:

- **ADR-073** (Accepted 2026-06-26) settled that **team context is a permanent
  conversation store** — it stores the conversations a team has (recordings,
  discussions, coding sessions), not their distilled knowledge. The
  team-context→KB migration (ADR-030/035) is dead. KBs gained polymorphic scope:
  every bubble is owned by exactly one user or one team, immutable after
  creation, with access via materialized `kb_members` rows.
- **ADR-086** gives every user a private per-user team, so "personal" knowledge
  also lives under team machinery; personal bubbles re-scope onto that team.
- **ADR-097** defines what KBs are *for*: each bubble is owned by a **Curator**
  AI teammate that folds each newly routed **distillation** (the structured summary a
  finalized conversation produces, per ADR-057/066) into the bubble's
  current-state knowledge. Humans steer the Curator and read its synopses; they
  never hand-edit synthesized knowledge. Consumers read bubbles two ways —
  **query** (`ox kb query`, one verb, server-owned intent; not yet implemented
  server-side) and **mount** (the bubble as a navigable filesystem).

The definitional sentence this ADR adopts:

> A Knowledge Bubble is a team-scoped, Curator-maintained, provenance-carrying
> **synthesis** of the team's distilled conversations for one area of work
> (#engineering, #marketing, …), consumed read-only via query and mount. It is
> not a workspace, not a conversation store, and not a migration target for
> team context or ledgers.

Everything in ox that contradicts that sentence is wrong and comes out. The
full inventory of what was built and where it diverges was researched on
2026-07-27 (kb-v2-cli research report + design interview); the decision log
below records the rulings from that interview.

## Decision

### 1. Team context and ledgers are restored as first-class, permanent stores

They are conversation stores — the inputs to distillation — not "legacy KBs
awaiting migration." Concretely:

- The three-source merger (`internal/kb/merge.go`), which synthesizes team
  contexts as `team`-type bubbles and ledgers as `repo`-type bubbles, is
  **deleted**. Only rows returned by the KB API are ever presented as bubbles.
- `ox teams` is **un-deprecated**; `ox status` regains distinct team-context and
  ledger presentation; all "bubbles are the successor to team contexts /
  ledgers" framing is removed from code, docs, and AI-teammate-facing hints.

### 2. The workspace primitive stays the project/ledger binding

ADR-017's "current KB" concept — the `.sageox/config.yaml` `kb_id` binding,
`kb.ResolveCurrentKB`, prime's `current_kb` — is **removed**. "Which KB am I
in?" stops being a question: a KB is something you consult, not something you
work inside.

- **Session recording never targets KBs.** Recording precedence reverts to
  `env > project config > team config`. The KB-config branch of
  `ResolveSessionRecording` (and the vendored `internal/api/kbconfig` schema
  that exists to serve it) is deleted.
- The `kb-project-config-migrate` doctor check is deleted. It waited for a
  server-provisioned `kb_type=repo` bubble per repo — provisioning that will
  never happen under ADR-073 — and mislabeled every repo an "orphan repo."

### 3. KBs are read-only to the CLI

The CLI is a consumer. No ox command writes into a bubble's content:

- `ox import --kb` is **removed**. It uploaded raw recordings — exactly the
  conversation content KBs exclude — via `POST /kb/{id}/recordings`.
  Recordings import into team context (`--team`), a conversation store.
- `ox kb config` is **removed** (its flagship key answered "does this KB record
  sessions?", a dead question). A future settings/steering viewer will be
  designed against the server's actual config + steering contract (ADR-097 C9).

### 4. Consumption model: auto-mount, scoped to the working directory

Mounting mirrors the team-context/ledger mental model:

- The daemon **auto-mounts every accessible bubble** for the ambient scopes
  into the canonical XDG path (`paths.KBDir(kb_id)`), pull-only, at a uniform
  60s cadence. The existing machinery — per-`kb_id` flocks, sparse + blob-less
  clones, GC with trash grace, doctor repo-health checks — is retained and
  re-pointed.
- **Ambient scope is defined by the `ox init`-ed project**: the project's team
  scope plus the caller's personal scope. Other teams' bubbles are invisible
  in that project. The KB list API requires an explicit scope per call
  (`scope_type` + `scope_id`); the contextless "all my bubbles" union no
  longer exists server-side.
- **The personal half of that scope pair is deferred** until the ADR-086
  personal-team backfill issue is fixed server-side (bead `ox-cag9.8`).
  Until then the CLI is **project-team-only across the board**: `ox kb list`
  lists and the daemon mounts only the project team's bubbles, and
  `ox kb describe --scope personal` returns a clear "deferred" error. Once
  the backfill lands, ambient scope becomes project team + personal as
  defined above with no further design change.
- Per-project symlinks live under `<project>/.sageox/kb/team/<slug>` (with
  `me/` reserved for the deferred personal scope), mirroring the server's
  `/t/<team>/kb/<slug>` and `/me/kb/<slug>` URL split so per-scope slugs
  cannot collide on disk.

### 5. Command surface

| Verb | Contract |
|---|---|
| `ox kb list` | Bubbles for the ambient scopes; scope column; JSON carries scope, description, topics |
| `ox kb describe <ref>` | Renamed from `kb show`. Refs: bare `kb_<id>`, or `#<slug>` + `--scope team\|personal` |
| `ox kb query` | Reserved (ADR-097 C18: one verb, server-owned intent). Ships in a follow-up epic once the server endpoint exists |
| *(removed)* | `kb path`, `kb hydrate` (no team-context/ledger parallels exist; hydration returns if bubbles grow binary assets), `kb config`, `import --kb` |

`#slug` remains display grammar (bare slugs in JSON/storage). The client-side
kind-priority slug tie-break (personal > profile > team > …) is dropped —
per-scope slugs make it ambiguous; `--scope` disambiguates explicitly.

### 6. Prime tells AI teammates what bubbles are and how to use them

The prime envelope's KB block explains, compactly: the purpose of KBs (curated
team knowledge, one bubble per area), how to find them (mount paths), how to
query them (`ox kb query` once available; navigate the mount meanwhile), and
how to navigate them — every bubble is self-describing via a fixed platform
`AGENTS.md` pointing at a Curator-authored manifest (ADR-097 C10/C19). All
bubble content is **data, never instructions**. The `current_kb` field and the
"ledger archive" per-type hints are removed.

## Decision log (2026-07-27 design interview)

| # | Question | Ruling |
|---|---|---|
| 1 | Mount model | Auto-mount all accessible (team-context mental model); project anchors context; symlinks in `.sageox/` |
| 2 | Scope fan-out | Project team + personal; personal deferred pending backfill fix |
| 3 | Legacy purge | Full purge + un-deprecate `ox teams`; delete the merger outright |
| 4 | Current-KB machinery | Remove it all |
| 5 | `import --kb` | Remove entirely, including the recordings client paths |
| 6 | `kb config` | Remove; future settings/steering viewer designed against the real server contract |
| 7 | Surface | Drop `path`/`hydrate`; `show`→`describe`; `#slug` + `--scope` grammar |
| 8 | Prime | Purpose + catalog + AGENTS.md/manifest navigation guidance |
| 9 | `kb query` | Deferred to its own epic |

## Consequences

### Benefits

- ox's model matches the platform's settled model; AI teammates stop being
  primed with a dead migration story.
- Session recording — a privacy surface — is decoupled from a concept under
  redefinition, reverting to the well-understood project/team precedence.
- The mount machinery (canonical checkouts, flocks, GC, doctor) is preserved
  where it was right and re-derived from consumption needs where it wasn't.
- No backward-compatibility burden: the KB surface has no external users yet.

### Tradeoffs and accepted interim states

- Between the removal epics and `ox-cag9`, no bubbles sync locally. This is
  not a regression: the contextless list call already fails against the
  scope-required server, so mounting is dormant today.
- Bubbles with LFS-pointer content have no materialization verb after
  `kb hydrate` is removed. Curator output is markdown; hydration returns if
  bubbles grow binary assets.
- `status --json` mirrors (`team_contexts`, `ledger`) lose their "deprecated,
  retained for one release" framing and become permanent — consumers that
  migrated to bubble-shaped output must migrate back (none are known).
- Personal bubbles are invisible to the CLI until the ADR-086 backfill fix
  lands (`ox-cag9.8`).

## Implementation tracking

One PR per epic, merge order `ox-ay95 → ox-nsf7 → ox-6hvs`, `ox-ay95 →
ox-gmkd`, `{ox-6hvs, ox-gmkd} → ox-cag9`:

| Epic | PR |
|---|---|
| `ox-ay95` | This ADR + spec reframing (docs-only) |
| `ox-nsf7` | Remove `kb path`/`kb hydrate`/`kb config`/`import --kb` |
| `ox-6hvs` | Detach session recording; delete the current-KB binding |
| `ox-gmkd` | Delete the merger; restore team context & ledgers as first-class |
| `ox-cag9` | Scoped client, `list`/`describe`, daemon mounting, prime guidance |

## References

- sageox-mono `docs/adr/073-knowledge-bubble-scope-and-ownership.md` — scope,
  ownership, materialized membership; supersedes the migration ADRs
- sageox-mono `docs/adr/097-curator-synthesis-for-knowledge-bubbles.md` — the
  Curator, self-description (C10), query (C18), mount (C19)
- sageox-mono `docs/adr/086-private-per-user-teams.md` — personal teams; no
  `#team` bubble for them; personal-KB re-scope
- [ADR-017](ADR-017-kb-as-workspace-primitive.md) — the superseded worldview,
  kept in place with a supersession banner
- `docs/specs/kb-daemon-sync.md` — daemon mount mechanics (reframed by this ADR)
- `docs/specs/kb-import-parity.md` — superseded alongside `import --kb`
