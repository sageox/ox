"""Integrity and cache tests for the public session-import fixture loader."""

import hashlib
import io
import json
import tarfile
import tempfile
import unittest
import urllib.error
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from unittest import mock

import session_import_fixture as fixture


def archive(extra=None):
    data = io.BytesIO()
    with tarfile.open(fileobj=data, mode="w:gz") as out:
        for name, content in {
            "math-blitz/code/app.py": b"print('fixture')\n",
            "math-blitz/claude/session.jsonl": b"{}\n",
            "math-blitz/codex/session.jsonl": b"{}\n",
        }.items():
            member = tarfile.TarInfo(name)
            member.size = len(content)
            out.addfile(member, io.BytesIO(content))
        if extra is not None:
            out.addfile(extra, io.BytesIO(b""))
    return data.getvalue()


class FixtureLoader(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.cache = self.root / "cache"
        self.pin = self.root / "pin.json"
        self.data = archive()
        self.digest = hashlib.sha256(self.data).hexdigest()
        self.pin.write_text(json.dumps({"url": "https://example.com/fixture.tar.gz", "sha256": self.digest}))

    def resolve(self):
        return fixture.resolve_fixture(cache_dir=self.cache, pin_path=self.pin)

    def test_fetch_is_verified_and_warm_cache_stays_offline(self):
        with mock.patch.object(fixture.urllib.request, "urlopen", return_value=io.BytesIO(self.data)) as fetch:
            first = self.resolve()
            fetch.assert_called_once()
            self.assertEqual(20, fetch.call_args.kwargs["timeout"])
        self.assertEqual(b"print('fixture')\n", (first / "code/app.py").read_bytes())
        with mock.patch.object(fixture.urllib.request, "urlopen", side_effect=AssertionError("offline")):
            self.assertEqual(first, self.resolve())
        self.assertEqual([], list(self.cache.glob(".fixture-*")))

    def test_bad_checksum_never_extracts_or_completes_cache(self):
        with mock.patch.object(fixture.urllib.request, "urlopen", return_value=io.BytesIO(b"wrong bytes")):
            with self.assertRaisesRegex(fixture.FixtureError, "SHA-256 mismatch"):
                self.resolve()
        self.assertEqual([], list(self.cache.iterdir()))

    def test_unsafe_names_and_nonregular_members_are_refused(self):
        names = ["/math-blitz/out", "math-blitz/../out", "math-blitz/code/./out", "math-blitz//out",
                 "outside/file", "math-blitz/code\\out", "math-blitz/code/C:out"]
        for name in names:
            with self.subTest(name=name):
                data = archive(tarfile.TarInfo(name))
                with self.assertRaisesRegex(fixture.FixtureError, "unsafe"):
                    fixture.verified_files(data, hashlib.sha256(data).hexdigest())
        for kind in [tarfile.SYMTYPE, tarfile.LNKTYPE, tarfile.DIRTYPE, tarfile.FIFOTYPE, tarfile.CHRTYPE]:
            with self.subTest(kind=kind):
                member = tarfile.TarInfo("math-blitz/code/link")
                member.type = kind
                member.linkname = "../../outside"
                data = archive(member)
                with self.assertRaisesRegex(fixture.FixtureError, "unsafe"):
                    fixture.verified_files(data, hashlib.sha256(data).hexdigest())

    def test_duplicate_member_and_resource_limits_are_refused(self):
        data = archive(tarfile.TarInfo("math-blitz/code/app.py"))
        with self.assertRaisesRegex(fixture.FixtureError, "duplicate"):
            fixture.verified_files(data, hashlib.sha256(data).hexdigest())
        with mock.patch.object(fixture, "FILE_LIMIT", 2):
            with self.assertRaisesRegex(fixture.FixtureError, "limits"):
                fixture.verified_files(self.data, self.digest)
        with mock.patch.object(fixture, "EXPANDED_LIMIT", 2):
            with self.assertRaisesRegex(fixture.FixtureError, "limits"):
                fixture.verified_files(self.data, self.digest)
        with mock.patch.object(fixture, "ARCHIVE_LIMIT", 2), mock.patch.object(fixture.urllib.request, "urlopen", return_value=io.BytesIO(self.data)):
            with self.assertRaisesRegex(fixture.FixtureError, "download limit"):
                self.resolve()

    def test_unavailable_asset_reports_a_controlled_failure(self):
        with mock.patch.object(fixture.urllib.request, "urlopen", side_effect=urllib.error.URLError("unavailable")):
            with self.assertRaisesRegex(fixture.FixtureError, "cannot download.*retry when available"):
                self.resolve()

    def test_changed_or_extra_cached_files_are_refused(self):
        with mock.patch.object(fixture.urllib.request, "urlopen", return_value=io.BytesIO(self.data)):
            cached = self.resolve()
        source = cached / "code/app.py"
        source.write_bytes(b"modified fixture")
        with self.assertRaisesRegex(fixture.FixtureError, "modified.*remove"):
            self.resolve()
        source.write_bytes(b"print('fixture')\n")
        (cached / "code/unverified.py").write_text("unverified")
        with self.assertRaisesRegex(fixture.FixtureError, "modified.*remove"):
            self.resolve()

    def test_cached_archive_is_verified_and_cached_symlinks_are_refused(self):
        with mock.patch.object(fixture.urllib.request, "urlopen", return_value=io.BytesIO(self.data)):
            cached = self.resolve()
        cached_archive = cached.parent / "fixture.tar.gz"
        cached_archive.write_bytes(b"corrupt cache")
        with self.assertRaisesRegex(fixture.FixtureError, "SHA-256 mismatch"):
            self.resolve()
        cached_archive.write_bytes(self.data)
        source = cached / "code/app.py"
        source.unlink()
        source.symlink_to(self.pin)
        with self.assertRaisesRegex(fixture.FixtureError, "modified.*remove"):
            self.resolve()

    def test_concurrent_fetches_publish_one_complete_cache(self):
        with mock.patch.object(fixture.urllib.request, "urlopen", side_effect=lambda *args, **kwargs: io.BytesIO(self.data)):
            with ThreadPoolExecutor(max_workers=4) as pool:
                results = list(pool.map(lambda _: self.resolve(), range(4)))
        self.assertTrue(all(p == results[0] for p in results))
        self.assertEqual([self.digest], [p.name for p in self.cache.iterdir()])

    def test_relative_xdg_cache_does_not_write_into_the_checkout(self):
        with mock.patch.dict(fixture.os.environ, {"XDG_CACHE_HOME": "relative-cache"}), mock.patch.object(fixture.Path, "home", return_value=self.root):
            self.assertEqual(self.root / ".cache/sageox/test-fixtures/session-import", fixture.default_cache())


if __name__ == "__main__":
    unittest.main()
