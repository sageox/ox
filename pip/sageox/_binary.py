"""Resolve, download, and verify the native ox binary.

Mirrors ../npm/lib/platform.js + binary.js and, transitively, scripts/install.sh
and .config/goreleaser.yml. Any change to the asset-name mapping there MUST be
reflected here.
"""

from __future__ import annotations

import hashlib
import os
import platform
import stat
import tarfile
import tempfile
import urllib.request
from pathlib import Path

from . import __version__

REPO = "sageox/ox"

# platform.system() -> GOOS
_OS_MAP = {"Darwin": "darwin", "Linux": "linux", "FreeBSD": "freebsd"}

# platform.machine() -> GOARCH (covers the common aliases across OSes).
_ARCH_MAP = {
    "x86_64": "amd64",
    "amd64": "amd64",
    "arm64": "arm64",
    "aarch64": "arm64",
}

# Exact GOOS_GOARCH matrix built in .config/goreleaser.yml (freebsd/arm64 excluded).
_SUPPORTED = {
    "darwin_amd64",
    "darwin_arm64",
    "linux_amd64",
    "linux_arm64",
    "freebsd_amd64",
}


def _binary_home() -> Path:
    # User-owned cache dir, consistent with ox's own ~/.local/share/ox layout.
    base = Path(os.environ.get("XDG_DATA_HOME", Path.home() / ".local" / "share"))
    return base / "ox" / "bin"


def binary_path() -> Path:
    return _binary_home() / "ox"


def _target() -> tuple[str, str]:
    goos = _OS_MAP.get(platform.system())
    goarch = _ARCH_MAP.get(platform.machine().lower())
    if not goos or not goarch or f"{goos}_{goarch}" not in _SUPPORTED:
        raise RuntimeError(
            f"unsupported platform: {platform.system()}/{platform.machine()}\n"
            "sageox ships prebuilt binaries for: darwin/amd64, darwin/arm64, "
            "linux/amd64, linux/arm64, freebsd/amd64.\n"
            f"For other targets install from source: go install github.com/{REPO}/cmd/ox@latest"
        )
    return goos, goarch


def _asset_name(version: str, goos: str, goarch: str) -> str:
    return f"ox_{version}_{goos}_{goarch}.tar.gz"


def _release_base(version: str) -> str:
    # Tag carries the leading 'v'; asset filename does not.
    return f"https://github.com/{REPO}/releases/download/v{version}"


def _fetch(url: str) -> bytes:
    with urllib.request.urlopen(url) as resp:  # noqa: S310 (trusted GitHub host)
        return resp.read()


def _expected_checksum(checksums_text: str, name: str) -> str | None:
    for raw in checksums_text.splitlines():
        line = raw.strip()
        if not line:
            continue
        parts = line.split()
        if len(parts) < 2:
            continue
        fname = parts[-1].lstrip("*")
        if fname == name:
            return parts[0].lower()
    return None


def ensure(force: bool = False) -> Path:
    """Download + verify + install the ox binary if missing. Returns its path."""
    dest = binary_path()
    if dest.exists() and os.access(dest, os.X_OK) and not force:
        return dest

    version = __version__
    goos, goarch = _target()
    name = _asset_name(version, goos, goarch)
    base = _release_base(version)

    archive = _fetch(f"{base}/{name}")

    # Verify BEFORE extracting — a missing/failed checksum is a HARD failure,
    # exactly as scripts/install.sh treats it.
    checksums = _fetch(f"{base}/checksums.txt").decode("utf-8")
    expected = _expected_checksum(checksums, name)
    if expected is None:
        raise RuntimeError(
            f"release checksums.txt does not list {name}; refusing to install unverified binary"
        )
    actual = hashlib.sha256(archive).hexdigest().lower()
    if actual != expected:
        raise RuntimeError(
            f"checksum mismatch for {name}\n  expected: {expected}\n  actual:   {actual}\n"
            "refusing to install a corrupt or tampered binary"
        )

    dest.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="sageox-ox-") as tmp:
        archive_path = Path(tmp) / name
        archive_path.write_bytes(archive)
        with tarfile.open(archive_path, "r:gz") as tf:
            member = tf.getmember("ox")
            extracted = tf.extractfile(member)
            if extracted is None:
                raise RuntimeError(f"archive {name} did not contain an 'ox' binary")
            dest.write_bytes(extracted.read())

    dest.chmod(dest.stat().st_mode | stat.S_IEXEC | stat.S_IXGRP | stat.S_IXOTH)
    return dest
