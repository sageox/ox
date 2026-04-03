# ADR-015: WASM as Adapter Runtime — Evaluation and Deferral

**Status**: Not adopted — deferred pending WASM Component Model maturity in Go toolchain
**Date**: 2026-04-02
**Revisit cadence**: Every 3–6 months (next: October 2026)

---

## Context

The existing adapter design uses platform-native binaries communicating over stdin/stdout NDJSON.
Each adapter ships as 5 platform binaries (darwin_amd64, darwin_arm64, linux_amd64, linux_arm64,
windows_amd64), distributed via GitHub releases or the `sageox/ox-adapters` registry.

During Cambridge v4 design, we evaluated whether adapters should instead be distributed as
`.wasm` modules loaded by the ox daemon at runtime. This ADR captures the evaluation in full
so future engineers can make an informed decision when revisiting.

---

## What WASM Promises

1. **Single artifact**: one `.wasm` file per adapter, regardless of target OS or CPU architecture.
   Community adapter authors manage one release artifact instead of five.

2. **Isolation**: WASM modules run in a sandbox. A buggy adapter cannot corrupt the daemon's heap
   or access files outside the directories it's granted.

3. **Controlled resource use**: some WASM runtimes support instruction-count fuel limits, enabling
   the daemon to cap CPU use per adapter call.

4. **Language flexibility via WIT**: the WASM Component Model (WIT/WASIp2) defines typed interfaces.
   In theory, an adapter author writes against the WIT interface and compiles from any language
   the WIT toolchain supports.

---

## What WASM Actually Requires (April 2026)

### WASM Component Model availability

The Component Model (WIT/WASIp2) is what makes cross-language typed interfaces possible. Without
it, adapters must manage WASM linear memory manually — directly allocating and reading byte buffers
for every string, slice, and struct crossing the host boundary. This is ergonomically equivalent
to writing C, and strictly worse than the existing pipe protocol.

Current state of Go-relevant runtimes as of April 2026:

| Runtime | Component Model | Notes |
|---------|----------------|-------|
| **wazero** (pure Go) | **Not implemented** | Open GitHub issue; not yet merged. Required for WIT/WASIp2 interfaces. |
| **wasmtime** (Rust/C) | Implemented | Not embeddable in pure Go binaries without CGO |
| **TinyGo** (Go → WASM) | Partial | Limited stdlib; `os`, `net`, `database/sql` constraints |
| **Standard Go wasip1** | None | Only WASIp1 (file system, clocks); no Component Model; 8–15 MB binary per module |
| **Rust → WASM** | Excellent | Best-in-class WIT support; wrong language for Go-native team |

wazero is the only pure-Go WASM runtime that ox could embed without pulling in CGO or shipping
a native library. Its Component Model gap is a hard blocker for typed interfaces.

Without the Component Model, the WASM adapter interface would look like:

```go
// host side — every string is a pointer+length pair in WASM linear memory
ptr, len := wasm.Call("find_session", agentIDPtr, agentIDLen, repoRootPtr, repoRootLen, ...)
result := wasm.Memory().Read(ptr, len)
```

This is worse than a JSON line on a pipe, not better.

---

## Performance Analysis (Principal Engineer Review)

### Hot path: `read-from-offset`

This is the critical path — called on every PostToolUse hook, target latency < 100ms end-to-end.

| Approach | Measured latency | Notes |
|----------|----------------|-------|
| Binary serve mode (current) | 0.1–0.5 ms/call | Adapter holds fd open; read is one syscall |
| WASM compiler mode (wazero) | 0.2–1 ms/call | JIT compilation amortized; cold module cache adds 5–20 ms first call |
| WASM interpreter mode (fallback) | 2–10 ms/call | No JIT; unacceptable for the hot path |

**Why binary wins on the hot path**: the adapter owns its file descriptor directly. A
`read-from-offset` call in serve mode is:

```
daemon writes JSON line to pipe → adapter reads, seeks fd, reads bytes, writes response → daemon reads
```

The file read involves zero data copies across any boundary. In WASM, the same operation
requires the adapter to write the entry bytes into WASM linear memory, then the host copies
them out into Go heap to serialize as JSON. Even ignoring the WASM execution overhead, the
extra memcpy adds latency proportional to entry size.

### Deadlock behavior

**Binary serve mode**: timeouts are clean. If the adapter process hangs, the daemon's read on
the pipe can be interrupted with `context.WithTimeout`. The daemon sends SIGTERM.

**WASM host imports**: if an adapter's WASM module calls a host function (e.g., `ox_write_entry`)
and the host function blocks, the WASM execution goroutine is parked. Interrupting a blocked
WASM goroutine requires threading a `context.Context` through every host import and checking it
at each yield point. wazero's fuel-based preemption (instruction count) can interrupt CPU-bound
WASM, but not WASM blocked on a host import. This creates a class of deadlocks that binary pipe
timeouts handle cleanly and WASM does not.

### Crash recovery

Both approaches recover from adapter failure by resuming from the last checkpointed offset.

For WASM: re-instantiation pays the JIT compilation cost if the module cache is cold (5–20 ms).
Warm cache (module already JIT-compiled) is equivalent to binary process startup (~2 ms).
In practice, the module cache will be warm during normal operation; cold restarts are uncommon.

This is **not** a meaningful disadvantage vs binaries — the difference is within noise.

### Memory

A wazero WASM instance for a typical adapter: 64–256 KB linear memory base + module code pages.
A Go binary adapter process: ~8–15 MB RSS (Go runtime overhead).

WASM wins on resident memory per adapter type. With multiple adapter types active (claude-code +
amp + cursor), the difference is 20–40 MB vs 3–6 MB. In practice, daemon RSS is dominated by
other factors (ledger cache, session state). Not a compelling difference either way.

---

## Cross-Platform Reality Check

WASM's strongest claim is cross-platform: one `.wasm` file instead of 5 platform binaries.

This is real, but the impact is limited for two reasons:

**1. Path logic is still platform-specific.** Adapters must locate agent session files, which live
in platform-specific directories:

```
macOS:   ~/Library/Application Support/<agent>/sessions/
Linux:   ~/.local/share/<agent>/sessions/
Windows: %APPDATA%\<agent>\sessions\
```

WASM eliminates the CPU instruction set difference, not the filesystem path difference. Adapter
code still contains `runtime.GOOS` branches or equivalent.

**2. goreleaser already solves the build matrix.** Official adapters in `sageox/ox-adapters` use
goreleaser to cross-compile and publish all 5 platform binaries in one CI step. The maintenance
burden is a 10-line goreleaser config, not 5 manual release artifacts.

The cross-platform benefit is more significant for **community authors** who maintain their own
release pipelines without goreleaser infrastructure. This becomes more compelling as the adapter
ecosystem grows.

---

## Forward Compatibility

The current design is WASM-compatible without changes to ox core:

- The `Adapter` interface (`internal/session/adapters.Adapter`) accepts any implementation.
  A future `WASMAdapter` struct implementing the same interface slots in alongside `ExternalAdapter`.
- The `ExternalAdapter` wrapper translates the interface to pipe calls. A `WASMAdapter` wrapper
  would translate the same interface to wazero host calls.
- Adapter business logic maps directly to WASM exports: `find_session`, `read_from_offset`, etc.
  Migration from a Go binary adapter to a WASM module is straightforward when the toolchain matures.
- The language-agnostic binary protocol means the Rust WASM story (best-in-class WIT support)
  becomes available to Rust adapter authors without ox core changes — Rust authors already write
  against the protocol spec directly.

---

## Decision

**Not adopted.** Keep the current binary protocol for the following reasons:

1. wazero lacks WASM Component Model support — the typed interface story is not available.
   Without it, WASM is ergonomically worse than pipes, not better.

2. Hot path performance favors binary serve mode due to direct fd ownership and zero cross-boundary
   memcpy for session file reads.

3. Deadlock handling is cleaner in the binary model — pipe timeouts via context cancel vs WASM
   host import blocking that cannot be interrupted by fuel limits.

4. The cross-platform benefit is real but addressed by goreleaser for official adapters, and
   community adapter volumes don't yet justify the additional runtime complexity.

5. The current design is forward-compatible: a `WASMAdapter` can be added later without
   protocol changes.

---

## Criteria to Revisit

Check these every 3–6 months. If 2 or more conditions are met, do a fresh evaluation:

1. **wazero merges WASM Component Model support** — watch: github.com/tetratelabs/wazero.
   This is the single hardest blocker. Without it, the ergonomic case for WASM doesn't exist.

2. **TinyGo Component Model reaches full stdlib compatibility** — TinyGo's partial support
   currently excludes standard packages Go adapter authors rely on (`database/sql`, `net/http`).

3. **Community adapter ecosystem reaches 20+ independent authors** — at this scale, the
   cross-platform build burden becomes a real friction point that goreleaser doesn't solve for
   authors without CI infrastructure.

4. **wazero compiler mode gains fuel-based preemption for host import blocks** — this closes the
   deadlock gap. Until then, WASM cannot match binary pipe timeout semantics.

---

## Appendix: WASM Component Model Primer

For engineers unfamiliar with the WASM Component Model:

The **WASM Core** spec (1.0, 2.0) defines a portable bytecode format and linear memory model.
Passing data across the host/WASM boundary requires manual memory management: the caller
allocates linear memory, writes bytes, passes a pointer+length pair, and the callee reads the
same bytes by pointer.

The **WASM Component Model** (WIT/WASIp2) adds a layer above this: typed interface definitions
(`.wit` files) that describe functions, records, and variants using high-level types. Toolchain
components (`wit-bindgen`, `wasm-tools`) auto-generate the memory management glue, exposing a
typed API to both host and guest code.

Without the Component Model, every string, struct, and slice that crosses the boundary requires
explicit manual serialization into the WASM module's linear memory. This is what "manually
managing WASM linear memory" means in practice.

The Component Model is what makes WASM genuinely more ergonomic than raw IPC for multi-language
plugin systems. Until it's available in wazero, WASM is not better than pipes.
