#!/usr/bin/env bash
# update-ox.sh — keep the pinned ox install in sync with the version this
# skill ships. On every invocation:
#
#   1. Confirm the install state file and the binary at the pinned path
#      exist, and that `command -v ox` resolves to the pinned path (not a
#      system ox earlier on PATH).
#   2. Read the version this skill pins from sibling install-ox-curl.sh —
#      that file is the source of truth, with its OX_INSTALL_REF and the
#      matching per-platform sha256 constants reviewed at skill publish.
#   3. If the installed binary's version doesn't match the skill's pin,
#      re-run install-ox-curl.sh to upgrade in place. The install script
#      downloads, sha256-verifies, extracts, and rewrites the state file
#      — same code path as a fresh install — so we converge on the new
#      pin without forcing the user back through the manual install flow.
#
# This script does NOT introduce dynamic "latest" resolution from the
# internet. It only closes the loop between "skill ships a new pinned
# version" and "the installed binary catches up." The supply-chain
# guarantee (sha256-pinned releases reviewed at skill publish) is
# preserved.
#
# Usage: update-ox.sh
#
# Stdout: nothing on success, human-readable progress during an upgrade
# Stderr: install/path diagnostics surfaced verbatim — most paths emit
#         an `error:` line followed by a `fix:` line, but some emit a
#         single line and others (e.g. when `ox version` itself fails)
#         include captured command stderr, so line count is not fixed.
#         On upgrade failure, install-ox-curl.sh's stderr is also passed
#         through.
# Exit:
#   0 — pinned ox is ready (either already current, or upgraded in place)
#   2 — initial install required (state file missing, binary missing at
#       pinned path, or PATH resolves elsewhere). Agent must read
#       references/INSTALL.md and run the install flow.
#   3 — an upgrade was attempted but install-ox-curl.sh failed (download
#       error, checksum mismatch, etc.). Surface its stderr to the user.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_SCRIPT="$SCRIPT_DIR/install-ox-curl.sh"
STATE_FILE="$HOME/.openclaw/memory/sageox-ox-install.json"
EXPECTED_OX="$HOME/.local/bin/ox"

if [ ! -f "$STATE_FILE" ]; then
  echo "ox install state not configured — run install flow from references/INSTALL.md" >&2
  exit 2
fi

# The pinned install must actually exist at the expected path. Anything
# else (deleted, wrong permissions, never installed by this skill) means
# the state file is stale and the install flow needs to be re-run.
if [ ! -x "$EXPECTED_OX" ]; then
  echo "error: ox is not installed at $EXPECTED_OX" >&2
  echo "fix: re-run the install flow from references/INSTALL.md" >&2
  exit 2
fi

# `command -v` must resolve to the pinned install — not some other ox
# earlier on PATH. If an older system install shadows the pinned one,
# running the skill against it would defeat the whole point of the
# pinned-release contract, so fail with a PATH-order hint.
resolved_ox="$(command -v ox 2>/dev/null || true)"
if [ "$resolved_ox" != "$EXPECTED_OX" ]; then
  echo "error: 'ox' resolves to '${resolved_ox:-<not on PATH>}', not $EXPECTED_OX" >&2
  echo "fix: prepend \$HOME/.local/bin to PATH in ~/.openclaw/.env so the pinned install wins" >&2
  exit 2
fi

# Read the release tag this skill currently pins. install-ox-curl.sh is
# the source of truth — it ships next to this script, and its
# OX_INSTALL_REF + per-platform sha256 constants are reviewed at skill
# publish. update-ox.sh just trails that pin: if the installed binary
# doesn't match, we re-run install-ox-curl.sh to converge.
if [ ! -f "$INSTALL_SCRIPT" ]; then
  echo "error: install script missing at $INSTALL_SCRIPT" >&2
  echo "fix: reinstall this skill via clawhub" >&2
  exit 2
fi
# awk over grep|head|sed: a no-match grep exits non-zero, and under
# `set -euo pipefail` that would silently abort the script inside this
# command substitution before the empty-check below ever ran. awk
# always exits 0 (printing nothing when the pattern doesn't match), so
# control reaches the empty-check and emits the proper remediation.
skill_pin="$(awk -F'"' '/^OX_INSTALL_REF="/ { print $2; exit }' "$INSTALL_SCRIPT")"
if [ -z "$skill_pin" ]; then
  echo "error: could not read OX_INSTALL_REF from $INSTALL_SCRIPT" >&2
  echo "fix: reinstall this skill via clawhub" >&2
  exit 2
fi
skill_version="${skill_pin#v}"

# Verify the binary still runs and report its version. A binary that
# exists on disk but fails to execute (libc mismatch, corruption, etc.)
# is a hard install error, not a drift-to-fix.
if ! version_output="$("$EXPECTED_OX" version 2>&1)"; then
  echo "error: $EXPECTED_OX failed to run" >&2
  echo "$version_output" >&2
  echo "fix: re-run the install flow from references/INSTALL.md" >&2
  exit 2
fi
first_line="$(printf '%s\n' "$version_output" | head -n1)"

# Happy path: installed binary matches the skill's current pin.
if [ "$first_line" = "ox $skill_version" ]; then
  exit 0
fi

# Drift detected. The skill has been updated to pin a different ox
# version than what's installed. Re-run install-ox-curl.sh to upgrade
# (or downgrade — the skill's pin always wins). The install script
# rewrites the state file on success, so the next invocation sees the
# new state.
echo "ox is at '$first_line' but skill pins '$skill_pin' — upgrading..."
if ! bash "$INSTALL_SCRIPT"; then
  echo "error: failed to upgrade ox to $skill_pin" >&2
  echo "fix: re-run scripts/install-ox-curl.sh manually and surface its stderr" >&2
  exit 3
fi

exit 0
