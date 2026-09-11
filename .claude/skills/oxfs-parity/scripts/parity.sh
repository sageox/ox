#!/usr/bin/env bash
# oxfs mount parity: Rust NFSv3 mount vs Swift FSKit mount, same source + selection.
# Diffs what a POSIX client observes across four oracles. See ../SKILL.md for why
# each step is shaped this way.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
FSKIT_DIR="$REPO/macos/oxfs-fskit"

SRC="" SELECTION="" CACHE_BYTES=1073741824 KEEP=0
while [[ $# -gt 0 ]]; do
    case "$1" in
        --source)      SRC="$2"; shift 2 ;;
        --selection)   SELECTION="$2"; shift 2 ;;
        --cache-bytes) CACHE_BYTES="$2"; shift 2 ;;
        --keep)        KEEP=1; shift ;;
        *) printf 'usage: parity.sh --source DIR --selection REL_DIR [--cache-bytes N] [--keep]\n' >&2; exit 2 ;;
    esac
done
[[ -n "$SRC" && -n "$SELECTION" ]] || { printf 'parity: --source and --selection are required\n' >&2; exit 2; }

SRC="$(cd "$SRC" && pwd -P)"   # realpath: both indexers canonicalize, so we must too
[[ -d "$SRC" ]] || { printf 'parity: --source is not a directory: %s\n' "$SRC" >&2; exit 2; }

case "$SELECTION" in
    /|""|..*|/*) printf 'parity: --selection must be a relative directory\n' >&2; exit 2 ;;
esac
[[ -d "$SRC/$SELECTION" ]] || { printf 'parity: no such selection: %s/%s\n' "$SRC" "$SELECTION" >&2; exit 2; }

WORK="$(mktemp -d "${TMPDIR:-/tmp}/oxfs-parity.XXXXXX")"
FSKIT_MOUNT="$WORK/fskit" NFS_MOUNT="$WORK/nfs" NFS_STATE="$WORK/nfs-state"
FSKIT_STATE="$WORK/oxfs-fskit-state"
OUT="$WORK/out"
mkdir -p "$FSKIT_MOUNT" "$NFS_MOUNT" "$NFS_STATE" "$OUT"

NFS_PID="" FIFO="$WORK/nfs.fifo"
cleanup() {
    local status=$?
    set +e
    # Invariant 3: opposite lifetimes. NFS unmounts itself on `quit`; FSKit's
    # mount outlived the oxfsmount process and must be umount'ed by us.
    if [[ -n "$NFS_PID" ]] && kill -0 "$NFS_PID" 2>/dev/null; then
        printf 'quit\n' >&3 2>/dev/null
        for _ in $(seq 1 50); do kill -0 "$NFS_PID" 2>/dev/null || break; sleep 0.1; done
        kill -0 "$NFS_PID" 2>/dev/null && kill "$NFS_PID" 2>/dev/null
    fi
    exec 3>&- 2>/dev/null
    mount | grep -qF "$FSKIT_MOUNT" && umount "$FSKIT_MOUNT" 2>/dev/null
    # Fallback only: oxdirtest unmounts itself on `quit`. Fully-qualified + `-n` so it
    # matches the NOPASSWD grant and can never prompt from a trap.
    mount | grep -qF "$NFS_MOUNT" && sudo -n /sbin/umount -f "$NFS_MOUNT" 2>/dev/null
    if [[ $KEEP -eq 1 ]]; then printf '\nartifacts kept: %s\n' "$OUT"; else rm -rf "$WORK"; fi
    exit $status
}
trap cleanup EXIT INT TERM

say() { printf '\n\033[1m== %s\033[0m\n' "$1"; }

# ---- 0. preflight -----------------------------------------------------------
say "preflight"
# mount.rs elevates internally with `sudo -n` (see mount_command_for / unmount_inner),
# so it never prompts — it just fails if NOPASSWD is missing. Verify the grant the
# same non-prompting way. Do NOT use `sudo -v`: that validates the general timestamp
# against the broad `(ALL) ALL` rule, which has no NOPASSWD, and would be the only
# password prompt in the whole run.
for cmd in /sbin/mount_nfs /sbin/umount; do
    sudo -n -l "$cmd" >/dev/null 2>&1 || {
        printf 'parity: missing NOPASSWD sudoers grant for %s\n' "$cmd" >&2
        printf '        oxfs mounts via `sudo -n`, so it cannot prompt. Add:\n' >&2
        printf '        %s ALL=(root) NOPASSWD: /sbin/mount_nfs, /sbin/umount\n' "$(id -un)" >&2
        exit 2
    }
done
( cd "$FSKIT_DIR" && swift build ) >/dev/null || { printf 'parity: swift build failed\n' >&2; exit 1; }
SWIFT_BIN="$(cd "$FSKIT_DIR" && swift build --show-bin-path)"
printf 'source=%s selection=%s\n' "$SRC" "$SELECTION"

# ---- 1. Swift selection (then exit — invariant 2: cache.lock) ---------------
say "swift: persist selection"
# Invariant 1: fresh state, exactly one select, so `generation` lands at 1 on both sides.
rm -rf "$FSKIT_STATE"; mkdir -p "$FSKIT_STATE"
printf 'select %s\nstatus\nmetrics\nquit\n' "$SELECTION" |
    "$SWIFT_BIN/oxdirtest" --source "$SRC" --state "$FSKIT_STATE" --cache-bytes "$CACHE_BYTES" \
    >"$OUT/swift-select.log" 2>&1 || { cat "$OUT/swift-select.log" >&2; exit 1; }
grep -E 'generation=|visible_files=' "$OUT/swift-select.log" | tail -2 || true
# oxdirtest has exited, so cache.lock is free for the extension.

# ---- 2. mount FSKit ---------------------------------------------------------
say "fskit: mount"
# oxfsmount shells to /sbin/mount -t oxfs and returns; the mount outlives it.
"$SWIFT_BIN/oxfsmount" --source "$SRC" --state "$FSKIT_STATE" \
    --mountpoint "$FSKIT_MOUNT" --cache-bytes "$CACHE_BYTES" \
    >"$OUT/fskit-mount.log" 2>&1 || {
    cat "$OUT/fskit-mount.log" >&2
    printf '\nparity: FSKit mount failed. Is the extension installed and enabled in\n' >&2
    printf '        System Settings > General > Login Items & Extensions?\n' >&2
    exit 1
}
cat "$OUT/fskit-mount.log"

# ---- 3. mount NFS, held open on a FIFO (invariant 3) ------------------------
say "nfs: mount + select"
mkfifo "$FIFO"; exec 3<>"$FIFO"
( cd "$REPO" && cargo build -p oxfs --bin oxdirtest ) >"$OUT/cargo-build.log" 2>&1 || {
    cat "$OUT/cargo-build.log" >&2; exit 1; }
"$REPO/target/debug/oxdirtest" --source "$SRC" --mountpoint "$NFS_MOUNT" \
    --state "$NFS_STATE" --cache-bytes "$CACHE_BYTES" <"$FIFO" >"$OUT/nfs.log" 2>&1 &
NFS_PID=$!
for _ in $(seq 1 100); do grep -qF 'commands:' "$OUT/nfs.log" && break; sleep 0.1; done
grep -qF 'commands:' "$OUT/nfs.log" || { cat "$OUT/nfs.log" >&2; printf 'parity: NFS mount never came up\n' >&2; exit 1; }
printf 'select %s\nstatus\nmetrics\n' "$SELECTION" >&3
for _ in $(seq 1 100); do grep -qE 'generation=1 ' "$OUT/nfs.log" && break; sleep 0.1; done
grep -E 'generation=|visible_files=' "$OUT/nfs.log" | tail -2 || true

# ---- 4. oracles -------------------------------------------------------------
FAIL=0
report() { # name, expected-file, actual-file
    if diff -u "$2" "$3" >"$OUT/$1.diff" 2>&1; then
        printf '  \033[32mPASS\033[0m %s\n' "$1"
    else
        printf '  \033[31mFAIL\033[0m %s  -> %s\n' "$1" "$OUT/$1.diff"; sed -n '1,25p' "$OUT/$1.diff"; FAIL=1
    fi
}

say "oracles"

# A. namespace
( cd "$NFS_MOUNT"   && find . -type f | sed 's|^\./||' | sort ) >"$OUT/a-nfs"
( cd "$FSKIT_MOUNT" && find . -type f | sed 's|^\./||' | sort ) >"$OUT/a-fskit"
report namespace "$OUT/a-nfs" "$OUT/a-fskit"

# B. synthetic index — raw, then generation-normalized to separate counter drift
# from a genuine working-set difference.
cp "$NFS_MOUNT/.sageox/INDEX.json"   "$OUT/b-nfs"
cp "$FSKIT_MOUNT/.sageox/INDEX.json" "$OUT/b-fskit"
report index-json "$OUT/b-nfs" "$OUT/b-fskit"
if [[ -s "$OUT/index-json.diff" ]]; then
    sed 's/"generation":[0-9]*/"generation":N/' "$OUT/b-nfs"   >"$OUT/b-nfs-norm"
    sed 's/"generation":[0-9]*/"generation":N/' "$OUT/b-fskit" >"$OUT/b-fskit-norm"
    if diff -q "$OUT/b-nfs-norm" "$OUT/b-fskit-norm" >/dev/null; then
        printf '       \033[33mnote\033[0m only `generation` differs — stale state dir, not a parity bug (SKILL.md invariant 1)\n'
    fi
fi

# C. content
( cd "$NFS_MOUNT"   && find . -type f ! -path './.sageox/*' -exec shasum -a 256 {} + | sort ) >"$OUT/c-nfs"
( cd "$FSKIT_MOUNT" && find . -type f ! -path './.sageox/*' -exec shasum -a 256 {} + | sort ) >"$OUT/c-fskit"
report content "$OUT/c-nfs" "$OUT/c-fskit"

# D. metadata — the only oracle that sees mode (A/B/C are structurally blind to it;
# INDEX.json has no mode field). Caught ox-v1i9. Needs an executable in the fixture
# to mean anything: a selection of plain files cannot fail this.
( cd "$NFS_MOUNT"   && find . -type f ! -path './.sageox/*' -exec stat -f '%Sp %z %N' {} + | sort ) >"$OUT/d-nfs"
( cd "$FSKIT_MOUNT" && find . -type f ! -path './.sageox/*' -exec stat -f '%Sp %z %N' {} + | sort ) >"$OUT/d-fskit"
report metadata "$OUT/d-nfs" "$OUT/d-fskit"
if [[ -s "$OUT/metadata.diff" ]] && grep -q 'r-x' "$OUT/metadata.diff"; then
    printf '       \033[33mnote\033[0m executable-bit mismatch — this is a REGRESSION of ox-v1i9, not a known issue.\n'
    printf '            Both indexers must mask file mode & 0o555. Check scripts/test-oxdirtest.sh too.\n'
fi

say "result"
if [[ $FAIL -eq 0 ]]; then printf '\033[32mall oracles agree\033[0m\n'; else printf '\033[31mparity divergence — see %s\033[0m\n' "$OUT"; KEEP=1; fi
exit $FAIL
