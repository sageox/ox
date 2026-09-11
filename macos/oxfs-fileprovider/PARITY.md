# File Provider parity ledger

The target is the union of behavior in the NFSv3 and FSKit implementations.
File Provider is a third presentation boundary over the same engine, not a
reduced product.

| Capability | Shared core | File Provider boundary | Live evidence |
|---|---:|---:|---:|
| Manifest validation and generations | yes | yes | yes |
| Per-entry path-collision status | yes | yes | via synthesized index |
| Immutable namespace snapshots | yes | yes | yes |
| Stable inode identity | yes | identifier mapping | restart + live-delta E2E |
| Eager verified materialization | yes | yes | SHA-256-backed E2E read |
| Bounded verified reads | yes | 64 KiB fetch loop | yes |
| Open descriptor cache pins | yes | retained during fetch | yes |
| Disk catalog and crash recovery | yes | shared app-group state | `fileproviderd` restart E2E; fault matrix pending |
| LRS-generation, CLOCK, and sampled-LFU | yes | host cache controls | core oracle; FP pressure E2E pending |
| Differential cache oracle | yes | no adapter-specific work needed | core tests |
| Selection control plane | yes | folder choice + staged activation | arbitrary-folder + no-reset refresh E2E |
| Synthetic `.sageox/INDEX` | yes | enumerated and fetched | yes |
| Local content source | yes | host-authorized atomic staging | arbitrary-folder + restart E2E |
| Remote raw/LFS/archive source | yes | local registry + ox credential helper | raw exact-byte E2E; LFS/archive unit gates |
| Cache and operation telemetry | yes | host display + app-group JSON | live generations/fetch bytes |
| Working-set changes | n/a | generation anchors + update/delete | add/modify/delete E2E |
| Concurrent fetch coalescing | disk cache yes | pipeline depth 4 | 16-way single-fetch stress test |
| Read-only semantics | yes | capabilities + mutation rejection | build verified |
| Finder enumeration | n/a | replicated enumerator | yes |
| Finder materialization | n/a | `fetchContents` | exact bytes verified |
| Domain install/remove | n/a | host app | yes |
| Extension sandbox/signing | n/a | app group + Apple Development | yes |

## Current end-to-end proof

On July 17, 2026, a signed development build:

1. installed and enabled `ai.sageox.oxfs.fileprovider.extension`;
2. registered domain `oxfs`;
3. appeared at
   `~/Library/CloudStorage/OxfsFileProviderDev-oxFS—e2e-1784320609`;
4. enumerated nested directories, normal files, and synthesized index files;
5. materialized `hello.txt` and `docs/nested/readme.md`;
6. produced bytes exactly matching the source fixtures; and
7. retained a live extension process after both reads.

The arbitrary-folder and live-change gates subsequently proved:

1. a folder selected through `NSOpenPanel` is staged into an app-group
   `OxfsCore` workspace, avoiding non-portable host-to-extension bookmarks;
2. the domain remains readable after terminating `fileproviderd`;
3. add, modify, and delete refreshes advance generations without replacing the
   domain;
4. a changed materialized file is evicted and fetched again with exact bytes;
5. deleted identifiers disappear through `enumerateChanges`; and
6. an already-staged workspace can be reactivated without renewed source
   access.

The remote boundary subsequently proved:

1. the nonsandboxed host discovers synced local SageOx repositories and their
   checked-out branch without performing a Git checkout;
2. credentials come from `ox git-credential-helper get` and remain in memory;
3. GitLab REST requests use `PRIVATE-TOKEN`, while LFS retains HTTP Basic;
4. a tracked remote file materializes through Finder with bytes identical to
   the local checkout's `HEAD` object;
5. neither configuration nor the shared workspace contains the credential; and
6. the materialized remote file remains readable after terminating both the
   extension and `fileproviderd`.

## Next executable gates

1. Run live-network LFS and archive selections through Finder.
2. Run concurrent Finder reads under a deliberately small cache and compare
   residency/eviction state with the oracle.
3. Kill the extension and fileproviderd at each persistence boundary, then
   verify restart correctness.
4. Replace the current refresh-wide update set with per-anchor durable deltas
   so lagging observers can replay more than one generation.
5. Remove the remaining global disk-cache fetch serialization while retaining
   the proven per-object coalescing invariant.
