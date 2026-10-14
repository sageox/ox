"""Resolve the pinned public session-import fixture into a verified local cache.

Only test tooling uses this module; production ox commands never download fixtures.
Standard library only, Python 3.9 compatible.
"""

import hashlib
import http.client
import io
import json
import os
import shutil
import tarfile
import tempfile
import urllib.error
import urllib.request
from pathlib import Path

PIN = Path(__file__).resolve().parent.parent / "cmd/ox/testdata/session-import/artifact.json"
ROOT = "math-blitz"
ARCHIVE_LIMIT = 2 * 1024 * 1024
EXPANDED_LIMIT = 8 * 1024 * 1024
FILE_LIMIT = 128


class FixtureError(Exception):
    """A recoverable fixture setup failure reported without a Python traceback."""

    pass


def default_cache():
    """Use an absolute XDG cache directory, falling back to the home cache."""
    base = Path(os.environ.get("XDG_CACHE_HOME") or Path.home() / ".cache")
    if not base.is_absolute():  # XDG requires absolute paths; never cache relative to a checkout.
        base = Path.home() / ".cache"
    return base / "sageox/test-fixtures/session-import"


def verified_files(data, expected):
    """Check the archive digest before reading any member or creating any file."""
    if hashlib.sha256(data).hexdigest() != expected:
        raise FixtureError("session-import fixture SHA-256 mismatch; no files were extracted")
    files = {}
    total = 0
    try:
        with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
            for member in archive:
                parts = member.name.split("/")
                if (not member.isfile() or len(parts) < 2 or parts[0] != ROOT
                        or any(p in ("", ".", "..") for p in parts)
                        or "\\" in member.name or ":" in member.name or "\x00" in member.name):
                    raise FixtureError("unsafe session-import fixture member: %r" % member.name)
                if member.name in files:
                    raise FixtureError("duplicate session-import fixture member: %r" % member.name)
                total += member.size
                if len(files) >= FILE_LIMIT or total > EXPANDED_LIMIT or member.size < 0:
                    raise FixtureError("session-import fixture exceeds extraction limits")
                with archive.extractfile(member) as source:
                    content = source.read(member.size + 1)
                if len(content) != member.size:
                    raise FixtureError("incomplete session-import fixture member: %r" % member.name)
                files[member.name] = (content, 0o755 if member.mode & 0o111 else 0o644)
    except (tarfile.TarError, EOFError, OSError) as err:
        raise FixtureError("invalid session-import fixture archive: %s" % err) from err
    if not any(n.startswith(ROOT + "/code/") for n in files):
        raise FixtureError("session-import fixture has no code files")
    for agent in ("claude", "codex"):
        if not any(n.startswith(ROOT + "/" + agent + "/") and n.endswith(".jsonl") for n in files):
            raise FixtureError("session-import fixture has no %s recordings" % agent)
    return files


def validate_cache(directory, expected):
    """Warm runs stay offline, but still verify both the archive and extracted bytes."""
    archive = directory / "fixture.tar.gz"
    if directory.is_symlink() or archive.is_symlink() or not archive.is_file():
        raise FixtureError("incomplete session-import fixture cache; remove %s and retry" % directory)
    if archive.stat().st_size > ARCHIVE_LIMIT:
        raise FixtureError("cached session-import fixture exceeds download limit")
    files = verified_files(archive.read_bytes(), expected)
    if (directory / ROOT).is_symlink():
        raise FixtureError("modified session-import fixture cache; remove %s and retry" % directory)
    cached = list((directory / ROOT).rglob("*"))
    names = {p.relative_to(directory).as_posix() for p in cached if p.is_file()}
    if any(p.is_symlink() for p in cached) or names != set(files):
        raise FixtureError("modified session-import fixture cache; remove %s and retry" % directory)
    for name, (content, _) in files.items():
        dest = directory / name
        parts = name.split("/")
        if (any(directory.joinpath(*parts[:i + 1]).is_symlink() for i in range(len(parts)))
                or not dest.is_file() or dest.stat().st_size != len(content) or dest.read_bytes() != content):
            raise FixtureError("modified session-import fixture cache; remove %s and retry" % directory)
    return directory / ROOT


def resolve_fixture(cache_dir=None, pin_path=PIN):
    """Fetch once, verify strictly, and atomically publish a SHA-keyed cache entry."""
    try:
        pin = json.loads(Path(pin_path).read_text(encoding="utf-8"))
        expected = pin["sha256"]
        if (not isinstance(expected, str) or len(expected) != 64
                or any(c not in "0123456789abcdef" for c in expected)
                or not isinstance(pin["url"], str) or not pin["url"].startswith("https://")):
            raise FixtureError("invalid session-import fixture pin")
        cache = Path(cache_dir) if cache_dir is not None else default_cache()
        destination = cache / expected
        if destination.exists() or destination.is_symlink():
            return validate_cache(destination, expected)
        cache.mkdir(parents=True, exist_ok=True)
        request = urllib.request.Request(pin["url"], headers={"User-Agent": "ox-session-import-tests"})
        try:
            with urllib.request.urlopen(request, timeout=20) as response:
                data = response.read(ARCHIVE_LIMIT + 1)
        except (urllib.error.URLError, http.client.HTTPException, TimeoutError, OSError) as err:
            raise FixtureError("cannot download session-import fixture from %s: %s; retry when available" % (pin["url"], err)) from err
        if len(data) > ARCHIVE_LIMIT:
            raise FixtureError("session-import fixture exceeds download limit")
        files = verified_files(data, expected)
        staging = Path(tempfile.mkdtemp(prefix=".fixture-", dir=cache))
        try:
            (staging / "fixture.tar.gz").write_bytes(data)
            for name, (content, mode) in files.items():
                dest = staging / name
                dest.parent.mkdir(parents=True, exist_ok=True)
                dest.write_bytes(content)
                dest.chmod(mode)
            try:
                staging.rename(destination)
            except OSError:
                # Another test process may have completed the same immutable pin.
                if not destination.is_dir():
                    raise
            return validate_cache(destination, expected)
        finally:
            if staging.exists():
                shutil.rmtree(staging)
    except (OSError, ValueError, KeyError, TypeError) as err:
        raise FixtureError("cannot prepare session-import fixture: %s" % err) from err
