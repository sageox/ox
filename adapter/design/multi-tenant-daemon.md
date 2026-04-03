# Multi-Tenant Daemon: Adapter Protocol Implications

## Context

The current daemon is one-per-repo: `WorkspaceID` is derived from the repo path, each repo gets its own Unix socket. The planned evolution is a single daemon per host shared across:

- All worktrees of a repo (e.g., git worktrees at different paths)
- All repos on the machine
- Multiple teams (a user may be a member of several ox teams using the same machine)

This has direct implications for the adapter protocol. Adapters currently assume one active context (one `repo_root`, one set of sessions). In a multi-tenant daemon, a single `ox-adapter-claude-code --serve` process handles sessions from multiple repos and potentially multiple team contexts simultaneously.

## What Changes in the Protocol

### Session identity becomes globally unique

Current `agent_id` (e.g., `OxA1b2`) is scoped to a single daemon instance. In a shared daemon, two users working in different repos might both have an agent with the same short ID. Agent IDs need a global uniqueness guarantee.

**Decision:** `agent_id` format becomes `<repo_id_prefix>-<random>` (e.g., `r7f3a2-OxA1b2`), where `repo_id_prefix` is the first 6 chars of `SHA256(repo_id)`. Daemon enforces uniqueness at session registration. Adapters treat `agent_id` as opaque.

### `repo_id` and `team_id` added to all serve-mode requests

Every request the daemon sends to an adapter includes routing context:

```json
{"id":1,"method":"find-session","params":{
  "agent_id":  "r7f3a2-OxA1b2",
  "repo_id":   "a1b2c3d4e5f6...",
  "team_id":   "team_xyz",
  "repo_root": "/Users/dev/projects/myapp",
  "since":     "2026-04-02T10:00:00Z"
}}
```

`team_id` is optional (present only when the session is associated with a team workspace). Adapters that don't need team isolation can ignore it. Adapters that do (e.g., an indexer that scopes results by team) use it.

### Push events carry `repo_id` for routing

```json
{"event":"entries","agent_id":"r7f3a2-OxA1b2","repo_id":"a1b2c3d4...","data":{"entries":[...],"new_offset":2048}}
```

The daemon routes push events by `(agent_id, repo_id)`. Events without a matching session registration are dropped with a warning (not an error — adapter may have restarted mid-session).

### Team isolation: adapters are not the enforcement boundary

Adapters must not be relied upon to enforce team isolation. The daemon is the trust boundary. An adapter receives `team_id` as context (for routing or scoping its own queries) but cannot be trusted to enforce that session data from Team A is not returned to a Team B client.

The daemon enforces isolation at the IPC layer: a CLI client authenticated to Team A's context only receives IPC responses for Team A sessions. The adapter is downstream of this — by the time a request reaches the adapter, the daemon has already decided the requesting client is authorized.

This means adapters can be stateless with respect to team isolation. They receive `team_id`, use it for scoping (e.g., an indexer adapter that queries a team-scoped API), and return data. Authorization is not their concern.

## What Does NOT Change

- The one-shot subcommands (`info`, `detect`, `install-hooks`, etc.) are stateless and always per-invocation. No multi-tenancy changes needed there.
- The `--serve` process model (one process per adapter type) is unchanged. Multi-tenancy increases the value of this model: one process handles more concurrent sessions, making the per-type approach even more efficient than per-session would be.
- NDJSON framing, request/response shapes, and serve-mode methods are unchanged. `repo_id` and `team_id` are additive fields in params.
- Crash recovery behavior is unchanged. On adapter crash, the daemon respawns and re-sends `find-session` for all active sessions of that type across all repos.

## Adapter Implementation Guidance

Adapters **should:**

- Key all internal state by `agent_id` (already the design — `agent_id` is globally unique in the multi-tenant model)
- Treat `repo_id` as an opaque string for correlation, not parsed
- Use `repo_root` (the filesystem path) for file operations, not `repo_id`
- Log `agent_id` and `repo_id` in all adapter-side log lines for debuggability
- Not assume that all active sessions share the same `repo_root`

Adapters **should not:**

- Use global mutable state indexed by `repo_root` string (paths can be the same across worktrees)
- Assume `team_id` is always present (it's optional for personal/non-team usage)
- Cache `team_id` → credentials mapping without TTL (team membership can change)

## Migration from Single-Repo Daemon

The protocol changes (`repo_id`, `team_id` in params) are additive. Adapters written for protocol v1 (single-repo) can ignore these fields — they just use `repo_root` as before. This is safe because in the single-repo daemon era, all sessions share the same `repo_root` anyway.

Protocol v2 can formally require `repo_id` and `team_id` in params. Until then, adapters receive them and may ignore them.
