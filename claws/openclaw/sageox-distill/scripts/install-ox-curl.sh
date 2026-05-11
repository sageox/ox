#!/usr/bin/env bash
# install-ox-curl.sh — download a pinned ox release binary directly from
# GitHub Releases, verify it against an embedded sha256 checksum, and
# install to $HOME/.local/bin. No sudo, no shell-script piping, no
# dynamic "latest" resolution.
#
# Why this shape: scanners flag `curl | bash` of a remote shell script,
# and skills that sudo into system paths. This script avoids both. The
# release tag and per-platform sha256s are pinned in the source below, so
# an attacker cannot substitute a different binary without also editing
# this file (which is reviewed on skill publish).
#
# Bumping: when a newer sageox/ox release ships, update OX_INSTALL_REF
# and the OX_SHA256_* constants. Fetch checksums.txt from
# https://github.com/sageox/ox/releases/download/<tag>/checksums.txt
#
# Usage: install-ox-curl.sh
#
# Stdout: human-readable progress
# Stderr: errors
# Exit:
#   0 — success, ox installed and memory file written
#   3 — internal error (curl/tar missing, download failed, checksum
#       mismatch, unsupported platform, or ox not runnable after install)

set -euo pipefail

OX_INSTALL_REF="v0.7.2"
OX_VERSION="${OX_INSTALL_REF#v}"
OX_REPO="sageox/ox"

# sha256(ox_<version>_<os>_<arch>.tar.gz) — from the checksums.txt asset
# on the pinned release. Lock these when bumping OX_INSTALL_REF.
OX_SHA256_darwin_amd64="ea055f19af0d95ff92a863c25a7375c5312b91c55ceb4de6f39b7d7f6bd1aec4"
OX_SHA256_darwin_arm64="4e4ec64a11b478f5ef820a910aaeb34d293bbc77e93461abbb8e841d2ea7d1c0"
OX_SHA256_linux_amd64="db0535adaeca92afe64f4046029457132d2f3bb74c5e57c69da78ae443721d26"
OX_SHA256_linux_arm64="5c705067fce9770b65d2c2db33cc29c2401b8549b1cfcb23d10b3d1f5b06f804"
OX_SHA256_freebsd_amd64="635c4feacb7b859ebf0808311023f559dab4ed86e28a6c10f06bbaeb7b75c6d9"

command -v curl >/dev/null 2>&1 || { echo "error: curl is required" >&2; exit 3; }
command -v tar  >/dev/null 2>&1 || { echo "error: tar is required"  >&2; exit 3; }

case "$(uname -s)" in
  Darwin)  OS="darwin"  ;;
  Linux)   OS="linux"   ;;
  FreeBSD) OS="freebsd" ;;
  *)
    echo "error: unsupported OS $(uname -s); skill supports macOS, Linux, and FreeBSD" >&2
    exit 3
    ;;
esac

case "$(uname -m)" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *)
    echo "error: unsupported architecture $(uname -m)" >&2
    exit 3
    ;;
esac

PLATFORM="${OS}_${ARCH}"
SHA_VAR="OX_SHA256_${PLATFORM}"
EXPECTED_SHA="${!SHA_VAR:-}"
if [ -z "$EXPECTED_SHA" ]; then
  echo "error: no pinned checksum for platform ${PLATFORM} in ${OX_INSTALL_REF}" >&2
  exit 3
fi

ARCHIVE_NAME="ox_${OX_VERSION}_${PLATFORM}.tar.gz"
DOWNLOAD_URL="https://github.com/${OX_REPO}/releases/download/${OX_INSTALL_REF}/${ARCHIVE_NAME}"

# Portable mktemp template — works on both GNU (Linux) and BSD (macOS).
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/ox-install.XXXXXXXX")"
trap 'rm -rf "$WORK_DIR" 2>/dev/null' EXIT

echo "Downloading ox ${OX_INSTALL_REF} for ${PLATFORM}"
echo "  from: ${DOWNLOAD_URL}"

# -f fails on HTTP errors, --max-time bounds a stalled connection.
if ! curl -fsSL --max-time 120 "$DOWNLOAD_URL" -o "$WORK_DIR/$ARCHIVE_NAME"; then
  echo "error: failed to download ${DOWNLOAD_URL}" >&2
  exit 3
fi

echo "Verifying sha256 checksum..."
if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL_SHA="$(sha256sum "$WORK_DIR/$ARCHIVE_NAME" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL_SHA="$(shasum -a 256 "$WORK_DIR/$ARCHIVE_NAME" | awk '{print $1}')"
else
  echo "error: neither sha256sum nor shasum is available; refusing to install unverified binary" >&2
  exit 3
fi

if [ "$ACTUAL_SHA" != "$EXPECTED_SHA" ]; then
  echo "error: sha256 mismatch for ${ARCHIVE_NAME}" >&2
  echo "  expected: $EXPECTED_SHA" >&2
  echo "  actual:   $ACTUAL_SHA" >&2
  exit 3
fi
echo "  ok: $ACTUAL_SHA"

echo "Extracting archive..."
if ! tar -xzf "$WORK_DIR/$ARCHIVE_NAME" -C "$WORK_DIR"; then
  echo "error: failed to extract $ARCHIVE_NAME" >&2
  exit 3
fi

INSTALL_DIR="$HOME/.local/bin"
mkdir -p "$INSTALL_DIR"

# Install ox and every ox-adapter-* binary shipped in the tarball. We
# glob rather than hardcode the adapter list so new adapters added in
# future releases work without re-editing this file.
shopt -s nullglob
installed=0
for src in "$WORK_DIR/ox" "$WORK_DIR"/ox-adapter-*; do
  [ -f "$src" ] || continue
  name="$(basename "$src")"
  dest="$INSTALL_DIR/$name"
  mv "$src" "$dest"
  chmod +x "$dest"

  # macOS ad-hoc re-sign: avoids slow Gatekeeper checks on first run.
  # Non-fatal — a failure here does not block install.
  if [ "$OS" = "darwin" ] && command -v codesign >/dev/null 2>&1; then
    codesign --remove-signature "$dest" 2>/dev/null || true
    codesign --force --sign - "$dest" 2>/dev/null || true
  fi

  installed=$((installed + 1))
done
shopt -u nullglob

if [ "$installed" -eq 0 ]; then
  echo "error: tarball contained no ox binaries" >&2
  exit 3
fi

echo "Installed $installed binaries to $INSTALL_DIR"

# PATH guidance — $HOME/.local/bin isn't on PATH by default on every
# distro. Surface this before the readiness gate so the user sees the
# fix in context.
if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]]; then
  echo ""
  echo "warning: $INSTALL_DIR is not on your PATH" >&2
  echo "fix: add this line to ~/.openclaw/.env (OpenClaw loads it into the skill subprocess):" >&2
  echo "  PATH=\"$INSTALL_DIR:\$PATH\"" >&2
  echo "then restart the skill." >&2
fi

# Readiness gate: the binary at $INSTALL_DIR/ox must exist, be
# executable, run, and report the pinned release version. The sha256
# check earlier guarantees the extracted bytes are exactly what GitHub
# Releases published for this tag, but those bytes might still fail to
# execute on the host (libc mismatch, unsupported OS version, stripped
# dependency, etc.) — catch that now, before we write state claiming a
# successful install.
#
# Invoke the literal $INSTALL_DIR/ox path rather than using PATH
# lookup. If for any reason the install loop above didn't actually
# write the ox binary (e.g. a future tarball ships only adapters, or
# the upstream release was mis-packaged), a PATH-based check could
# silently fall through to a pre-existing system ox and claim success
# against the wrong binary.
if [ ! -x "$INSTALL_DIR/ox" ]; then
  echo "error: expected ox binary missing at $INSTALL_DIR/ox" >&2
  exit 3
fi

if ! version_output="$("$INSTALL_DIR/ox" version 2>&1)"; then
  echo "error: $INSTALL_DIR/ox failed to run" >&2
  echo "$version_output" >&2
  exit 3
fi

# `ox version` prints "ox <version>\n..." on stdout. Compare the first
# line exactly rather than substring-matching — a loose *"$OX_VERSION"*
# pattern would false-positive when e.g. 0.6.3 is a suffix of 10.6.3.
first_line="$(printf '%s\n' "$version_output" | head -n1)"
if [ "$first_line" != "ox $OX_VERSION" ]; then
  echo "error: $INSTALL_DIR/ox reports '$first_line', expected 'ox $OX_VERSION'" >&2
  exit 3
fi

# Record install state so update-ox.sh can confirm the skill has a
# known-good ox install on subsequent runs. Only factual state — what
# was installed, where, when — no preference fields.
#
# Use `jq -n --arg` rather than a heredoc so JSON-special characters in
# values (e.g. `"` or `\` in a pathological $HOME) get escaped correctly
# instead of producing a malformed state file. `jq` is a hard skill
# dependency (declared in SKILL.md `requires.bins`), so OpenClaw has
# already ensured it's available by the time this script runs.
#
# Write to a temp file in the same directory, then rename into place.
# update-ox.sh reads this file, so an interrupted write that left a
# zero-byte or partial JSON file would break the readiness gate.
# Same-directory rename is atomic at the directory-entry level, which
# is all we need here.
STATE_DIR="$HOME/.openclaw/memory"
STATE_FILE="$STATE_DIR/sageox-ox-install.json"
mkdir -p "$STATE_DIR"
TMP_STATE_FILE="$(mktemp "${STATE_DIR}/sageox-ox-install.json.XXXXXXXX")"
INSTALLED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
jq -n \
  --arg ox_install_ref "$OX_INSTALL_REF" \
  --arg install_dir    "$INSTALL_DIR" \
  --arg installed_at   "$INSTALLED_AT" \
  '{
    ox_install_ref: $ox_install_ref,
    install_dir:    $install_dir,
    installed_at:   $installed_at
  }' > "$TMP_STATE_FILE"
mv "$TMP_STATE_FILE" "$STATE_FILE"

echo "ox ${OX_INSTALL_REF} installed successfully"
