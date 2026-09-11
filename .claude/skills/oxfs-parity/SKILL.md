---
name: oxfs-parity
description: Differentially compare the Swift FSKit mount against the Rust NFSv3 mount over the same human-selected directory, at the VFS boundary. Verifies namespace, synthetic .sageox/INDEX.json, file content, and POSIX metadata agree between the two implementations. Use when asked to "compare FSKit and NFS", "check mount parity", "diff the two mounts", "does FSKit match NFS", "run the parity harness", or /oxfs-parity. Distinct from the in-process CacheModel oracle in OracleTests.swift, which compares a Swift model to the Swift cache and never crosses a mount.
---

# oxfs mount parity: FSKit vs NFSv3

Mounts both implementations over the same `--source` and the same selection, then
diffs what a POSIX client actually observes. This is the **mount-level** differential
oracle. It is the only check that exercises the FSKit callback boundary, which
compilation, signing, and packaging all pass without validating.

Layers, so you reach for the right one:

| Oracle | Compares | Crosses a mount? |
|---|---|---|
| `OracleTests.swift` (phase 4) | Swift `CacheModel` ↔ Swift `DiskContentCache` | no — in-process |
| **this skill** | Rust NFSv3 mount ↔ Swift FSKit mount | yes — real VFS |

## Run it

```bash
.claude/skills/oxfs-parity/scripts/parity.sh --source /path/to/src --selection some/dir
```

The script is the source of truth. Read it before hand-running the steps; the
manual procedure below exists only to explain *why* each step is shaped as it is.

## The four invariants that make this deterministic

Get any of these wrong and the harness reports a divergence that isn't real.
This is the load-bearing part of the skill.

### 1. Both state dirs must be fresh, and get the same number of `select`s

`generation` is a counter persisted per state dir (`oxdirtest-control.json`,
incremented once per `select`/`clear`) and it is **embedded in INDEX.json**:

```
{"generation":2,"files":[...]}
```

The two sides use separate caller-specified state dirs. A stale state dir on either side, or one
extra `select`, shifts `generation` and every INDEX.json diff goes red for a
reason that has nothing to do with FSKit.

The script wipes both state dirs and issues exactly one `select` per side. It also
emits a generation-normalized diff so you can tell "only generation drifted" apart
from "the working set genuinely differs".

### 2. The Swift `oxdirtest` must exit before `oxfsmount` runs

Not merely because live selection isn't wired — because of a **lock**.
`DiskCache.swift` takes an exclusive `lockf(fd, F_TLOCK, 0)` on `cache.lock` under
the cache root. `oxdirtest --state "$FSKIT_STATE"` and the FSKit extension both
want that same root. Hold it and the extension cannot open the cache.

`test-oxdirtest.sh` already asserts this: a second instance fails with
`Resource temporarily unavailable`. Same lock, different claimant.

### 3. `oxfsmount` exits; the Rust `oxdirtest` must stay alive

They have opposite lifetimes, which is easy to get backwards:

- `oxfsmount` shells out to `/sbin/mount -t oxfs` and **returns**. The mount
  outlives the process. Teardown is `umount "$FSKIT_MOUNT"`.
- Rust `oxdirtest` hosts the NFS server **in-process** and unmounts on `quit`.
  Kill it and the mount dies with it — so it must stay running for the whole
  comparison, fed by a FIFO (the technique `test-oxdirtest.sh` already uses).

## What the oracles catch — and what they miss

The script runs four, because each is blind to something the next one sees:

- **A. Namespace** — `find -type f`, relative, sorted. Catches missing/extra files.
- **B. INDEX.json** — the synthetic file both `Workspace`s render. Catches
  working-set/status/selector drift. **Blind to mode**: INDEX.json carries
  `path,size,source_id,source_kind,status,selectors` — no mode field.
- **C. Content** — `shasum -a 256` over every non-`.sageox` file. Catches
  read/materialization corruption. Blind to metadata.
- **D. Metadata** — `stat` mode + size. The only oracle that sees permissions.

Oracle D exists because it already caught one real bug (ox-v1i9, now fixed). Swift
hardcoded `mode: 0o444` while Rust used `metadata.mode() & 0o555`, so every
executable read `-r-xr-xr-x` on NFS and `-r--r--r--` on FSKit. Both sides now derive
mode from file metadata masked `& 0o555`.

The durable lesson: **oracles A, B, and C were all blind to it.** `find` sees names,
`shasum` sees bytes, and INDEX.json has no mode field at all. Only `stat` saw it.
When adding an oracle, ask what the existing ones structurally cannot observe.

**Put an executable in your fixture.** A selection of plain files cannot fail
oracle D, so a green run over plain files is not evidence that mode is correct.
`scripts/test-oxdirtest.sh` now guards this specific regression at the manifest
level (asserting `"mode":365` — `0o555` — in `<state>/state/selections.json`),
which catches it with no signing and no mount.

## Human gates

Exactly one remains, and it is one-time-per-machine.

**FSKit extension installed + enabled** — approved by hand in System Settings →
General → Login Items & Extensions. If it isn't, `oxfsmount` fails with
`mount failed with status …`. This is macOS's consent model, not an oversight:
it is deliberately not scriptable. Bake it into the machine image or MDM-provision it.

### sudo is NOT a gate — and never prompt for it

`mount.rs` elevates internally with **`sudo -n`** (`mount_command_for(state, true)`,
`unmount_inner(true)`). The doc comment on `mount_server` is explicit: *"without
allowing sudo to prompt. This is the privilege boundary used by end-to-end release
gates."* It cannot prompt; it only succeeds or fails.

So given a NOPASSWD grant:

```
<user> ALL=(root) NOPASSWD: /sbin/mount_nfs, /sbin/umount
```

…the NFS half is already fully unattended. **Do not add `sudo -v` to this harness.**
A typical sudoers file also carries a broad `(ALL) ALL` rule with no NOPASSWD;
`sudo -v` validates the general timestamp against *that* rule and prompts for a
password — becoming the only human touch in an otherwise unattended run. It buys
nothing, because the code path it is "warming up" never consults that timestamp.

The script instead verifies the grant the same non-prompting way the real code
uses it, `sudo -n -l /sbin/mount_nfs`, and fails with the sudoers line to add.
Any `sudo` the harness itself runs must be `-n` and fully-qualified (`/sbin/umount`,
not `umount`) so it matches the grant and can never block a trap.
