# Reference-Driven Native Filesystem Implementation

This document reconstructs the approach used to build the Swift FSKit
implementation of oxFS. It is intended as a reusable method for producing
another native implementation from the Rust reference, while preserving the
parts that worked and correcting the parts that created false confidence.

## Executive summary

The implementation followed a sound overall shape:

1. Treat the existing Rust implementation as the executable specification.
2. Separate the portable filesystem engine from the platform adapter.
3. Port behavior in dependency order: values and invariants, namespace,
   persistence, verified cache, policies, control plane, remote sources, then
   the filesystem API.
4. Port conformance scenarios with the code instead of designing new tests from
   prose.
5. Introduce a small deterministic reference model before adding complex cache
   policies.
6. Exercise the core through a real CLI workload before attaching it to FSKit.
7. Attack readiness claims with independent adversarial reviews.
8. Cross the platform boundary only after core correctness issues are either
   fixed or explicitly isolated.

The main flaw was not the architecture. It was evidence sequencing. Early
phases combined too much code in one change and treated passing component tests
as evidence for crash safety, restart correctness, bounded I/O, and mount
readiness. Adversarial review later found that those properties had not actually
been tested.

The improved method is therefore:

> Port one semantic slice at a time, preserve the reference invariants, and
> require a failure-oriented executable proof for every strong claim before
> building the next boundary.

## What the work actually did

The committed work was divided into three checkpoints:

| Commit | Scope | Result |
|---|---|---|
| `b720042` | Phases 1–3: Swift package, data model, namespace, workspace, persistence, memory cache, SQLite disk cache | Established the complete portable core and real disk-backed execution path |
| `ac1e185` | Phase 4: LRU/CLOCK/LFU policy support and differential cache oracle | Made policy behavior independently checkable instead of trusting implementation-specific tests |
| `edb207` | Phase 5: `oxdirtest` selection and metrics control plane | Created a human- and script-operable workload surface before FSKit mounting |

The following Session then challenged the claim that “everything up to mount”
was ready. It found restart, integrity, durability, transactionality, bounded
read, concurrency, harness, SDK, signing, and sandbox gaps. Work continued in
the working tree to harden the core, add remote-source behavior, build the FSKit
adapter and Xcode scaffold, and stop at the machine/signing boundary.

## The reusable architecture

Keep three boundaries explicit:

```text
Reference implementation
        |
        | behavior + invariants + conformance scenarios
        v
Portable native core
  model -> namespace -> persistence -> verified cache -> policies
        |
        | narrow API: snapshot, apply, open, bounded read, close, metrics
        v
Platform adapter
  FSKit lifecycle, FSItem mapping, handles, capabilities, mount packaging
        |
        v
Real mounted filesystem
```

The core must not know about FSKit types. The adapter must not reimplement
selection, cache, namespace, integrity, or persistence policy. This made it
possible to compile and test almost all behavior with SwiftPM, without Xcode,
signing, entitlements, extension installation, or a live mount.

### Preserve the reference's load-bearing invariants

The port carried forward these important oxFS properties:

- Reconciliation materializes and verifies content before publishing the new
  namespace.
- Metadata operations do not trigger backend content fetches.
- Namespace snapshots are immutable from the reader's perspective.
- Reconciliation is serialized.
- Stable inode identity survives restart.
- Content is addressed and verified by size and SHA-256.
- Path collisions are handled per entry and reported as status, rather than
  rejecting an entire desired set.
- Cache admission, protection, and eviction are distinct decisions.
- A stale generation cannot replace newer state.
- Files already opened are bound to the exact content object they opened, even
  if the namespace later changes.

These are behavioral contracts, not implementation details. A new
implementation may use different locks, databases, or native APIs, but it
should not change these contracts accidentally.

## The implementation method

### 1. Build a behavioral inventory from the reference

Before writing platform code, map the reference by responsibility:

- value types and validation;
- manifest ordering and collision behavior;
- namespace construction and stable identity;
- selection persistence and generation rules;
- cache admission, materialization, verification, and rollback;
- eviction policy and tie-breaking;
- observation and metrics semantics;
- content-source security rules;
- open-handle and namespace lifetimes;
- control-plane commands and output;
- platform filesystem operations.

For each behavior, record:

| Field | Question |
|---|---|
| Contract | What must callers observe? |
| Invariant | What must never be temporarily exposed? |
| Reference evidence | Which source path and test demonstrate it? |
| Native design | What is the smallest native mechanism that preserves it? |
| Failure proof | Which executable test disproves a broken version? |

Do not translate source file for source file. Translate contracts in dependency
order.

### 2. Establish a platform-neutral core

Start with the smallest native package that can run without the platform
extension:

1. content identity and validation;
2. relative-path validation;
3. manifests and statuses;
4. namespace nodes and stable inode table;
5. in-memory content source and cache;
6. workspace reconciliation;
7. persistence and observation.

Port the corresponding reference tests in the same slice. Keep fixtures small
and deterministic. At this stage, prove namespace and selection semantics
without disk or FSKit complexity.

### 3. Add the real persistence and cache path early

Replace test-only storage with the production-shaped cache before adding many
features:

- SQLite catalog with explicit object states;
- content-addressed object layout;
- streaming hash and size verification;
- temporary file, file sync, atomic rename, and directory sync;
- batch begin/commit/rollback;
- startup recovery of pending work;
- explicit inter-process ownership or locking.

Test every durability boundary with injected failures. A successful rename is
not proof of a durable transaction, and a transaction in SQLite is not a
transaction with a separately published JSON file.

Use an explicit state machine for cross-store work:

```text
write pending intent durably
        |
        v
reconcile cache transaction
   | success              | failure
   v                      v
publish selection      discard intent
   |
   v
clear intent

startup: detect durable intent and roll forward idempotently
```

### 4. Introduce an oracle before policy complexity

The best part of the approach was the differential cache oracle. The production
catalog and a small pure model receive the same operation stream. After every
operation, compare externally meaningful state:

- resident key set;
- resident bytes;
- selected victims and deterministic order;
- admitted/refused objects;
- eviction and blocked-eviction results;
- rollback result.

Keep the oracle intentionally less optimized and structurally different from
the implementation. If it copies the same SQL ordering or data structure, both
can share the same bug.

Use seeded randomized traces in addition to named scenarios, and print the seed
and minimized operation prefix on failure.

### 5. Add an executable control plane before the platform adapter

The `oxdirtest` step was valuable because it converted library behavior into a
real workflow:

```text
source tree -> select -> manifest -> reconcile -> namespace/cache -> metrics
                                      |
                                   restart
                                      |
                         restore and continue generation
```

The harness should:

- run as a subprocess, not only through in-process calls;
- persist state and restart in a second process;
- report whether an apply was actually accepted;
- expose selected, available, stopped, resident, evicted, and fetched state;
- support a deliberately tiny cache to force pressure;
- use machine-readable output or stable semantic fields;
- exit nonzero when its claim is false.

This is the first end-to-end proof of the portable core. It is not proof of a
mounted filesystem.

### 6. Perform a claim-based adversarial gate

Before starting the platform adapter, write the proposed claim literally:

> The portable implementation is restart-correct, crash-safe, integrity-safe,
> bounded in memory, concurrency-safe, and ready to serve filesystem reads.

Assign a burden of proof to each term. Review code and run tests specifically
designed to falsify it:

- restart with previously persisted selections;
- same-process and cross-process same-size corruption;
- failure of file sync, directory sync, rename, unlink, commit, and selection
  publication;
- crash between cache commit and selection publication;
- two processes opening the same state root;
- concurrent apply, open, read, and eviction;
- large-object reads with a small requested range;
- stale generations and partial failures;
- test-wrapper execution in the oldest supported shell;
- SDK availability, API version floor, entitlements, and signing identity.

Only advance the properties that pass. Keep blockers visible rather than
softening the language.

### 7. Design open-file lifetime before implementing the adapter

Filesystem namespace identity and open-file lifetime are different:

```text
lookup(path) -> item/inode                    no content pin
                    |
                    | open revalidates and binds exact content
                    v
              retained handle                pins cache object

namespace removes path -> future lookup fails
                          existing handle still reads

close/reclaim -> descriptor closes and cache pin releases
```

The core API should therefore offer a retained verified descriptor or equivalent
handle with bounded offset reads. Avoid an API such as `openBytes()` that copies
the whole object and makes the adapter appear simpler while violating extension
memory constraints.

Eviction must exclude objects pinned by open handles. Reads must use the opened
descriptor, not re-resolve the current namespace path.

### 8. Add the platform adapter as a thin translation layer

Map platform concepts onto the proven core:

- resource probe/load -> validate configuration and open workspace;
- activate/deactivate -> volume lifecycle;
- root/lookup/enumerate/attributes -> immutable namespace snapshot;
- open/read/close/reclaim -> retained core handles;
- mutations -> explicit read-only errors;
- capabilities -> advertise only implemented semantics.

Compile against the real installed SDK immediately. Header and Swift overlay
inspection are stronger evidence than remembered API shapes or third-party
examples.

Treat deployment as its own sequence of gates:

1. adapter type-checks;
2. host and embedded extension package builds unsigned;
3. extension metadata and personality are recognized;
4. products sign with the required entitlement;
5. extension installs and enables;
6. resource probes and loads;
7. volume mounts;
8. lookup, enumerate, open, bounded read, and close work through the mount;
9. restart, unmount, and failure recovery work.

Do not collapse “adapter compiles” into “mount ready.”

### 9. Validate sandbox and security assumptions with the real process model

An extension cannot necessarily follow arbitrary paths named by a config file.
Design resource ownership around the platform sandbox:

- give the extension a security-scoped resource it can actually access;
- keep state in a caller-selected safe location or app-group container;
- do not write implementation state into the source tree merely to gain access;
- persist and resolve security-scoped bookmarks where appropriate;
- strip credentials from logged or stored URLs;
- allowlist HTTPS origins;
- reject cross-host redirects and untrusted LFS action URLs;
- validate archive paths, link types, checksums, duplicates, and aggregate size.

Test these rules with hostile inputs, not only valid fixtures.

## What worked well

### Reference-first rather than API-first

The work began from oxFS behavior and only later mapped it to FSKit. That
prevented the native API from redefining cache, selection, and namespace
semantics.

### Pure native core with no platform dependency

Most behavior remained buildable with the ordinary Swift toolchain. This made
tests fast and kept signing and extension lifecycle out of the correctness loop.

### Dependency-ordered phases

Model -> engine -> disk cache -> policy -> control plane -> remote -> FSKit is a
good dependency direction. Later layers exercise earlier ones rather than
forcing them to depend on platform machinery.

### Differential testing

The cache oracle supplied evidence that examples alone could not. It was
especially appropriate for deterministic eviction ordering and all-or-nothing
behavior.

### Real workload surface before mount

`oxdirtest` made selection, pressure, eviction, restart, and metrics observable
without waiting for Apple deployment prerequisites.

### Willingness to stop at a failed gate

The adversarial review rejected the readiness claim and did not immediately
start FSKit work. That pause exposed real correctness bugs and improved the
foundation.

### Empirical SDK and platform investigation

The implementation checked installed headers and overlays, discovered that the
chosen path-resource API raises the honest deployment floor to macOS 26, and
found sandbox and Xcode-installation constraints before claiming a mount.

## What should be changed next time

| Problem observed | Why it was risky | Better workaround |
|---|---|---|
| Phases 1–3 arrived as one 2,748-line commit | Model, engine, cache, persistence, and recovery failures were hard to isolate or review | Commit by semantic slice: model; namespace; reconciliation; persistence; disk materialization; recovery |
| “Crash-safe” was claimed before every sync/unlink error propagated | The label was stronger than the executable evidence | Maintain a durability checklist and inject failure at every syscall boundary before using the term |
| Component tests passed while restart state diverged | In-process objects hid process initialization errors | Require a two-process restart test as soon as persistence exists |
| Cache commit and selection publication were separate | A crash could leave durable content state and desired state inconsistent | Use a durable intent journal with idempotent startup roll-forward, or one transactional store |
| Integrity checks were cached too aggressively | Same-size corruption could be served after an earlier validation | Reverify at every data-open trust boundary, or retain a verified descriptor whose identity cannot change |
| Whole objects were copied into memory on open | Small FSKit reads could allocate object-sized memory | Define bounded descriptor reads in the core before adapter work |
| Concurrency and multi-instance behavior were deferred | Single-process green tests did not establish ownership or coalescing | Add root locking, per-key fetch gates, and concurrent traces during the disk-cache phase |
| Metrics mixed attempts and completed operations | Diagnostics could imply durability that had not occurred | Name attempt/success/failure counters separately and increment success only after the boundary completes |
| Test wrapper itself was not part of early validation | `swift test` passed while `make test` failed on system Bash | Run the public developer command in CI from the first phase, using the oldest supported shell/toolchain |
| Platform version assumptions came from base FSKit availability | The actual chosen resource type required a newer OS | Compile a minimal spike against the exact resource/API shape before publishing the product floor |
| Signing and Xcode setup were discovered late | Packaging progress stopped on machine prerequisites | Run a day-zero environment probe: SDK, first-launch state, identity, entitlement, extension support |
| Config-file paths were assumed to grant extension access | The sandbox does not inherit access to arbitrary referenced paths | Design and test the security-scoped resource/config arrangement before the mount helper |
| Remote transport initially buffered responses | Large LFS/archive objects could violate memory goals | Make streaming and aggregate limits part of the `ContentSource` contract from its first implementation |
| Status documentation mixed completed, tested, and planned claims | Readers could infer stronger readiness than existed | Use an evidence matrix with separate Implemented, Component-tested, Process-tested, Mounted, and Blocked columns |
| A large dirty worktree spanned Rust, Swift, harness, and packaging | Review and rollback boundaries became unclear | Use short-lived semantic checkpoints; keep cross-language parity changes in paired but independently reviewable commits |

## Improved phase and gate plan

| Phase | Deliverable | Required gate before advancing |
|---|---|---|
| 0 | Behavioral inventory and minimal platform spike | Reference contracts mapped; exact SDK API compiles; environment blockers listed |
| 1 | Value model and validation | Ported table tests and malformed-input tests pass |
| 2 | Namespace, stable identity, reconciliation | Differential namespace scenarios; stale generation and collision tests pass |
| 3 | Persistence and restart | Two-process restore/advance test passes |
| 4 | Verified disk cache and recovery | Syscall/commit fault injection, corruption, inter-process ownership, and crash-window tests pass |
| 5 | Admission and eviction policies | Pure-model differential traces and seeded randomized tests pass |
| 6 | Control plane | Real subprocess selection -> restart -> pressure -> metrics workflow passes |
| 7 | Remote sources | Streaming, origin/auth, LFS, archive, traversal, and aggregate-limit tests pass |
| 8 | Core serving API | Retained descriptor, bounded read, pinning, stale item, and concurrent eviction tests pass |
| 9 | Platform adapter | Exact SDK compile plus operation-level adapter tests pass |
| 10 | Packaging and mount | Signed install, enable, mount, browse, restart, and clean unmount pass |
| 11 | Cross-implementation parity | Same workload and normalized semantic metrics agree on both implementations |

## Evidence matrix

Track readiness per capability instead of with one phase-level status:

| Capability | Implemented | Unit/model tested | Process tested | Live mount tested | Known blocker |
|---|---:|---:|---:|---:|---|
| Selection/reconciliation | yes/no | yes/no | yes/no | n/a | text |
| Restart recovery | yes/no | yes/no | yes/no | yes/no | text |
| Content integrity | yes/no | yes/no | yes/no | yes/no | text |
| Crash consistency | yes/no | yes/no | yes/no | yes/no | text |
| Bounded reads | yes/no | yes/no | yes/no | yes/no | text |
| Concurrent access | yes/no | yes/no | yes/no | yes/no | text |
| Eviction parity | yes/no | yes/no | yes/no | yes/no | text |
| Remote security | yes/no | yes/no | yes/no | yes/no | text |
| Platform lifecycle | yes/no | yes/no | yes/no | yes/no | text |

This prevents “tests pass” from silently meaning “the product mounts and is
safe under failure.”

## Practical rules for the next implementation

1. Use the Rust implementation as an oracle for behavior, not as a template for
   syntax or internal structure.
2. Write the failure test before using words such as atomic, durable,
   crash-safe, verified, bounded, concurrent, or mount-ready.
3. Preserve a narrow platform-neutral core API.
4. Port tests at the same time as each contract.
5. Add process boundaries as soon as state becomes persistent.
6. Add the differential model before optimizing or adding policy variants.
7. Make open-handle lifetime and bounded reads core concepts, not adapter fixes.
8. Test the public build/test command, not only the underlying compiler command.
9. Probe SDK, OS, sandbox, signing, and entitlement constraints on day zero.
10. Keep claims evidence-scoped and stop at the first boundary that has not been
    demonstrated.

## Source trail

This reconstruction is based on:

- Git commits `b720042`, `ac1e185`, and `edb207` from July 16, 2026.
- The July 16–17 SageOx Session **Hardening oxFS Through the FSKit Boundary**
  (`OxnFVT`), including its three adversarial reviews and subsequent hardening.
- The implementation and current handoff notes under `macos/oxfs-fskit/`.

The commit history shows the intended phase structure. The Session is the more
important source for weaknesses because it records which readiness claims failed
when tested and how the design changed in response.
