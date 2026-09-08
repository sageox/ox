# Native-First, Project-Scoped Skill Management

SageOx authors each playbook once in the portable Agent Skills layout:

```text
extensions/skills/<skill>/
  SKILL.md
  references/     # optional
  assets/         # optional
  scripts/        # optional
```

The embedded catalog is the built-in source of truth. `core`, `onramp`, and
`lifecycle` are selected by default. Authenticated Team Context sources may
extend the catalog later, but must use the same Plan/Apply engine rather than
introduce a second installer or activation framework.

## Native targets

Adapters declare target descriptors instead of independently managing files:

```go
type SkillTarget struct {
    Key        string
    Root       string
    Format     string
    Scope      string
    LinkPolicy string
}
```

The CLI validates repository-relative paths and deduplicates targets by their
canonical root and format. Codex and Gemini therefore produce one shared
`.agents/skills` projection and one truthful change count.

| AI coworker | Target | Format |
|---|---|---|
| Claude Code | `.claude/skills/<skill>/` | Native Agent Skills |
| Codex | `.agents/skills/<skill>/` | Portable Agent Skills |
| Gemini CLI | `.agents/skills/<skill>/` | Portable Agent Skills (shared with Codex) |
| AI coworker without native skills | None | Future Skill Bridge; not implemented speculatively |

All managed targets are project-scoped, and every managed file is GITIGNORED
under a reserved namespace (ADR-031) — with exactly one exception, the committed
`sageox` on-ramp skill, which is the only SageOx artifact present on a machine
where the CLI is not installed.

All managed targets are project-scoped. Native discovery and activation remain
authoritative; SageOx does not replace vendor-trained routing with hooks, a
bootloader, or an MCP page-fault path.

## Desired state and ownership

`.sageox/skills.lock.json` records:

- built-in source revision and ox version;
- selected bundle IDs and target keys;
- normalized target descriptor snapshots;
- every managed file's repository-relative path, SHA-256 digest, and mode.

Ownership is split across two files as of schema 2 (ADR-031): the COMMITTED
`.sageox/skills.lock.json` carries the project's selection (bundles, targets),
while the machine-local, gitignored `.sageox/cache/skills-state.json` carries the
catalog revision, the ox version, and the per-file digests this machine
materialized. Without that split the manifest alone would keep producing a
tracked diff on every content-bearing release, even though every file it
describes is invisible to git. Schema-1 manifests migrate on read.

Inside the reserved namespaces (`ox-cli-*`, `sageox-team-*`) ox owns the bytes
ABSOLUTELY: a local edit is restored on the next reconcile rather than preserved
as a conflict. Preserve-on-edit was correct while these files were tracked — an
edit showed up in `git diff` — and becomes harmful once they are gitignored,
where a preserved edit is permanent silent drift. Overwrite is not delete,
though: ox removes only what it can prove it wrote, so unrecognized content
inside a reserved namespace is reported, never swept.

The lockfile—not an inline comment—is the ownership source. Existing
`ox-hash` stamps are accepted for one-release migration only when their body
hash verifies. New projections contain clean canonical Agent Skills content.

`Plan` is deterministic, read-only, and considers the complete skill tree:
`SKILL.md`, references, assets, and scripts. It classifies creates, updates,
removals, conflicts, and preserved files. `Apply` performs recoverable
per-file changes and commits the lockfile last.

Ownership rules are deliberately conservative:

- update or remove a file only when its current digest matches the last
  installed digest;
- recreate a recorded file that is missing;
- preserve user additions, unknown collisions, and modified managed files;
- remove unchanged retired files and clean only empty directories;
- on uninstall, preserve a modified retired file but relinquish ownership so
  it does not become a permanent Doctor conflict;
- reject symlinked targets, parent directories, lockfiles, and managed files.
  A central store symlinked into each repository was evaluated and rejected in
  ADR-031: symlinked skill entries do work in Claude Code and Codex, but as a
  delivery mechanism they silently no-op on Windows, dangle in containers, and
  make one editor save change every repository on the machine.

Apply writes `.sageox/cache/skills-apply.json` before target mutation. The
journal records each old/new digest pair. If a process exits before the
lockfile commit, the next Plan accepts only the previous or journaled digest,
finishes the operation, commits the manifest, and removes the journal. This
distinguishes interrupted SageOx writes from coincidentally similar user files.

## Lifecycle

- `ox init` adds only the targets selected for that invocation and persists
  them. It never equates later agent detection with authorization.
- `ox doctor` plans only committed targets. For the one-release migration, a
  target with a valid legacy stamp may bootstrap selection.
- `ox doctor --fix` applies the plan and preserves conflicts.
- uninstall removes unchanged owned files once across all targets.
- an upgrade converges on the next explicit Doctor/init lifecycle operation;
  the old process does not attempt to execute a newly installed catalog.

AMENDED by ADR-031: the daemon now runs a DETERMINISTIC reconcile check on its
slow tick (`skills-inventory-drift`). The objection below was to an AI coworker
performing repair; this is the same code path `ox doctor` runs, and it is bounded
— it never installs into a repository that selected no targets, never runs while
a session is recording, never touches the git index, and never rewrites a tracked
file. The sentence below stands for the AI-coworker case only.

The daemon does not schedule an AI coworker to perform deterministic skill
repair. Explicit lifecycle commands own reconciliation. Current desired state
produces an empty plan and no daemon task.

The imperative adapter RPCs remain for one compatibility release. Built-in
adapters and CLI workflows use target descriptors and central reconciliation.

## Team Context follow-on

Team Context transport is a separate authenticated source boundary. A future
manifest must identify bundle, revision, file digests, provenance, and script
approval policy. It will feed the existing portable catalog and Plan/Apply
engine. The API source of truth and storage ergonomics require their own human
review and are intentionally not selected here.

## Skill Bridge follow-on

Only an AI coworker that genuinely lacks native skill discovery should receive
a compact catalog through `ox agent prime` and activate a playbook through a
future `ox skill activate <id> --json` or equivalent MCP tool. Native-capable
AI coworkers continue receiving real native skills. Enabling the bridge for a
native-capable client requires measured activation-quality evidence first.
