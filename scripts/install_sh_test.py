"""Run install.sh's release install against a fake GitHub (issue #1333)."""

import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest


REPO_ROOT = Path(__file__).resolve().parents[1]
INSTALL_SH = REPO_ROOT / "scripts" / "install.sh"
VERSION = "v0.25.0"
PLATFORM = "linux_amd64"
ARCHIVE = f"ox_{VERSION[1:]}_{PLATFORM}.tar.gz"
RELEASES = "https://github.com/sageox/ox/releases"
SYSTEM_TOOLS = ["bash", "cat", "chmod", "cut", "dirname", "basename", "grep",
                "gzip", "head", "mkdir", "mktemp", "mv", "rm", "sed", "sha256sum",
                "tail", "tar", "tr", "uname"]

# Stands in for curl or wget. api.github.com always answers like an IP that has
# used its 60 anonymous requests this hour; github.com serves the release.
FAKE_DOWNLOADER = f"#!{sys.executable}\n" + r'''
import json
import os
from pathlib import Path
import sys

root = Path(os.environ["OX_INSTALL_TEST_ROOT"])
tool = Path(sys.argv[0]).name
args = sys.argv[1:]
urls = [a for a in args if a.startswith("https://")]
url = urls[-1]
with (root / "requests.jsonl").open("a") as log:
    log.write(json.dumps({"tool": tool, "args": args}) + "\n")

if url.startswith("https://api.github.com/"):
    if tool == "wget":
        sys.stderr.write("  HTTP/1.1 403 rate limit exceeded\n")
        sys.exit(8)
    sys.stderr.write("curl: (22) The requested URL returned error: 403\n")
    sys.exit(22)

releases = "https://github.com/sageox/ox/releases"
tag_url = releases + "/tag/" + os.environ["OX_INSTALL_TEST_VERSION"]
if url == releases + "/latest":
    if tool == "curl":
        sys.stdout.write(tag_url)
        sys.exit(0)
    sys.stderr.write("  HTTP/1.1 302 Found\n  Location: " + tag_url + "\n")
    sys.exit(8)

assets = root / "assets"
prefix = releases + "/download/" + os.environ["OX_INSTALL_TEST_VERSION"] + "/"
asset = assets / url[len(prefix):] if url.startswith(prefix) else None
if asset is None or not asset.is_file():
    sys.exit(22 if tool == "curl" else 8)
out = None
for flag in ("-o", "-O"):
    if flag in args:
        out = args[args.index(flag) + 1]
if out in (None, "/dev/null"):
    if tool == "curl" and "-w" in args:
        sys.stdout.write("200")
    sys.exit(0)
Path(out).write_bytes(asset.read_bytes())
'''


class InstallFromReleaseTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="ox-install-sh-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.bin = self.root / "bin"
        self.bin.mkdir()
        for tool in SYSTEM_TOOLS:
            path = shutil.which(tool)
            if path is None:
                self.skipTest(f"{tool} is required")
            (self.bin / tool).symlink_to(path)
        self.install_dir = self.root / "install"
        self.install_dir.mkdir()
        assets = self.root / "assets"
        assets.mkdir()
        archive = io.BytesIO()
        with tarfile.open(fileobj=archive, mode="w:gz") as tar:
            body = b"#!/bin/sh\necho ox\n"
            info = tarfile.TarInfo("ox")
            info.size = len(body)
            info.mode = 0o755
            tar.addfile(info, io.BytesIO(body))
        (assets / ARCHIVE).write_bytes(archive.getvalue())
        digest = hashlib.sha256(archive.getvalue()).hexdigest()
        (assets / "checksums.txt").write_text(f"{digest}  {ARCHIVE}\n")

    def use_downloader(self, tool):
        path = self.bin / tool
        path.write_text(FAKE_DOWNLOADER)
        path.chmod(0o755)

    def install(self):
        script = (f'source "{INSTALL_SH}"\n'
                  f'default_install_dir() {{ echo "{self.install_dir}"; }}\n'
                  f'install_from_release {PLATFORM}\n')
        env = {"PATH": str(self.bin), "HOME": str(self.root),
               "TMPDIR": str(self.root), "OX_INSTALL_TEST_ROOT": str(self.root),
               "OX_INSTALL_TEST_VERSION": VERSION}
        return subprocess.run([str(self.bin / "bash"), "-c", script], env=env,
                              cwd=self.root, capture_output=True, text=True,
                              timeout=60)

    def requested_urls(self):
        log = self.root / "requests.jsonl"
        if not log.exists():
            return []
        return [a for line in log.read_text().splitlines()
                for a in json.loads(line)["args"] if a.startswith("https://")]

    def assert_installed_without_api(self, result):
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue((self.install_dir / "ox").is_file())
        self.assertIn(f"Latest version: {VERSION}", result.stdout)
        self.assertIn("Verifying checksum...", result.stdout)
        urls = self.requested_urls()
        self.assertFalse([u for u in urls if "api.github.com" in u], urls)
        self.assertIn(f"{RELEASES}/download/{VERSION}/{ARCHIVE}", urls)
        self.assertIn(f"{RELEASES}/download/{VERSION}/checksums.txt", urls)

    def test_installs_with_curl_when_api_rate_limited(self):
        self.use_downloader("curl")
        self.assert_installed_without_api(self.install())

    def test_installs_with_wget_when_api_rate_limited(self):
        self.use_downloader("wget")
        self.assert_installed_without_api(self.install())

    def test_still_refuses_archive_that_fails_checksum(self):
        self.use_downloader("curl")
        (self.root / "assets" / "checksums.txt").write_text(f"{'0' * 64}  {ARCHIVE}\n")
        result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Checksum verification failed", result.stdout + result.stderr)
        self.assertFalse((self.install_dir / "ox").exists())

    def test_platform_without_archive_falls_back(self):
        self.use_downloader("curl")
        (self.root / "assets" / ARCHIVE).unlink()
        result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("No prebuilt archive available", result.stdout + result.stderr)
        self.assertFalse((self.install_dir / "ox").exists())


if __name__ == "__main__":
    unittest.main()
