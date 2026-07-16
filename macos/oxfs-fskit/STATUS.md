# oxfs-fskit — build status & handoff

Standalone **pure-Swift** reimplementation of the oxFS context filesystem on
macOS FSKit, targeting **100% parity** with the Rust `crates/oxfs` reference
(zero Rust in the shipped product). Plan:
`~/.claude/plans/fileprovider-macos-12-1-compiled-lighthouse.md`. Epic: `ox-cpdq`.

## How to run

```
cd macos/oxfs-fskit
make build      # swift build (no Xcode needed)
make test       # swift-testing suite (scripts/test.sh handles CLT vs Xcode)
make run        # oxfs-hello smoke binary
```

## Environment (this box)

- macOS **26.5.2** — past the 26.1/26.2 third-party-FSKit breakage.
- **Xcode now installed** (user switched `xcode-select` to it + accepted license).
  So `xcodebuild` is available for the eventual `.appex` build.
- Still **0 codesigning identities** — a signing cert + the
  `com.apple.developer.fskit.fsmodule` entitlement (paid Apple dev team) are
  needed before a real signed extension can mount. Not required for the core.
- `import FSKit` **type-checks from the CLI** — the adapter can be written and
  compiled-checked without an Xcode project.

## Done (Phase 1 + 2 + 3 + **4** + **5 control plane**) — all `make test` green (29 tests)

### Phase 5 — `oxdirtest` select/metrics control plane (WORKING)
`Sources/oxdirtest/main.swift` — Swift port of the Rust `oxdirtest` control
loop. Interactive REPL: `select DIR...`, `clear`, `status`, `metrics`, `help`,
`quit`. Indexes a local directory (`--source`), stream-hashes every file, builds
a manifest, and applies it to a real `Workspace` (disk-backed verified cache),
reporting `available`/`stopped` and full cache metrics — same command names and
output shape as the NFS-v3 harness. The capacity-limited path
(`stopped`/`WARNING`/`evict_blocked`) works. **Not yet wired:** the actual FSKit
mount/browse (Phase 7, needs signing) — everything up to the mount is exercised.
Run: `swift run oxdirtest --source <dir> [--state <dir>] [--cache-bytes N]`.

### Phase 4 — eviction policies + differential oracle (COMPLETE)
- `EvictionOrder` (LRU-by-selection-generation / CLOCK-second-chance / sampled-LFU)
  in the catalog via `insert_seq`/`reference`/`frequency` columns; `touch` wired
  through `openBytes`.
- `CacheModel.swift` — pure Swift reference model (port of `cachesim`).
- `OracleTests.swift` — differential oracle: same op stream through the real
  `DiskContentCache` and the model, asserting resident-key parity every apply,
  across all 3 policies + a distinctness meta-check. **It caught a real bug**:
  `materializeMissingBatch` was finishing each object before reserving the next,
  so a batch-mate could be evicted mid-batch — fixed to reserve-all-then-fetch.

### Phase 1 + 2 + 3 (earlier)

### Phase 3 — disk-backed crash-safe verified cache (COMPLETE)
- **`Catalog.swift`** — SQLite catalog via system `import SQLite3` (no dependency):
  WAL, `cache_meta`/`cache_objects`, pending/resident/evicted states, `reserve`
  with LRU-by-selection-generation eviction, `finish`/`release`/`markMissing`/
  `importResident`, `gauges`, batch `begin/commit/rollback`.
- **`DiskContentCache.swift`** — content-addressed store under
  `cache/objects/<tenant-hash>/<key>`; verified atomic materialization
  (`VerifyingFileWriter`: SHA-256 + size, fsync, atomic rename); journal-based
  batch crash-safety (`active-apply.v1`, fsynced); recovery on open (orphan
  temps, pending cleanup, journal replay, `metadata.v1` migration);
  **same-size-corruption healing** on `resident()`; `directory_syncs` /
  `catalog_transactions` telemetry. Wired as the default cache in `Workspace.open`.
- **New conformance tests** (`DiskCacheTests.swift`): catalog persistence +
  `catalog_transactions==2` + sqlite-file presence, same-size-corruption heals
  across restart, legacy `metadata.v1` migration. Plus all Phase 2 Workspace
  scenarios now run against the **real disk cache** and pass.

### Phase 1 + 2 (earlier)

- Package scaffold, `OxfsCore` lib, `oxfs-hello` exe, swift-testing harness.
- **Data model** (faithful ports, with ported unit tests):
  `ContentRef` (CryptoKit SHA-256, storage key, validation), `RelativePath`,
  `Manifest`/`ManifestEntry` (mode mask, validate, collisions), `Namespace`
  (`Node`/`FileNode`/`Selector`/`Status`), `InodeTable` (append-only, persistent,
  escape/unescape).
- **Engine**: `Workspace` — `apply`/reconcile, `buildNamespace`, per-entry
  path-collision resolution, `.sageox/INDEX.{md,json}` generation, eager
  materialize-then-publish, stable-inode restart, read path with observation log.
- **Cache**: `ContentCache` protocol + `MemoryContentCache` (fetch → verify
  size+SHA-256 → admit; byte cap; LRU eviction; begin/commit/rollback batch;
  `AdmitEverything` ranked admission).
- **Persistence**: `SelectionStore` (JSON, atomic replace), `ObservationLog`
  (JSONL access log), Codable model.
- **Ported conformance scenarios** (the parity oracle) in `WorkspaceTests.swift`.

## Next (in priority order)

1. **Remaining Phase 2/3 parity edges**: fetch coalescing + concurrency
   (`concurrent_sessions_coalesce_one_fetch`, per-key in-flight gate),
   replacement-capacity refusal, ranked-admission-stops, eviction counters,
   injected-commit-failure rollback (the `failNextCommit()` hook exists — add
   the test). Mostly small, now that the disk cache is in place.
2. **Phase 4 — eviction policies + differential oracle**: add CLOCK-second-chance
   and sampled-LFU on top of the default LRU (extend the catalog with
   `insert_seq`/`slot` + a policy layer); port `cachesim` as a Swift oracle
   asserting residency/byte parity every apply.
3. **Phase 5 — control plane**: `apply`/`status`/`metrics` surface (oxdirtest parity).
4. **Phase 6 — remote ContentSource**: GitLab raw/archive + LFS Batch
   (mirror `oxdir_remote.rs`).
5. **Phase 7 — FSKit `.appex` (BLOCKED on signing)**: `OxfsVolume`/`OxfsFileSystem`
   Swift adapter mapping `enumerateDirectory`/`lookupItem`/`read`/`attributes`
   onto the `Workspace` API; reject mutators with `EROFS`; mount `/Volumes/oxfs`.
   Op-shape + signatures already resolved (see FSKit API section above). Prior
   art (copy-safe): `~/oss/2026/h1/fskit/{FSKitSample,FSKitBridge}` (Apache-2.0);
   `ExtendFS` is GPLv3 → **reference only, no code copy**.

## FSKit API ground truth (resolved on THIS SDK: macOS 26.5 / Xcode 26)

Settled the research's #1 footgun empirically from
`.../MacOSX.sdk/.../FSKit.framework/Headers/FSVolume.h`:

- FSKit operation protocols are **Objective-C** (`FSVolumeOperations`,
  `FSVolumeReadWriteOperations`, `FSVolumePathConfOperations`, `FSVolumeOpenClose`/
  `Xattr`/`AccessCheck`/`Rename`Operations). The Swift overlay
  (`.swiftinterface`) is thin (124 lines: `UnaryFileSystemExtension`, buffer I/O).
- **Both op shapes are vended** — no hard footgun here. Use whichever:
  - completion-handler: `read(from:at:length:into:replyHandler:)`,
    `lookupItem(named:inDirectory:replyHandler:)`,
    `enumerateDirectory(_:startingAt:verifier:attributes:packer:replyHandler:)`,
    `getAttributes(_:of:replyHandler:)`, `activate(options:replyHandler:)`,
    `deactivate(options:replyHandler:)`.
  - async: the header states an async Swift impl drops the reply handler and
    just returns the value / throws. (Recommend the **async/throws** shape —
    cleaner for calling into `Workspace`.)
- **Read-only**: no single `readOnly` capability flag. Advertise via
  `FSVolumeSupportedCapabilities` + reject mutators with `EROFS`; mount honors
  `FSMountOptionsReadOnly`. (Matches the ExtendFS pattern.)
- Item identity: `FSItem.Identifier(rawValue: UInt64)` ← our stable inode.
- Virtual (non-block) FS: probe/load an `FSPathURLResource` (Apple passthrough
  sample structure), not `FSBlockDeviceResource`.

## Notes / deferred parity gaps (tracked, not lost)

- Concurrency/coalescing deferred to a per-key in-flight gate (Phase 3/4).
- Cache telemetry is the observable subset; SQLite-specific counters
  (`catalog_transactions`, sqlite file presence) land in Phase 3.
- Precise eviction-policy parity is validated by the Phase 4 oracle.
- **Not committed** — all work is in the working tree pending your review
  (repo rule: confirm before commit/push).
