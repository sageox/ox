# Adapter Types

The initial design focused on coding-agent session adapters. But ox integrates with more than just agents — version control, issue trackers, indexers. All of these are natural extension points with the same architectural need: agent-specific knowledge that shouldn't live in ox core.

## Adapter Type System

All adapter binaries use the same `ox-adapter-*` discovery prefix. The `type` field in the `info` response tells ox what subsystem the adapter belongs to. One binary naming convention, one discovery loop, different routing by type.

```json
{
  "protocol_version": 1,
  "name": "claude-code",
  "type": "session",
  ...
}
```

### Types

| Type | Purpose | Examples |
|------|---------|---------|
| `session` | Reads coding-agent transcripts, installs hooks | `claude-code`, `gemini`, `codex`, `amp`, `cursor` |
| `vcs` | Version control history, blame, diffs, branches | `git`, `perforce`, `svn` |
| `indexer` | Indexes external content for agent context (issues, PRs, docs) | `github`, `linear`, `jira`, `beads`, `confluence` |
| `test` | Simulates a session adapter — no real agent behind it | `test-session`, `test-slow`, `test-failure` |

ox routes adapters to the right subsystem at registration time based on their declared type. A `vcs` adapter is never asked for session data. A `session` adapter is never asked to index GitHub issues.

---

## Session Adapters (coding agents)

What they do:
- Read agent transcripts (`find-session`, `read-from-offset`)
- Install/check/uninstall hooks
- Detect agent presence

Who owns them: sageox (official), community (third-party)

Bundled: `claude-code`, `gemini`, `codex`
External: `amp`, `cursor`, `windsurf`, community agents

---

## VCS Adapters

ox currently assumes git everywhere. VCS adapters abstract the version control layer.

What they provide:
- Repository history (commits, authors, timestamps)
- File blame/annotate
- Branch list, current branch
- Diff between refs
- Staging area status

Why this matters: Perforce shops, SVN shops, Mercurial users exist. An ox-perforce adapter would make ox viable there without any ox core changes.

```
ox-adapter-git       — already implicit (ox's current behavior, made explicit)
ox-adapter-perforce  — community or enterprise
ox-adapter-svn       — community
```

VCS adapters don't need hooks or session recording. Their subcommands are different:

```
ox-adapter-git info                           → {type: "vcs", name: "git", ...}
ox-adapter-git detect                         → {detected: true}
ox-adapter-git current-branch                 → {branch: "main"}
ox-adapter-git log --limit 20                 → {commits: [...]}
ox-adapter-git blame --file path --line 42    → {author, timestamp, commit}
ox-adapter-git diff --from HEAD~1             → {files: [...], patch: "..."}
```

The `vcs` type is likely worth bundling in ox core for git — it's nearly universal. Perforce and SVN ship externally.

---

## Indexer Adapters

Indexers pull external content into the daemon's knowledge base so agents get richer context at prime time.

```
ox-adapter-github    — indexes PRs, issues, code search
ox-adapter-linear    — indexes Linear issues and projects
ox-adapter-jira      — indexes Jira tickets
ox-adapter-beads     — indexes Beads tasks (tight integration)
ox-adapter-confluence — indexes Confluence pages
```

What indexers do:
- `detect` — are credentials configured for this service?
- `index --since <timestamp>` — fetch and index new content
- `search --query "..." --limit 10` — search indexed content
- `get --id <id>` — fetch specific item

The daemon runs indexers on a schedule (similar to how it syncs ledgers). At agent prime time, ox queries relevant indexers to enrich the context injected into the agent.

```
ox agent prime
  → query ox-adapter-github: "recent PRs touching auth/"
  → query ox-adapter-linear: "open issues assigned to current user"
  → query ox-adapter-beads: "current sprint tasks"
  → inject all into prime output
```

Indexers are likely sageox-official (they touch the cloud services), but community indexers for Jira/Confluence make sense.

---

## Test Adapters

Test adapters simulate session adapters with controllable behavior. They are the key to testing ox internals without needing a real coding agent running.

### Uses

1. **ox CI**: Test session recording, doctor, rewind — without Claude Code or Gemini installed
2. **Adapter author development**: Write a new adapter while testing against a known-good test adapter to understand expected behavior
3. **Failure injection**: Test ox's crash recovery, timeout handling, degraded mode
4. **Performance testing**: Simulate large sessions, high-frequency tool calls

### Built-in Test Adapters

```
ox-adapter-test          — normal behavior, controllable via env vars
ox-adapter-test-slow     — simulates slow file reads (tests timeout handling)
ox-adapter-test-crash    — crashes after N requests (tests respawn logic)
ox-adapter-test-large    — generates large session files (tests offset handling)
```

### Control Protocol

Test adapters are controlled via environment variables or a control file:

```bash
# inject a fake session
OX_TEST_SESSION_FILE=/tmp/fake-session.jsonl ox-adapter-test --serve

# simulate crash after 3 read-from-offset calls
OX_TEST_CRASH_AFTER=3 ox-adapter-test --serve

# simulate 500ms latency per call
OX_TEST_LATENCY_MS=500 ox-adapter-test --serve
```

Or via a control socket for mid-test injection:

```bash
# in one terminal: start ox with test adapter
OX_ADAPTER_PATH=./bin ox daemon start

# in another terminal: inject entries mid-session
ox-adapter-test inject --entry '{"role":"user","content":"hello"}'
```

### Bundling

Test adapters are bundled in ox's development builds but NOT in release builds. They're gated by a build tag:

```go
//go:build testadapters
```

Released via a separate `ox-dev` or `ox-test` tarball for CI environments. Never shipped to end users in the standard release.

---

## Adapter Discovery: All Types Together

The daemon's discovery loop is type-agnostic. It scans for `ox-adapter-*`, calls `info` on each, reads the `type` field, and routes to the appropriate registry:

```
scan: find ox-adapter-* binaries
  ox-adapter-claude-code → info → {type: "session"} → register in SessionAdapterRegistry
  ox-adapter-git         → info → {type: "vcs"}     → register in VCSAdapterRegistry
  ox-adapter-github      → info → {type: "indexer"} → register in IndexerRegistry
  ox-adapter-test        → info → {type: "session", test: true} → register if testadapters build
```

One scan, one protocol, multiple subsystems. Adding a new adapter type later requires adding a new registry — not changing the discovery mechanism.

---

## Bundled vs External: The Practical Split

### Bundled in ox monorepo (start here)

These are bundled in-tree initially because:
- They're used in ox's own tests
- They're nearly universal
- Moving them out later is straightforward

```
session: claude-code, gemini, codex, git (as vcs)
test:    test, test-slow, test-crash
```

### External (sageox/ox-adapters repo)

Official but separate release cadence. sageox maintains them:

```
session: amp, cursor, windsurf, opencode, factory-ai
indexer: github, linear, beads
vcs:     perforce (if enterprise demand exists)
```

### Community (third-party repos)

Anyone can write and publish:

```
session: any new coding agent
indexer: jira, confluence, notion, asana
vcs:     svn, mercurial
```

### Locally plugged-in (no repo, no install)

Just drop a binary in `~/.local/share/ox/adapters/` or set `$OX_ADAPTER_PATH`. Useful for:
- Enterprise internal adapters (can't be open source)
- In-development adapters being actively built
- Test adapters for CI pipelines that don't use standard agents

```bash
# enterprise internal adapter, not in any public repo
cp /internal/tools/ox-adapter-internal-agent ~/.local/share/ox/adapters/
ox adapter list  # shows it as "local"
```

No registry approval, no PR, no install command. Binary present = adapter available.
