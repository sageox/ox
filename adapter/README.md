# ox Adapter System

Design documents and ADRs for the external adapter system.

ox currently hardcodes all agent adapters into the binary. The adapter system extracts per-agent
knowledge into standalone binaries (`ox-adapter-*`) that ox discovers and delegates to at runtime.
New agents ship as new binaries — no ox changes needed.

## Start Here

- **Building an adapter (Go)?** → [Adapter SDK Guide](design/adapter-sdk.md) — SDK-first, minimal boilerplate
- **Building an adapter (any language)?** → [Adapter Author Guide](design/adapter-author-guide.md) — full protocol details
- **Understanding the protocol?** → [Protocol Spec](protocol/spec.md)
- **Visual overview?** → [Diagrams](DIAGRAMS.md)

## Architecture Decision Records

| ADR | Decision |
|-----|----------|
| [001](adr/001-external-adapter-binaries.md) | External binaries over built-in adapters |
| [002](adr/002-naming-convention.md) | `ox-adapter-*` naming convention |
| [003](adr/003-ipc-mechanism.md) | stdin/stdout NDJSON, two-way with push events |
| [004](adr/004-repository-structure.md) | Separate `sageox/ox-adapters` repo; `pkg/adapterprotocol` public |
| [005](adr/005-daemon-as-supervisor.md) | Daemon owns adapter process lifecycle |
| [006](adr/006-distribution.md) | Bundled + registry distribution, no `$PATH` scan |
| [007](adr/007-shared-packages.md) | Shared `pkg/ndjson`, `pkg/adapterprotocol` |
| [008](adr/008-wasm-evaluation.md) | WASM as adapter runtime — evaluated, deferred |

## Design Docs

| Doc | Topic |
|-----|-------|
| [Protocol Spec](protocol/spec.md) | Full protocol: subcommands, NDJSON wire format, types |
| [Adapter Author Guide](design/adapter-author-guide.md) | How to build an adapter, common pitfalls |
| [Adapter SDK Guide](design/adapter-sdk.md) | `pkg/adapterruntime` Go SDK: API, examples, typed handlers |
| [Adapter Types](design/adapter-types.md) | session, vcs, indexer, test adapter types |
| [Daemon Integration](design/daemon-integration.md) | How daemon supervises adapter processes |
| [Doctor Integration](design/doctor-integration.md) | How `ox doctor` calls adapter `diagnose` |
| [Installation](design/installation.md) | Distribution, Homebrew, local dev |
| [Testing Strategy](design/testing.md) | Three-layer test approach, compliance suite |
| [Failure Modes](design/failure-modes.md) | Edge cases, crash recovery, degraded states |
| [Migration Path](design/migration.md) | Transition from built-in adapters to external |
| [Multi-Tenant Daemon](design/multi-tenant-daemon.md) | Multi-repo, multi-team, multi-worktree implications |
| [Subagent Workers](design/subagent-workers.md) | Adapter-controlled agentic sessions, worker lifecycle |
| [Diagrams](DIAGRAMS.md) | Visual protocol flows, architecture, state machines |

## Related Code

- Current adapter code: `internal/session/adapters/`
- Hook installation: `cmd/ox/hooks_claude.go` et al.
