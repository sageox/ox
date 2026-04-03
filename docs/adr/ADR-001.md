# ADR-001: Pure-Go, No CGo

**Status**: Accepted
**Date**: 2026-04-02

## Context

ox runs on macOS (Intel + ARM), Linux (amd64 + arm64), and Windows. It's distributed as a single static binary via Homebrew, GitHub Releases, and `go install`. Cross-compilation is a hard requirement — GoReleaser builds all platform variants from a single CI runner.

CGo breaks this. It requires platform-specific C toolchains, complicates CI matrices, produces dynamically-linked binaries that fail on minimal containers, and makes `go install` unreliable for end users.

Two critical dependencies had CGo-only options at the time of adoption:

- **SQLite**: Used for whisper stores, CodeDB indexes, and daemon state. The dominant Go driver (`mattn/go-sqlite3`) requires CGo.
- **Tree-sitter**: Used for symbol extraction in CodeDB. The standard Go bindings (`smacker/go-tree-sitter`) wrap C parsers via CGo.

## Decision

All ox dependencies must be pure Go. No CGo, ever.

- **SQLite**: Use `modernc.org/sqlite` (pure-Go SQLite translation via c2go). Trades ~20% throughput for zero C dependency.
- **Tree-sitter**: Build a pure-Go symbol extraction layer. Accepts reduced language coverage in exchange for portability.
- **Future dependencies**: When evaluating new libraries, pure-Go is a hard filter, not a preference. If no pure-Go option exists, build one or find a different approach.

## Consequences

**Benefits**:
- `GOOS=X GOARCH=Y go build` works for every target — no cross-compilation toolchain
- GoReleaser config stays simple (no CGo flags, no platform-specific build steps)
- `go install github.com/sageox/ox/cmd/ox@latest` works for any user on any platform
- Static binaries with zero shared library dependencies
- Reproducible builds across CI and developer machines

**Tradeoffs**:
- `modernc.org/sqlite` is slower than `mattn/go-sqlite3` for write-heavy workloads (~20% penalty). Acceptable because ox's SQLite usage is dominated by reads (whisper queries, search) with infrequent writes (murmur relay, index builds).
- Pure-Go tree-sitter limits which languages get symbol extraction. Languages without pure-Go parsers fall back to regex-based extraction or are unsupported.
- Some high-performance libraries (e.g., compression, crypto) have CGo-accelerated paths. We accept the pure-Go performance ceiling.

**Decision rule**: If a future feature requires CGo and no pure-Go alternative exists, the feature ships as a separate optional binary (like the adapter pattern in ADR adapter-001) rather than contaminating the core ox binary.
