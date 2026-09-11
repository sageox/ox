# oxFS macOS walking skeleton

This crate is the first macOS-only oxFS vertical slice. It is an unprivileged
Rust NFSv3 service with an EdenFS-shaped core: immutable namespace generations,
stable inodes, a storage-neutral object source, a verified materialization
cache, and a thin operating-system protocol adapter.

The core keeps intent, policy, storage, and presentation separate:

```text
Desired manifests       What Insight wants
        ↓
Admission + eviction    What fits and wins priority
        ↓
Resident cache          What complete objects exist locally
        ↓
Visible namespace       What users can read right now
```

Manifest membership is not a cache pin. Desired content may be nonresident when
capacity policy prefers other content; only complete resident objects appear in
the namespace, while `.sageox/INDEX` explains stopped entries. In-flight fetches
are reserved. Already-open files remain readable through their OS file descriptor
if their cache pathname is later evicted.

The service itself remains rootless. Its mount command asks `sudo` to run only
macOS's built-in `mount_nfs`/`umount` tools at the privilege boundary.

## Semantic boundary

oxFS borrows EdenFS's architecture, not its hydration behavior.

- EdenFS exposes a broad source tree and can fetch content when a file is used.
- oxFS is WYSIWYG. A selected path remains absent until its entire object has
  been fetched, size-checked, SHA-256 checked, fsynced, and atomically renamed.
- NFS `LOOKUP`, `READDIR`, and `READ` only see resident local files. They never
  start or wait for network work.
- The mount is read-only. All NFSv3 mutation procedures return `NFS3ERR_ROFS`.

```text
Manifest generation
       │
       ▼
validate paths ──► fetch immutable objects ──► verify + fsync + rename
                                                    │
                                                    ▼
                                        build namespace off to the side
                                                    │
                                                    ▼
                                            atomic Arc snapshot swap
                                                    │
                                                    ▼
                                         NFSv3 serves local reads only
```

## Package map

| Module | Responsibility |
|---|---|
| `workspace` | Per-Session generations, reconciliation, immutable snapshot publication |
| `namespace` | In-memory inode tree independent of NFS and process connections |
| `inode` | Append-only stable path-to-inode persistence |
| `content` | Tenant-scoped immutable content identity and storage-neutral fetch trait |
| `cache` | Tenant-separated loose objects, SQLite catalog, verification, crash-safe publication |
| `observations` | Append-only open/read/dismiss JSONL |
| `selections` | Transactional persistence of complete per-Session generations |
| synthetic index | Always-resident `.sageox/INDEX.md` and `.sageox/INDEX.json` with all selectors |
| `nfs` | XDR, ONC RPC record marking, mountd v3, and read-only NFSv3 |

The cache catalog uses bundled SQLite through the MIT-licensed `rusqlite`
binding. SQLite replaces the former rewritten text index: startup opens the
catalog directly, each manifest is one transaction, and NFS reads perform no
durable recency write. An apply-scoped recovery journal bounds crash cleanup to
the keys in the interrupted manifest rather than scanning the object tree.

## Mount on macOS

```bash
cargo build -p oxfs
target/debug/oxfsd mount-demo /tmp/oxfs-state /tmp/oxfs-source /tmp/oxfs
cat /tmp/oxfs/hello.txt
target/debug/oxfsd unmount /tmp/oxfs-state
```

The daemon listens only on a dynamic localhost port. `mount_nfs` receives that
same explicit NFS and mountd port, so oxFS does not need `rpcbind`. If mounting
fails, the child server is stopped and its runtime state is removed.

## Run without mounting

```bash
cargo run -p oxfs -- serve-demo /tmp/oxfs-state /tmp/oxfs-source
```

The process prints the selected localhost port and export (`/oxfs`). The demo
publishes `hello.txt` without invoking a mount command.

## Validation

```bash
cargo fmt --all --check
cargo clippy -p oxfs --all-targets -- -D warnings
cargo test -p oxfs
```

The mount-required end-to-end release gate runs on macOS and refuses to prompt
for elevation. Its runner must allow non-interactive `sudo` only for the system
`mount_nfs` and `umount` commands:

```bash
cargo run -p oxjtest -- --artifact oxjtest-results.json --oracle-probability 0.1
```

`oxjtest` creates a deterministic million-file virtual corpus, materializing
only selected objects from a weighted tiny-file/media size distribution. It
mutates per-Session working sets under concurrent reads through a real NFS
mount, injects a partial fetch, forces LRU eviction, and verifies restart
healing. Correctness, capacity, and
mount cleanup are gating; materialization latency percentiles are recorded in
the JSON artifact but are not gating in E1. Use `--seed`, `--generations`,
`--readers`, `--reads-per-reader`, and `--source-delay-ms` to reproduce or scale
a run. A non-macOS host or unavailable passwordless mount privilege is a hard
failure, not a skipped test.

### Interactive mount (`serve`)

`gate` owns its mount for the duration of one automated run and unmounts on the
way out, so there is no window to inspect the live filesystem by hand. The
`serve` subcommand fills that gap: it generates a working set of manifests,
mounts it at a mountpoint you choose, and evolves it in place until you press
Ctrl-C.

```bash
sudo -v    # cache mount privilege; serve uses non-interactive `sudo -n`
cargo run -p oxjtest -- serve \
  --mountpoint /tmp/oxjfs \
  --seed 1 \
  --files 1000 \
  --churn 10 \
  --evolve-forever
```

Each generation applies a manifest for a synthetic working set of `--files`
files laid out as `shelf-NN/obj-NNNNNN.bin`, drawn from the same weighted
tiny-file/media size distribution as `gate`. Every `--interval-ms` (default
1000) a `--churn` percentage of the set turns over — that many files are
removed and that many fresh files are added — so the mounted tree visibly
evolves while staying near `--files` in size. Content is immutable
(content-addressed, write-once, matching ox-fs); files are never rewritten in
place, only added and removed. Cache capacity is derived from `--files`
(override with `--cache-bytes`); if a generation exceeds it, the eager
capacity gate reports `stopped > 0` in the stats and hides the overflow.

Each generation emits one stats record. **In an interactive terminal** it is a
human-readable table (the header reprints every 20 rows); **when stdout is
piped** it is a single-line `oxjtest.serve.v3` JSON object instead, so
`| jq` keeps working.

```
  GEN   FILES    BYTES  ADD  RMV EVICT  BLK STOP  FETCH   MAT_MS         CACHE
    1    1000    35.2M 1000    0     0    0    0   1000     84.2   35.2M/256.0M
    2    1000    35.1M  100  100     0    0    0    100     12.7   70.1M/256.0M
    3    1000    35.3M  100  100    73    0    0    100     11.9  105.0M/256.0M
```

| Column   | Meaning |
|----------|---------|
| `GEN`    | Generation number (one manifest applied per row). |
| `FILES`  | Files in the working set right now (held near `--files`). |
| `BYTES`  | Sum of the working set's file sizes. |
| `ADD`    | Files that **entered** this generation. |
| `RMV`    | Files that **left** this generation. |
| `EVICT`  | Cache objects **unlinked** to make room this generation (churned-out content reclaimed once the cache fills). |
| `BLK`    | `evict_blocked`: times a reservation needed space but no resident object was evictable, typically because capacity was reserved by pending transfers. |
| `STOP`   | Entries the eager capacity gate **refused to admit** this generation (they stay invisible). |
| `FETCH`  | Origin fetches this generation (new bytes pulled from the source). |
| `MAT_MS` | Wall-clock milliseconds to fetch + admit this generation's new content. |
| `CACHE`  | Cache used / capacity. |

The JSON form carries the same fields plus `ts_unix`, `applied`, and cumulative
`source_fetches`:

```json
{"schema":"oxjtest.serve.v3","ts_unix":...,"generation":3,"mountpoint":"/tmp/oxjfs",
 "files":1000,"total_bytes":37025792,"added":100,"removed":100,
 "evicted":73,"evicted_bytes":4194304,"evict_blocked":0,"stopped":0,
 "applied":true,"materialize_us":11912,"fetched":100,"fetched_bytes":3702579,
 "refetched":2,"refetched_bytes":8192,"catalog_lookups":3300,
 "resident_hits":3000,"resident_misses":300,
 "opened_objects":1000,"opened_bytes":37025792,"catalog_transactions":1,
 "catalog_commit_us":420,"object_sync_us":9100,
 "directory_syncs":1,"directory_sync_us":1600,"source_fetches":1200,"resident_objects":1000,
 "resident_bytes":109821952,"pending_objects":0,"catalog_bytes":262144,
 "wal_bytes":131072,"cache_capacity":268435456,"cache_remaining":158613504}
```

Manifest admission remains deterministic: capacity is reserved in manifest
order, then up to eight independent immutable objects are fetched, verified,
and synced concurrently before the namespace is published. `object_sync_us`
is therefore aggregate worker time and can exceed `materialize_us`;
`directory_syncs` counts coalesced directory durability barriers (normally one
per generation for a single tenant).

To see **every** eviction (victim key, size, LRU access, occupancy) rather than
just the per-generation count, set `OXFS_CACHE_LOG=1` — the cache then emits a
`level=INFO action=evict …` line per unlink on stderr. `action=evict_blocked`
WARN lines are always emitted regardless, since they are rare and important. To
force eviction pressure (and watch `EVICT`/`BLK`/`STOP` climb), shrink the cache
with `--cache-bytes`.

Pass `--generations N` to stop after N generations instead of `--evolve-forever`,
and `--source-delay-ms` to simulate a slow origin. From another terminal, watch
the working set evolve:

```bash
find /tmp/oxjfs -type f | wc -l
du -sh /tmp/oxjfs
cat /tmp/oxjfs/.sageox/INDEX.json | jq '.entries | length'
while true; do find /tmp/oxjfs -type f | wc -l; sleep 1; done
```

Ctrl-C (or SIGTERM) unmounts, shuts down the server, and removes the temporary
workspace. Like `gate`, `serve` requires macOS and non-interactive `sudo` for
`mount_nfs`/`umount`.

### Human-selected local directories (`oxdirtest`)

`oxdirtest` mounts complete directory selections from a large local tree while
using the real oxFS hashing, verified cache, manifest, and NFS paths. It walks
only the selected directories. `select` is additive: each command adds backing
directories to the desired set. Files that win admission become visible only
after hashing and complete verified materialization; capacity-stopped files stay
absent and are explained in `.sageox/INDEX`. Symlinks and special files are
skipped.

```bash
sudo -v
cargo run -p oxfs --bin oxdirtest -- \
  --source /path/to/large-tree \
  --mountpoint /tmp/oxdirtest \
  --cache-bytes 10737418240
```

Commands are read from standard input:

```text
select projects/foo docs/reference
select projects/bar
status
metrics
clear
quit
```

Selections are relative to `--source`; absolute paths and `..` are rejected.
Repeated `select` commands grow the mounted set; selecting an already-selected
directory is a no-op. `clear` is an explicit test reset. The cache defaults to
10 GiB. An expanded selection whose unique content is larger than the cache is
rejected without changing the mount. Use `--state DIR` to retain the verified
cache between runs; otherwise temporary state is removed during clean shutdown.
Directory names containing whitespace are not supported by the interactive
command parser.

#### Remote SageOx repositories

Remote mode discovers repository origins from the canonical SageOx XDG store,
but never reads content from or mutates those checkouts. Ordinary files come
from GitLab's authenticated raw/archive routes at remote `HEAD`; Git LFS
pointers are hydrated through the repository's LFS Batch API. `--git-host` and
`--source` are mutually exclusive.

```bash
cd /Users/port8080/src/sageox/oxfs-v1
set -euo pipefail

export MOUNT=/tmp/oxdirtest-mount
export STATE=/tmp/oxdirtest-state
export LOG=/tmp/oxdirtest.log
export TEST_DATA_ROOT="$HOME/.local/share/sageox/test.sageox.ai"

ox login --endpoint https://test.sageox.ai
printf 'protocol=https\nhost=git.test.sageox.ai\n\n' \
  | ox git-credential-helper get \
  | grep -q '^password='

test -f "$TEST_DATA_ROOT/teams/team_jihjpfkt8b/.git/config"
test -f "$TEST_DATA_ROOT/teams/team_xlcr6yzpec/.git/config"
test -f "$TEST_DATA_ROOT/ledgers/repo_019c5812-01e9-7b7d-b5b1-321c471c9777/.git/config"

sudo -v
if mount | grep -F " on $MOUNT " >/dev/null; then
  sudo -n /sbin/umount "$MOUNT"
fi
rm -rf "$MOUNT" "$STATE" "$LOG"
mkdir -p "$MOUNT" "$STATE"

cargo build -p oxfs --bin oxdirtest
sudo -v
OXFS_CACHE_LOG=1 target/debug/oxdirtest \
  --git-host git.test.sageox.ai \
  --mountpoint "$MOUNT" \
  --state "$STATE" \
  --cache-bytes 10737418240 \
  2>&1 | tee "$LOG"
```

Run the binary as the normal user, never with `sudo`. At the prompt:

```text
repos
select teams/team_jihjpfkt8b/SOUL.md
select teams/team_jihjpfkt8b/docs
select ledgers/repo_019c5812-01e9-7b7d-b5b1-321c471c9777/sessions
select teams/team_xlcr6yzpec/MEMORY.md
status
metrics
quit
```

If graceful cleanup fails, run `sudo -n /sbin/umount "$MOUNT"`. After
inspecting logs and state, remove them with
`rm -rf "$MOUNT" "$STATE" "$LOG"`.

#### Manual observability walkthrough

Keep a named state directory so another terminal can follow oxFS activity.
`OXFS_CACHE_LOG=1` enables one line per evicted cache object; blocked evictions
are always logged.

```bash
cargo build -p oxfs --bin oxdirtest
sudo -v
mkdir -p /tmp/oxdirtest-mount /tmp/oxdirtest-state
OXFS_CACHE_LOG=1 target/debug/oxdirtest \
  --source "$HOME/src" \
  --mountpoint /tmp/oxdirtest-mount \
  --state /tmp/oxdirtest-state \
  --cache-bytes 268435456 \
  2>&1 | tee /tmp/oxdirtest.log
```

In a second terminal, follow file activity through the mounted NFS filesystem.
The JSONL records contain `open`, ranged `read`, and `dismiss` observations with
path, inode, byte offset, and byte count.

```bash
tail -F /tmp/oxdirtest-state/workspace/state/access.jsonl | jq -c .
```

Drive one action at a time from the `oxdirtest>` prompt and compare cumulative
snapshots:

```text
metrics
select sageox/oxfs-v1/crates/oxfs
select sageox/oxfs-v1/docs
metrics
```

Then read through the mount:

```bash
head -c 4096 /tmp/oxdirtest-mount/sageox/oxfs-v1/crates/oxfs/README.md >/dev/null
find /tmp/oxdirtest-mount -maxdepth 6 -type f | head
```

Run `metrics` again. A cold selection increases `resident_misses`,
`fetched_objects`, `fetched_bytes`, and the durability timings. Re-selecting the
same directory primarily increases `resident_hits` without increasing
`fetched_objects`. Adding directories with a deliberately small cache can emit
`action=evict` and increase `evicted_objects`. The desired set may exceed the
cache: older resident files disappear from the namespace as newer content wins
admission, while `.sageox/INDEX` records desired paths that are stopped.

`metrics` reports cumulative process-lifetime counters plus current
cache/catalog gauges. Subtract two snapshots to attribute changes to the action
between them. There are currently no per-NFS-RPC counters: mounted activity is
observed at the workspace `open`/`read`/`dismiss` boundary instead.

#### SageOx trace pipe

oxFS owns its OpenTelemetry spans and sends them to the authenticated SageOx
OTLP proxy; it never sends directly to Honeycomb and never accepts Honeycomb
credentials. `oxdirtest` activates the pipe for manual runs using the canonical
SageOx environment inputs:

```bash
export SAGEOX_ENDPOINT=https://api.sageox.ai # optional; this is the default
export SAGEOX_TOKEN=...                      # tracing is off when unset
target/debug/oxdirtest \
  --source "$HOME/src" \
  --mountpoint /tmp/oxdirtest-mount \
  --state /tmp/oxdirtest-state \
  --cache-bytes 268435456
```

The pipe posts OTLP protobuf to
`$SAGEOX_ENDPOINT/api/v1/otlp/v1/traces` with SageOx bearer authentication, a
two-second export timeout, and a shutdown flush. Initialization and export
failures never prevent the filesystem from running. For now it emits only the
`oxfs.session` root span; behavior-specific child spans belong inside oxFS and
will be added after the manual walkthrough identifies useful boundaries.

The integration suite sends real ONC RPC records over TCP. It does not bypass
the wire adapter. Coverage includes mountd `MNT`, hierarchical `LOOKUP`, ranged
`READ`, and a mutation returning `NFS3ERR_ROFS`. Core tests cover unsafe paths,
stale working-set generations, same-size cache corruption after restart, stable
inode reuse, failed verification remaining invisible, and concurrent fetch
coalescing.

## Grounding against `nfsserve` (2024)

After implementing the slice, it was compared with
`/Users/port8080/oss/2026/h1/nfsserve` as requested. The useful validation
findings incorporated here are:

- ONC RPC over TCP must accept records split across multiple record fragments.
- macOS uses `READDIRPLUS`; response size must respect both `dircount` and
  `maxcount`, and truncation must clear `eof`.
- Kernel clients probe `FSINFO`, `FSSTAT`, `PATHCONF`, `ACCESS`, and `READLINK`
  in addition to the obvious lookup/read path.
- Supplying explicit `port` and `mountport` avoids requiring a portmapper.
- Mountd can remain stateless. `UMNT` is idempotent.
- macOS directory enumeration is tolerant of a zero/stable cookie verifier and
  reacts poorly to aggressive `NFS3ERR_BAD_COOKIE` responses.
- Opaque file handles need a server-owned format rather than exposing raw inode
  bytes. oxFS uses a format marker plus its persisted stable inode.

The implementation is not copied from or linked to `nfsserve`; that project is
used only as validation prior art.

## Intentionally incomplete

- No GitLab-LFS HTTP adapter yet; tests and the demo use local sources.
- No physical free-space watermark yet.
- No RPC authentication policy beyond loopback-only listening and mount auth
  flavor advertisement.
- No graceful connection takeover across daemon restart.

Those omissions are explicit so “NFS server responds” is not confused with the
complete 40-hour oxFS walking skeleton.
