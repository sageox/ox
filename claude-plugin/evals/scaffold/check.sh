#!/usr/bin/env bash
# Free, offline proof that the eval sandbox is sound — run it before paying
# for an eval run, and after any change to seed.sh or fixtures/.
#
# It seeds a throwaway sandbox the way `claude plugin eval` would (fresh
# HOME, fresh cwd), then asserts the things every case depends on:
#   - `ox agent prime` runs offline and emits the fixture's team knowledge
#   - `ox session list` sees the seeded ledger (yesterday's session)
#   - the seeded pagination bug really fails `go test` (case 06's red)
#   - nothing under the sandbox points at a real SageOx host
#
# Usage: check.sh /path/to/ox
set -euo pipefail

ox="${1:?usage: check.sh /path/to/ox}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
sandbox="$(mktemp -d "${TMPDIR:-/tmp}/ox-eval-check.XXXXXX")"
trap 'rm -rf "$sandbox"' EXIT

mkdir -p "$sandbox/home" "$sandbox/cwd"
export HOME="$sandbox/home"
export XDG_CONFIG_HOME="$HOME/.config" XDG_DATA_HOME="$HOME/.local/share"
export XDG_CACHE_HOME="$HOME/.cache" XDG_STATE_HOME="$HOME/.local/state"
export OX_NO_DAEMON=1 OX_SESSION_RECORDING=disabled SAGEOX_TELEMETRY=false DO_NOT_TRACK=1

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "  ok  $*"; }

(cd "$sandbox/cwd" && bash "$here/seed.sh")
pass "seed.sh populated the sandbox"

# no fixture may name a resolvable SageOx host — the fake bearer must never leave the box
if grep -rq "sageox\.ai" "$sandbox/cwd/.sageox" "$HOME/.config/sageox"; then
  fail "sandbox config references a *.sageox.ai host; use the loopback endpoint"
fi
pass "endpoint is loopback-only"

# The first prime is the one that matters: a hook-driven SessionStart prime,
# exactly as the plugin delivers it. (A second prime for the same agent in
# one window is a compact re-prime by design — so this one goes first.)
# Session markers live under /tmp/<user>/sageox/sessions, outside the
# sandbox HOME, so the id must be unique per run or the second run is a
# compact re-prime of the first.
hooked="$sandbox/prime-hook.xml"
session_id="eval-check-$(basename "$sandbox")"
(cd "$sandbox/cwd" && printf '{"session_id":"%s","hook_event_name":"SessionStart","source":"startup"}' "$session_id" \
  | "$ox" agent prime --agent claude-code > "$hooked" 2> "$sandbox/prime.err") || fail "hook-mode prime exited non-zero: $(cat "$sandbox/prime.err")"
grep -q '<ox-prime' "$hooked" || fail "prime output is not <ox-prime>"

# the plugin hook runs prime with 2>&1, so anything on stderr lands in the
# model's context next to the payload; a WARN there reads as "context failed".
# The KB fetch is deliberately left enabled here so it fails (connection
# refused) exactly as it does offline — this is the red-first gate for
# logger.InitPayloadMode: with payload mode removed this line fails.
if grep -q . "$sandbox/prime.err"; then
  fail "prime wrote to stderr — that noise rides the hook into the model: $(head -c 300 "$sandbox/prime.err")"
fi
pass "prime stderr is silent"

# Claude Code injects at most 10,000 characters of hook output and keeps only
# a 2 KB preview of anything longer. The hook-driven prime must fit, must
# still carry the always-visible rule, and must point at a complete bundle
# holding everything it left out. This is the red-first gate for the hook-cap
# trimmer: with fitPrimeToHookCap removed the size assertion fails.
hooked_bytes="$(wc -c < "$hooked" | tr -d ' ')"
[ "$hooked_bytes" -le 10000 ] || fail "hook-mode prime is $hooked_bytes bytes — over Claude Code's 10,000-char hook cap; the model would see a 2 KB preview"
grep -q 'name="retry-policy" visibility="always"' "$hooked" || fail "hook-mode prime dropped the always-visible team rule while fitting the cap ($hooked_bytes bytes)"
grep -q '<consult-first>' "$hooked" || fail "hook-mode prime dropped consult-first while fitting the cap"
if grep -q '<deferred path="' "$hooked"; then
  full="$(grep -o '<deferred path="[^"]*"' "$hooked" | sed 's/<deferred path="//; s/"$//')"
  [ -s "$full" ] || fail "deferred pointer names $full but the full bundle was not written"
  grep -q 'adr-012-config-precedence.md' "$full" || fail "indexed team doc missing from the full bundle"
  grep -q '<context-budget' "$full"              || fail "context-budget tag missing from the full bundle"
  budget="$(grep -o '<context-budget[^>]*>' "$full")"
  pass "hook-mode prime fits the cap ($hooked_bytes bytes; full bundle $(wc -c < "$full" | tr -d ' ') bytes) — $budget"
else
  grep -q 'adr-012-config-precedence.md' "$hooked" || fail "indexed team doc missing from prime"
  budget="$(grep -o '<context-budget[^>]*>' "$hooked")"
  pass "hook-mode prime fits the cap without deferring ($hooked_bytes bytes) — $budget"
fi

listing="$(cd "$sandbox/cwd" && "$ox" session list --limit 5 --json < /dev/null 2>/dev/null)"
echo "$listing" | grep -q '"ledger_available": *true' || fail "session list does not see the seeded ledger"
echo "$listing" | grep -q 'TestListingHandler_Timeout' || fail "yesterday's session summary missing from session list"
pass "session list sees yesterday's ledger session"

if (cd "$sandbox/cwd" && go test ./internal/pagination/ >/dev/null 2>&1); then
  fail "seeded pagination bug did not fail go test — case 06 has no red"
fi
pass "seeded pagination bug fails go test (case 06 red confirmed)"

# guard against the real-user leak the recording path would otherwise cause
if find "$XDG_DATA_HOME" -path '*ledgers*' -name '.recording.json' | grep -q .; then
  fail "prime started a session recording inside the sandbox; OX_SESSION_RECORDING is not being honored"
fi
pass "no recording started in the sandbox"

echo "READY: eval sandbox is sound"
