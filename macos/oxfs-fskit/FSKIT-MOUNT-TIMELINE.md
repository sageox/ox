# FSKit first-mount attempt — timeline

**Date:** 2026-07-16, ~17:40–18:15
**Goal:** Mount `~/src/sageox/git-lfs-lore` through the FSKit extension and browse it — the first
crossing of the FSKit callback boundary. Then diff it against the NFSv3 mount.
**Outcome:** Never mounted. The System Settings toggle that enables the FSKit module cannot be
clicked. Root cause still unknown.

---

## What worked

| Step | Result |
|---|---|
| Persist selection (`oxdirtest --source git-lfs-lore --state .oxfs-state`) | 351 files, 8.28 MB, `generation=1`, zero evictions |
| Extension built + signed (`make xcode-build`) | `TeamIdentifier=C2LRSF5CK3`, `com.apple.developer.fskit.fsmodule=true`, valid provisioning profile |
| Module registered | visible in `pluginkit` and in System Settings |
| Source repo | untouched, `git status` clean throughout |

Everything up to the mount works. The blocker is a single UI toggle.

---

## Timeline

**17:40 — Setup.** Mountpoint moved from `/Volumes` to `/tmp` (`/Volumes` is root-owned).
Discovered `oxfsmount` writes `.oxfs-state/` and `.oxfs-mount.json` *into* the source tree.
git-lfs-lore is a **worktree** of sageox-monorepo, and per-worktree `info/exclude` is not
honored — excludes had to go in the common gitdir. Filed **ox-x5iu** (cache should live outside
`--source`, like the Rust impl's `--state`).

**17:53 — First mount attempt.** `Module ai.sageox.oxfs.extension is disabled!` The extension is
registered but not enabled. Enabling requires a manual toggle in System Settings → General →
Login Items & Extensions.

**17:54–18:15 — The toggle will not move.** Five hypotheses, each investigated and each wrong:

1. **Ad-hoc signing.** Bundles were `Signature=adhoc`, `TeamIdentifier=not set`. Cause: `make
   xcode-build-unsigned` (`CODE_SIGNING_ALLOWED=NO`) had overwritten the signed build at 17:54 —
   both targets write to the same DerivedData path, and `pluginkit` shows `+` either way.
   Rebuilt signed. **Toggle still dead** — and it had already been dead at 17:45 while correctly
   signed, so this was never the cause.
2. **DerivedData location.** Copied app to `/Applications`, re-registered. **Still dead.** This
   *created a duplicate registration* — four LaunchServices paths for two bundle IDs. Ajit spotted
   it in the UI ("there are 2 oxfshost fskit modules"); `pluginkit` only ever showed one.
   Unregistered the stale copies. **Still dead.**
3. **Stale `fskit_agent`.** Found the agent was 2.5 days old. An earlier `killall` had reported
   success but the process survived — every "try it now" before this was against the same stale
   agent. SIGKILL replaced it (`launchctl kickstart` blocked by SIP). **Still dead.**
4. **Missing `EXExtensionPrincipalClass`.** Apple's msdos module declares one; ours doesn't.
   Disproved by prior art: both Swift samples (FSKitSample, ExtendFS) also omit it — the key is
   ObjC-only, irrelevant to `@main` / `UnaryFileSystemExtension`.
5. **Stale `fskitd`.** pid 208, started 7s after boot, **3 days 19:46 old**. `sudo killall fskitd`
   respawned it fresh (pid 11262). **Still dead.**

**~18:05 — The control experiment (Ajit's idea).** Built KhaosT's **FSKitSample**, re-signed to
team `C2LRSF5CK3`, installed to `/Applications` alongside oxfs. **Its toggle does not move either.**

That settles it: **the bug is not in oxfs.** Signing, Info.plist, `@main`, path-URLs, hardened
runtime — none of it. A known-good Apple-pattern sample fails identically on this machine.

---

## Frustration points

- **Eight rounds of "try the toggle now."** Each required a manual click; each failed. Two were
  against state I'd wrongly reported as fixed (the `killall fskit_agent` that didn't kill, the
  premature "breakthrough").
- **I claimed victory on an empty sample.** After restarting `fskitd` I reported the `errno 2` was
  gone — it was zero only because no click had occurred in that 28-second window. It came back on
  the next click.
- **Nearly rebooted into recovery mode for nothing.** A search result recommended Reduced Security
  + "allow user management of kernel extensions." That's kext advice; FSKit is explicitly
  userspace ("without kernel-level access" — the subtitle in the pane itself). Would have cost two
  reboots and permanently weakened the machine, and changed nothing.
- **`pluginkit` lied at every step** — showed `+` for an ad-hoc bundle, hid the duplicate
  registration, and marked the broken module `+` while the working sample had no flag.
- **The control experiment was available the whole time.** Ajit proposed it; I didn't. It would
  have ruled out hypotheses 1–4 in one move instead of an hour.

---

## Current state

- `errno 2, retrieving team ID` fires on **every** click, from a fresh `fskitd` (11262) and fresh
  `fskit_agent` (11263). Both running.
- Log distinguishes two client types:
  `LoginItems → fskitd: entitled 0 → errno 2` (the toggle) vs `mount → fskitd: entitled 1 → no error`.
- Not MDM-enrolled. Uptime ~4 days. macOS 26.5.2 (25F84).
- Also seen, relevant to the mount later, not the toggle:
  `Sandbox: ScopedBookmarkAgent deny(1) file-read-data …/.build/…`

## Next steps

1. **Reboot** (normal — not recovery, not Reduced Security). Cheapest untried lever; ~4 days
   uptime and several daemons killed mid-flight.
2. If the toggle works after reboot, retest **FSKitSample first** as the control.
3. If it still fails with a stock sample on a fresh boot, it's an OS/Apple issue — file feedback.

## Cleanup owed

`/Applications/FSKitExp.app`, `/Applications/OxfsHost.app`, `OxfsHost.app.stale` in DerivedData,
the running `log stream`, `/tmp/sageox`, and the gitdir excludes in `sageox-monorepo/.git/info/exclude`.

## Bugs worth keeping regardless

- **ox-x5iu** — `oxfsmount` hardcodes cache/state inside `--source`.
- **Unfiled** — `make xcode-build-unsigned` silently clobbers `make xcode-build` (same DerivedData
  path, strips Team ID, no warning). Worth a signature preflight in `oxfsmount`.
