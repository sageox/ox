#!/usr/bin/env bash
# security/scripts/install-bins.sh — installs all ox CLI security-review tool binaries.
#
# DESIGN (May 2026):
#   Binaries live in a machine-wide cache at $XDG_CACHE_HOME/ox/security/bin
#   (defaults to ~/.cache/ox/security/bin). The workspace ./bin/ is a set of
#   symlinks into that cache. Why: agentic dev sessions spin up 10–12 git
#   worktrees a day; downloading ~200MB of tools per worktree is wasted time
#   and bandwidth. One download, all worktrees benefit.
#
#   - Cached tools are skipped on subsequent runs. Set FORCE=1 to re-download.
#   - Symlinks into ./bin/ are always re-created (cheap, fixes drift if you
#     hand-edited or rm'd them).
#
# Floating-on-latest, no root, no Homebrew, no PATH pollution.
# Used by: `make sec-install`, the GH Action, the pre-commit fast tier when a tool is missing.
#
# WHY "LATEST" AND NOT PINNED (May 2026):
#   Best security practice would be to pin every binary by version + checksum. We're not
#   doing that — ox CLI has no security engineer to adjudicate version bumps, and a stale
#   pin would mean shipping known-bad versions for weeks. Until we have someone to own
#   the pin-bump cadence, floating on `/latest/` is the lesser evil. If you join SageOx
#   as the security owner, please pin these and own the bump schedule.
#
# Why these tools and not others (May 2026):
#   - OpenGrep: FOSS Semgrep fork (LGPL-2.1) with cross-function taint analysis,
#     post-late-2024 Semgrep CE license change. https://appsecsanta.com/sast-tools/opengrep-vs-semgrep
#   - govulncheck: official Go tool. Lowest false-positive SCA via call-graph reachability.
#   - OSV-Scanner: Google. Multi-ecosystem advisories, reachability for some langs.
#   - Syft + Grype: Anchore. REPLACES Trivy — Trivy was compromised twice in March 2026.
#     See https://github.com/aquasecurity/trivy/security/advisories/GHSA-69fq-xp46-6x23
#     and https://www.stepsecurity.io/blog/trivy-compromised-a-second-time---malicious-v0-69-4-release
#     Even though patched, May-2026 community sentiment is "trust is broken for a tool that
#     ships in CI's critical path."

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$ROOT/bin"
CACHE_ROOT="${OX_SECURITY_BIN_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/ox/security/bin}"
mkdir -p "$BIN" "$CACHE_ROOT"

# Detect platform once; bail if unsupported.
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) echo "unsupported arch: $ARCH" >&2; exit 1 ;;
esac
[[ "$OS" == "darwin" || "$OS" == "linux" ]] || { echo "unsupported os: $OS" >&2; exit 1; }

# Resolve the latest release tag for a given github repo (e.g. "opengrep/opengrep").
# Falls back gracefully — empty string on failure so the caller can decide.
latest_release_tag() {
  local repo="$1"
  curl -fsSL "https://api.github.com/repos/${repo}/releases/latest" 2>/dev/null \
    | python3 -c 'import json,sys; print(json.load(sys.stdin).get("tag_name",""))' 2>/dev/null \
    || echo ""
}

# Skip-cache predicate. FORCE=1 forces re-download for all tools.
have_cached() {
  [[ -z "${FORCE:-}" && -x "$CACHE_ROOT/$1" ]]
}

# Workspace symlink — ./bin/<tool> points into the shared cache.
# Always recreated so a stale or hand-edited link gets corrected.
link_into_bin() {
  local tool="$1"
  ln -sfn "$CACHE_ROOT/$tool" "$BIN/$tool"
}

# Helper: download + extract + install single binary from a release tarball.
# Args: name url ext binary-name-in-archive
# Installs into $CACHE_ROOT, then symlinks into $BIN.
fetch_tarball() {
  local name="$1" url="$2" ext="$3" inner="$4"
  local tmp; tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  echo "→ $name $url"
  local dl="$tmp/download.$ext"
  curl -fsSL "$url" -o "$dl"
  case "$ext" in
    tar.gz|tgz) tar -xzf "$dl" -C "$tmp" ;;
    zip) unzip -q "$dl" -d "$tmp" ;;
    bin) mv "$dl" "$tmp/$inner" ;;
    *) echo "unknown ext: $ext" >&2; return 1 ;;
  esac
  install -m 0755 "$tmp/$inner" "$CACHE_ROOT/$name"
  link_into_bin "$name"
}

# ---------------------------------------------------------------------------
# OpenGrep — platform-aware asset name. Hardcoding `manylinux_x86` here used to
# break every Mac dev's first `make sec-install`.
# ---------------------------------------------------------------------------
if have_cached opengrep; then
  echo "✓ opengrep cached"; link_into_bin opengrep
else
  case "${OS}-${ARCH}" in
    linux-amd64)  OPENGREP_ASSET="opengrep_manylinux_x86" ;;
    linux-arm64)  OPENGREP_ASSET="opengrep_manylinux_aarch64" ;;
    darwin-amd64) OPENGREP_ASSET="opengrep_osx_x86" ;;
    darwin-arm64) OPENGREP_ASSET="opengrep_osx_arm64" ;;
    *) echo "unsupported opengrep platform: ${OS}-${ARCH}" >&2; exit 1 ;;
  esac
  fetch_tarball "opengrep" \
    "https://github.com/opengrep/opengrep/releases/latest/download/${OPENGREP_ASSET}" \
    "bin" "opengrep"
fi

# ---------------------------------------------------------------------------
# govulncheck — installed via go install. `@latest` floats; matches our
# top-of-file "no security engineer yet" stance.
# ---------------------------------------------------------------------------
if have_cached govulncheck; then
  echo "✓ govulncheck cached"; link_into_bin govulncheck
elif ! command -v go >/dev/null; then
  echo "warning: go not on PATH — govulncheck install skipped. Install Go ≥1.23 to enable." >&2
else
  GOBIN="$CACHE_ROOT" go install "golang.org/x/vuln/cmd/govulncheck@latest"
  link_into_bin govulncheck
fi

# ---------------------------------------------------------------------------
# OSV-Scanner — resolve the latest release tag, then build the asset URL.
# ---------------------------------------------------------------------------
if have_cached osv-scanner; then
  echo "✓ osv-scanner cached"; link_into_bin osv-scanner
else
  OSV_TAG="$(latest_release_tag "google/osv-scanner")"
  OSV_VERSION="${OSV_TAG#v}"
  if [[ -n "$OSV_VERSION" ]]; then
    # v2.3.8+ ships bare binaries (no tarball, no version in filename)
    fetch_tarball "osv-scanner" \
      "https://github.com/google/osv-scanner/releases/download/v${OSV_VERSION}/osv-scanner_${OS}_${ARCH}" \
      "bin" "osv-scanner"
  else
    echo "warning: could not resolve osv-scanner latest tag — skipped" >&2
  fi
fi

# ---------------------------------------------------------------------------
# Syft / Grype (Anchore). The install scripts themselves are checksum-verified
# binaries internally; floating-on-main matches the rest of this script's
# stance. Pass no version arg → installer picks the latest release.
# ---------------------------------------------------------------------------
if have_cached syft; then
  echo "✓ syft cached"; link_into_bin syft
else
  curl -fsSL https://raw.githubusercontent.com/anchore/syft/main/install.sh | sh -s -- -b "$CACHE_ROOT"
  link_into_bin syft
fi
if have_cached grype; then
  echo "✓ grype cached"; link_into_bin grype
else
  curl -fsSL https://raw.githubusercontent.com/anchore/grype/main/install.sh | sh -s -- -b "$CACHE_ROOT"
  link_into_bin grype
fi

# Summary
echo
echo "Cache: $CACHE_ROOT"
echo "Linked into $BIN:"
for tool in opengrep govulncheck osv-scanner syft grype; do
  if [[ -x "$BIN/$tool" ]]; then
    printf "  ✓ %-14s %s\n" "$tool" "$("$BIN/$tool" --version 2>/dev/null | head -1 || echo '(version unknown)')"
  else
    printf "  ✗ %-14s not installed\n" "$tool"
  fi
done

echo
echo "Add ./bin to your PATH for this shell:    export PATH=\"$BIN:\$PATH\""
echo "Or run via:                               make sec   /   make sec-fast"
echo "To re-download all tools:                 FORCE=1 make sec-install"

echo "READY: install-bins.sh installed security scanner binaries"
