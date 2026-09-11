#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

TMP="$(mktemp -d "${TMPDIR:-/tmp}/oxdirtest-e2e.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

mkdir -p "$TMP/source/a" "$TMP/source/b" "$TMP/state"
printf 'abc' > "$TMP/source/a/one"
printf 'xyz' > "$TMP/source/b/two"

BIN="$(swift build --show-bin-path)/oxdirtest"
FIRST="$TMP/first.log"
SECOND="$TMP/second.log"

printf 'select a\nstatus\nquit\n' |
    "$BIN" --source "$TMP/source" --state "$TMP/state" --cache-bytes 3 >"$FIRST" 2>&1
printf 'status\nselect b\nstatus\nmetrics\nquit\n' |
    "$BIN" --source "$TMP/source" --state "$TMP/state" --cache-bytes 3 >"$SECOND" 2>&1

grep -Fq 'generation=1 selections=1 [a] visible_files=1' "$FIRST"
grep -Fq 'generation=1 selections=1 [a] visible_files=1' "$SECOND"
grep -Fq 'generation=2 selections=2 [a, b] visible_files=1' "$SECOND"
grep -Eq 'evicted_objects=[1-9][0-9]*' "$SECOND"
grep -Fq 'resident_objects=1 resident_bytes=3' "$SECOND"
grep -Fq '"generation":2' "$TMP/state/oxdirtest-control.json"

FIFO="$TMP/control.fifo"
LOCKED="$TMP/locked.log"
SECOND_INSTANCE="$TMP/second-instance.log"
mkfifo "$FIFO"
exec 3<>"$FIFO"
"$BIN" --source "$TMP/source" --state "$TMP/state" --cache-bytes 3 <"$FIFO" >"$LOCKED" 2>&1 &
LOCKED_PID=$!
for _ in 1 2 3 4 5 6 7 8 9 10; do
    grep -Fq 'type '\''help'\'' for commands' "$LOCKED" && break
    sleep 0.1
done
if printf 'quit\n' |
    "$BIN" --source "$TMP/source" --state "$TMP/state" --cache-bytes 3 >"$SECOND_INSTANCE" 2>&1; then
    printf 'second oxdirtest instance unexpectedly opened the same cache\n' >&2
    exit 1
fi
grep -Fq 'Resource temporarily unavailable' "$SECOND_INSTANCE"
printf 'quit\n' >&3
wait "$LOCKED_PID"
exec 3>&-

# The Swift indexer must preserve executable bits while stripping write bits,
# matching Rust readonly_mode(metadata) & 0555. The persisted manifest is the
# input to the FSKit namespace, so this catches the parity bug without signing.
mkdir -p "$TMP/exec-source/bin" "$TMP/exec-state"
printf '#!/bin/sh\n' > "$TMP/exec-source/bin/tool"
chmod 0755 "$TMP/exec-source/bin/tool"
printf 'select bin\nquit\n' |
    "$BIN" --source "$TMP/exec-source" --state "$TMP/exec-state" --cache-bytes 64 >"$TMP/exec.log" 2>&1
grep -Eq '"mode"[[:space:]]*:[[:space:]]*365' "$TMP/exec-state/state/selections.json"

printf 'oxdirtest process test passed\n'
