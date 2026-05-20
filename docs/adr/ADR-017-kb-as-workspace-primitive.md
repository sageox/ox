# ADR-017: Knowledge Bubble (KB) as the Unifying Workspace Primitive

**Status**: Proposed
**Date**: 2026-05-18

## Context

ox today has two implicit, separately-modeled concepts of "where am I working":

1. **Legacy ledger binding.** A directory tree with a `.sageox/` directory at its root is bound to a `repo_id`, which in turn resolves to a per-project ledger git repo on disk. Session recording, murmurs, and several other behaviors target that ledger. The discovery rule is a classic path-walk: walk up from `cwd` looking for `.sageox/`, read `repo_id` from `.sageox/config.json`, and use that. Outside any such tree, the behaviors silently no-op (no session recording, no murmur target).

2. **Knowledge Bubble (KB) catalog.** A newer, unified surface where every piece of structured knowledge — personal scratchpad, team conventions, public profile, repo-specific archive (the ledger!), and ad-hoc custom bubbles — is a row in a single typed table (`KBType ∈ {personal, profile, team, repo, custom}`). KBs are auto-synced by the daemon, sparse-checked-out under `~/.local/share/sageox/<endpoint>/kb/<kb_id>/`, and listed by `ox kb list`. See ADR-006 (context fallback layers) and the recent KB CLI work (`feat(kb): unified kb CLI with personal + clone parity`, #595).

These two models overlap on exactly one point: the legacy ledger is, conceptually, a `kb_type=repo` KB. Everywhere else they diverge — a personal KB has no "directory I can `cd` into and have it become my recording target"; a team KB cannot be the target of `ox session start` from within the team KB's own working tree; a non-git directory cannot be tagged as belonging to any KB at all.

The divergence is starting to cause concrete pain:

- **`ox-43q0.3`** ("Add KB-API tier to `ResolveSessionRecording` precedence chain") assumes a function exists that returns "which KB does this session belong to." None exists for non-repo KB types — the implementation silently delegates to the legacy ledger lookup, which only handles `kb_type=repo`.
- **`ox-xkr3`** ("ox agent prime — narrative guidance for KBs") wants `ox agent prime` to tell the agent which KB it is currently operating inside. Today the envelope lists all available KBs but cannot answer "which one is current?" outside of a git-rooted project.
- **`ox-43q0`** (epic: unified KB config like `git config`) explicitly models `.sageox/config.yaml` as the per-KB local-config layer, but only if the KB happens to have a directory at the user's CWD root. Personal and team KBs have no such anchor today.
- The forward-looking case the founder raised: a future where a directory tree can be bound to a KB without containing a `.git` repository at all (a scratch dir, a documentation subtree, a Conductor parent workspace).

The honest framing: **what we called "the ledger" was always a KB — specifically a `kb_type=repo` KB — we just hadn't yet recognized the general type**. The path-walk algorithm and the "outside the tree → silent no-op" semantics were correct then and remain correct; what's changed is our understanding of the population being resolved against. The ledger was the first concrete instance of a primitive we hadn't yet named. Continuing to layer additional KB types on top of a ledger-shaped resolver — without promoting the resolver to operate over KBs generally — guarantees that every new KB-type-aware behavior either reimplements the path-walk or special-cases `kb_type=repo`.

## Decision

Adopt **Knowledge Bubble (KB) as the canonical workspace primitive**, with a single resolver that maps any path on disk to its bound KB (or `nil`).

### 1. "Current KB" is a first-class concept

Introduce `kb.ResolveCurrentKB(cwd string) (*KBBinding, error)` in `internal/kb/resolve.go`. The function walks upward from `cwd` searching for KB-binding markers and returns the first one found, or `(nil, nil)` if none.

```go
type KBBinding struct {
    KBID       string  // kb_xxx — immutable identifier
    KBType     string  // personal | profile | team | repo | custom
    Source     string  // ".sageox/config.yaml" | ".sageox/config.json"
    Anchor     string  // absolute path of the marker that bound this tree
    Scope      string  // "exclusive" (default) | "subtree" (reserved; see §6)
}
```

The return value is the only sanctioned answer to "which KB am I operating against?" — across session recording, murmurs, prime envelopes, and any future KB-scoped behavior. Outside any KB-bound tree, the resolver returns `(nil, nil)` and the caller must implement the no-op fallback.

### 2. One marker shape: the `.sageox/` directory

The resolver looks for exactly one marker — a directory named `.sageox/` containing
a `config.yaml` file with at minimum a `kb_id` field. The same shape covers both
workspace and binding-only trees; the difference is what *else* lives inside the
directory, not the marker shape.

| Tree | Minimum `.sageox/` payload | Additional payload (workspace) |
|------|---------------------------|--------------------------------|
| Binding-only | `config.yaml` with `kb_id: kb_xxx` | (none) |
| Workspace | `config.yaml` with `kb_id: kb_xxx` | `cache/`, `ledger/`, `kb/<slug>` symlinks, indexing artifacts |

The resolver tries marker formats in priority order within a directory before
walking to the parent:

1. `.sageox/config.yaml` (current)
2. `.sageox/config.json` (legacy; readable for a deprecation window; see §7)

There is no separate `SAGEOX.yml`-style file marker. Callers that need to
distinguish workspace from binding-only call `KBBinding.IsWorkspace()` (returns
true if `cache/` or other workspace-only paths exist alongside `config.yaml`).

### 3. The binding's payload is minimal

The marker carries the minimum content needed to resolve to a KB and nothing more:

```yaml
# .sageox/config.yaml (minimum)
kb_id: kb_01H...

# .sageox/config.yaml (with explicit endpoint — for multi-endpoint users)
kb_id: kb_01H...
endpoint: sageox.ai
```

The `endpoint:` field is optional. If absent, the resolver uses the user's current default endpoint via `endpoint.Get()`. Multi-endpoint users (running prod and dev simultaneously) include the field to disambiguate which endpoint owns the `kb_id`; single-endpoint users omit it. A new doctor check `kb-binding-endpoint-mismatch` surfaces ambiguity (binding's `endpoint:` differs from the user's default, or `endpoint:` is absent but the `kb_id` is not resolvable under the default endpoint), but doctor never auto-rewrites the field — endpoint disambiguation is a user decision.

Workspace `.sageox/config.yaml` files may additionally carry workspace-scoped fields (`repo_id`, ledger settings, indexing options, etc.) as they do today. A binding-only `.sageox/config.yaml` is just `kb_id` (and optionally `endpoint`).

### 4. Behaviors that resolve through `ResolveCurrentKB`

The following behaviors switch from their current type-specific lookups to the unified resolver:

| Behavior                          | Today                                                                   | After ADR-017                                                                |
|-----------------------------------|-------------------------------------------------------------------------|------------------------------------------------------------------------------|
| Session recording target          | Path-walk for `.sageox/` → `repo_id` → legacy ledger                    | `ResolveCurrentKB(cwd)` → KB. Records into that KB, regardless of type.      |
| Auto-recording on/off             | Recording enabled iff inside a `.sageox/` tree                          | Recording enabled iff `ResolveCurrentKB(cwd) != nil`. Same semantics, broader reach. |
| Murmur target                     | Legacy ledger only                                                      | Current KB (any type).                                                       |
| `ox agent prime` envelope         | Emits all KBs; agent does not know which is "current"                   | Adds `current_kb` field populated from `ResolveCurrentKB(cwd)`.              |
| `ox kb config` local layer        | `.sageox/config.yaml` (only inside a project root)                      | Read-only resolution from the binding's KB layer (`.sageox/config.yaml`) via `ox kb config get/list`. Writes happen via hand-edit + git push or the web UI; ox does not mediate writes. |
| `config.IsInitialized(root)`      | Returns bool                                                            | Retained for callers that need workspace-vs-binding distinction; new callers prefer `ResolveCurrentKB`. |

### 5. Daemon spawn semantics

The ox daemon is per-workspace today, keyed by `workspace_id = hash(repo_id)`
(see ADR-002 §1). With `.sageox/` now also marking binding-only trees, the
question is whether binding-only trees also spawn daemons. They do not.

| Tree resolved by `ResolveCurrentKB` | `ox agent prime` behavior |
|------------------------------------|----------------------------|
| nil (no marker found) | Status `unavailable`. No daemon. |
| Workspace `.sageox/` | Spawn per-workspace daemon as today. Daemon may also hold the per-(user, endpoint) global-sync lease (see daemon-debate). |
| Binding-only `.sageox/` | Do NOT spawn a daemon. If any workspace daemon for this endpoint is alive on the machine, rely on it for global sync. Otherwise fire `ox kb sync --all --quiet &` as a one-shot subprocess and return. |

Rationale: binding-only trees have no local state requiring a long-lived
process (no ledger, no code index, no session recording state). Spawning a
daemon per binding-only tree multiplies daemon processes for no benefit. The
leader-election lease (daemon-debate PR2) ensures one workspace daemon per
endpoint handles global sync; binding-only trees consume that daemon's output.
The one-shot fallback covers the case where the user has no workspace open
on the machine.

### 6. Subtree overrides — design space reserved, implementation deferred

A nested `.sageox/` directory inside another `.sageox/`-rooted tree should be able to remap a subtree to a different KB (e.g. `docs/.sageox/` binding `docs/` to a team-docs KB while the rest of the repo binds to the repo KB). The `KBBinding.Scope` field reserves this design space (`"subtree"`), but the v1 implementation rejects subtree overrides with a clear error. The semantics — what about nested subtree overrides, what about session-state that straddles a boundary — are non-trivial and worth getting right separately.

### 7. Backward compatibility

- `.sageox/config.json` (legacy JSON, repo-only) continues to resolve correctly. The resolver reads `repo_id` from it and synthesizes a `kb_type=repo` `KBBinding` from the merger's ledger-legacy row.
- Every code path that today calls `config.IsInitialized(root)` keeps working — the function is unchanged. New code that needs the *identity* of the current KB calls `ResolveCurrentKB` instead.
- No on-disk file is renamed or moved. Existing `.sageox/` directories keep working; binding-only trees use the same `.sageox/` marker shape with a minimum `config.yaml` payload.

## Consequences

### Benefits

- **One mental model.** "Which KB am I in?" has a single answer derived from one resolver. Ledger discovery is no longer a special case; it is the `kb_type=repo` branch of the resolver's output.
- **Session recording becomes uniform.** `ox session start` inside `$(ox kb path team-conventions)` records into the team bubble — the same way it records into the project ledger today. No new code path per KB type.
- **Non-git workspaces become first-class.** A scratch directory with a `.sageox/config.yaml` binding is now a legitimate place to do recorded work. Conductor parent dirs, doc trees, learning sandboxes, anywhere a `.git/` would feel heavyweight.
- **Prime gets stronger.** Adding `current_kb` to the prime envelope answers the question "where does my work go right now?" — a much stronger handoff than the today's flat list of available KBs.
- **Future-proofs the KB config epic.** `ox-43q0` (`kb config` like `git config`) gets the precedence chain it needs: `defaults → user → KB-API → local marker → env`, with "local marker" being whichever of the two formats the resolver found.
- **Cheaper to add KB-aware features.** Anything that needs "current KB context" becomes a one-line call instead of a path-walk reimplementation.

### Tradeoffs

- **Two file formats means two parser paths.** The resolver must handle `.sageox/config.yaml` and `.sageox/config.json` (legacy). Each format we accept is a place a bug can live. Mitigated by routing both through a single `loadBinding(path)` helper that returns a normalized `KBBinding`.
- **Subtree overrides will be tempting before they are ready.** Reserving the design space (`Scope` field) creates a forward-compatibility contract that limits future flexibility. Acceptable because rejecting `subtree` in v1 keeps the door open and doesn't ship broken semantics.
- **More places for a misconfigured marker to break things.** A user who hand-writes a `.sageox/config.yaml` with a non-existent `kb_id` gets "no current KB" behavior even though a marker exists. Doctor must surface this: new check `kb-binding-invalid` that detects markers referencing unknown `kb_id`s and offers to fix or remove. Auto-fix level `CheckOnly` (this is user-authored content; we don't silently rewrite it).
- **The legacy `repo_id`-in-`.sageox/config.json` model is now load-bearing for slightly less.** That field used to be the only thing that mattered for binding; now it's one resolver input alongside `kb_id`. The deprecation path for `.sageox/config.json` becomes slightly more delicate because it now interacts with the resolver, not just session recording.

### Risks explicitly accepted

- Users who have hand-written tooling that greps for `.sageox/config.json` will not see `.sageox/config.yaml` markers. We accept this — third-party tooling can adopt the new format on its own schedule.
- The resolver walks the filesystem on every `ox` command that needs current-KB context. We accept the cost (it's the same walk we do today) and will revisit caching if profiling shows it.

## Implementation tracking

This ADR establishes the *concept*. Implementation is decomposed across beads:

- **`ox: internal/kb.ResolveCurrentKB resolver + .sageox/ marker parsing`** — the resolver itself, with `KBBinding` type and path-walk logic.
- **`ox: vendor KBConfig Go types + KBTypeChannel + drift detector`** — supersedes ox-43q0.1; vendors the canonical Go schema from sageox-mono, pre-ships KBTypeChannel, includes the drift-detection script.
- **`ox: ox kb config get/list with --show-origin/--at/--json`** — supersedes ox-43q0.5; read-only CLI for inspecting effective config. No `set` verb.
- **`ox: ResolveSessionRecording integrates ResolveCurrentKB + safety inversion`** — supersedes ox-43q0.3.
- **`ox: prime envelope adds current_kb field`** — extends ox-xkr3.
- **`ox: ox doctor migration of project-side .sageox/config.json → .yaml`** — extends ox-43q0.4 with the (D) hybrid-read + staged-deprecation timeline.
- **`fix(kb): cross-process flock guards on shared KB working trees`** — daemon-debate PR1; closes the Tier 1 silent-corruption race.
- **`feat(daemon): leader-elect per-(user, endpoint) global-sync owner via flock lease`** — daemon-debate PR2; closes the redundant-sync problem without adding a global daemon flavor.
- **`[HUMAN] kb config: revisit committed_at + banner with mono team`** — cross-repo pushback; the in-file `committed_at` and banner are redundant with git metadata and bloat hand-edits.

## References

- ADR-006: Context Fallback Layers — established the precedence model that this ADR extends to apply to KB-resolution.
- `internal/kb/merge.go` — three-source merger (kb-API + team-legacy + ledger-legacy) whose output is the population this ADR's resolver maps a directory to.
- `internal/paths/paths.go:514-600` — canonical KB on-disk layout (`KBDir`, `ProjectKBLink`).
- `cmd/ox/agent_prime.go:2071-2093` — `buildPrimeKBEnvelope`, which gains the `current_kb` field per §4.
- Beads epic `ox-43q0` — KB config (like `git config`) — the implementation track for the unified config layer this ADR's binding format participates in.
