# ox Adapter System — Working Design Docs

This directory contains design documents, ADRs, and open questions for the external adapter system. Not checked in — working scratch space.

## Context

ox currently hardcodes all agent adapters (Claude Code, Gemini, Codex) into the ox binary. Every new AI coding agent requires modifying ox source and cutting a release. This doesn't scale.

The adapter system extracts that per-agent knowledge into standalone binaries (`ox-adapter-*`) that ox discovers and delegates to at runtime. New agents ship as new binaries — no ox changes needed.

## Documents

### Architecture Decision Records

| ADR | Decision | Status |
|-----|----------|--------|
| [ADR-001](adr/001-external-adapter-binaries.md) | External binaries over built-in adapters | Proposed |
| [ADR-002](adr/002-naming-convention.md) | `ox-adapter-*` naming | Proposed |
| [ADR-003](adr/003-ipc-mechanism.md) | stdin/stdout NDJSON over gRPC/sockets | Proposed |
| [ADR-004](adr/004-repository-structure.md) | Monorepo under `cmd/` | Proposed |
| [ADR-005](adr/005-daemon-as-supervisor.md) | Daemon owns adapter process lifecycle | Proposed |
| [ADR-006](adr/006-distribution.md) | Same release tarball, no registry API (phase 1) | Proposed |
| [ADR-007](adr/007-shared-packages.md) | Shared `pkg/ndjson`, `pkg/adapterprotocol`, `internal/progress` | Proposed |

### Design Docs

| Doc | Topic |
|-----|-------|
| [Protocol Spec](protocol/spec.md) | Full protocol: subcommands, NDJSON wire format, types |
| [Daemon Integration](design/daemon-integration.md) | How daemon supervises adapter processes |
| [Installation](design/installation.md) | Distribution, Homebrew, local dev |
| [Testing Strategy](design/testing.md) | Three-layer test approach, compliance suite |
| [Migration Path](design/migration.md) | Transition from built-in adapters to external |
| [Multi-Tenant Daemon](design/multi-tenant-daemon.md) | Multi-repo, multi-team, multi-worktree implications |
| [Subagent Workers](design/subagent-workers.md) | Adapter-controlled agentic sessions, worker lifecycle, ox config |

### Open Questions

See [OPEN-QUESTIONS.md](OPEN-QUESTIONS.md) — decisions not yet made.

## Related Context

- ox's current adapter code: `internal/session/adapters/`
- Current hook installation: `cmd/ox/hooks_claude.go` et al.
