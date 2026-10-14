"""Exercise install recipes with sandboxed Go and removal commands."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


REPO_ROOT = Path(__file__).resolve().parents[1]
MAKE = shutil.which("make")


@unittest.skipUnless(MAKE, "make is required")
class MakeInstallTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="ox-make-install-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.tools = self.root / "tools"
        self.tools.mkdir()
        self.log = self.root / "commands.jsonl"
        # Even if a recipe's safety check regresses, these replacements cannot
        # write or remove anything outside this test's temporary directory.
        command = f"#!{sys.executable}\n" + r'''
import json
import os
from pathlib import Path
import sys

root = Path(os.environ["OX_INSTALL_TEST_ROOT"])
tool = Path(sys.argv[0]).name
if tool == "go" and sys.argv[1] == "env":
    values = {"GOPATH": os.environ["OX_INSTALL_TEST_GOPATH"],
              "GOBIN": os.environ["OX_INSTALL_TEST_GOBIN"], "GOOS": "linux"}
    print(values[sys.argv[2]])
    sys.exit(0)
with (root / "commands.jsonl").open("a") as log:
    log.write(json.dumps({"tool": tool, "args": sys.argv[1:],
                          "gobin": os.environ.get("GOBIN", "")}) + "\n")
if tool == "go":
    assert sys.argv[1] == "install", sys.argv
    directory = os.environ.get("GOBIN") or (
        os.environ["OX_INSTALL_TEST_GOPATH"].split(os.pathsep)[0] + "/bin")
    paths = [Path(directory) / Path(sys.argv[-1]).name]
else:
    assert tool == "rm" and sys.argv[1] == "-f", sys.argv
    paths = [Path(arg) for arg in sys.argv[2:]]
for path in paths:
    if root not in path.resolve().parents:
        sys.exit("sandbox refused path: " + str(path))
    if tool == "go":
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("installed")
    else:
        path.unlink(missing_ok=True)
'''
        for tool in ("go", "rm"):
            path = self.tools / tool
            path.write_text(command)
            path.chmod(0o755)

    def run_make(self, target, *, gobin="", gopath=None, extra=()):
        gopath = str(gopath or self.root / "gopath")
        env = os.environ.copy()
        for key in ("MAKEFLAGS", "MFLAGS", "GNUMAKEFLAGS"):
            env.pop(key, None)
        env.update({
            "PATH": str(self.tools) + os.pathsep + env.get("PATH", ""),
            "OX_INSTALL_TEST_ROOT": str(self.root),
            "OX_INSTALL_TEST_GOPATH": gopath,
            "OX_INSTALL_TEST_GOBIN": str(gobin),
        })
        return subprocess.run(
            [MAKE, "--no-print-directory", target, "ADAPTERS=ox-adapter-cursor",
             "GOBIN=" + str(gobin), "GOPATH=" + gopath, *extra],
            cwd=REPO_ROOT, env=env, capture_output=True, text=True, timeout=10,
        )

    def commands(self):
        if not self.log.exists():
            return []
        return [json.loads(line) for line in self.log.read_text().splitlines()]

    def assert_success(self, result):
        self.assertEqual(0, result.returncode, result.stdout + result.stderr)

    def test_first_gopath_entry_is_used_for_install_and_uninstall(self):
        first = self.root / "first user's workspace"
        second = self.root / "second"
        for base in (first, second):
            (base / "bin").mkdir(parents=True)
            for name in ("ox", "ox-adapter-cursor", "unrelated"):
                (base / "bin" / name).write_text("original")
        gopath = str(first) + os.pathsep + str(second)

        self.assert_success(self.run_make("install-ox", gopath=gopath))
        self.assert_success(self.run_make("install-adapters", gopath=gopath))
        self.assertEqual("installed", (first / "bin/ox").read_text())
        self.assertEqual("installed", (first / "bin/ox-adapter-cursor").read_text())
        self.assert_success(self.run_make("uninstall", gopath=gopath))

        self.assertFalse((first / "bin/ox").exists())
        self.assertFalse((first / "bin/ox-adapter-cursor").exists())
        self.assertEqual("original", (first / "bin/unrelated").read_text())
        for name in ("ox", "ox-adapter-cursor", "unrelated"):
            self.assertEqual("original", (second / "bin" / name).read_text())

    def test_safe_alias_uses_the_canonical_directory_for_every_operation(self):
        real = self.root / "real bin"
        (real / "child").mkdir(parents=True)
        alias = self.root / "alias"
        alias.symlink_to(real, target_is_directory=True)
        gobin = str(alias) + "/child/../"
        for target in ("install-ox", "install-adapters", "uninstall"):
            self.assert_success(self.run_make(target, gobin=gobin))
        for command in self.commands():
            if command["tool"] == "go":
                self.assertEqual(str(real), command["gobin"])
            else:
                self.assertEqual(real, Path(command["args"][-1]).parent)

    def test_system_directory_aliases_are_refused_before_any_binary_operation(self):
        targets = ("install-ox", "install-adapters", "uninstall-ox", "uninstall-adapters")
        for index, directory in enumerate(("/", "/bin", "/sbin", "/usr/bin", "/usr/sbin")):
            if not Path(directory).is_dir():
                continue
            alias = self.root / f"system-alias-{index}"
            alias.symlink_to(directory, target_is_directory=True)
            aliases = (directory + "/", directory + "/../" + Path(directory).name,
                       "//" + directory.lstrip("/"), alias)
            for gobin in aliases:
                for target in targets:
                    with self.subTest(gobin=str(gobin), target=target):
                        self.log.unlink(missing_ok=True)
                        result = self.run_make(target, gobin=gobin)
                        self.assertNotEqual(0, result.returncode)
                        self.assertIn("Refusing unsafe install directory", result.stderr)
                        self.assertEqual([], self.commands())

    def test_install_creates_a_missing_directory_and_uninstall_is_idempotent(self):
        gobin = self.root / "new directory/bin"
        self.assert_success(self.run_make("uninstall", gobin=gobin))
        self.assertFalse(gobin.exists())
        self.assert_success(self.run_make("install-ox", gobin=gobin))
        self.assertTrue((gobin / "ox").is_file())
        self.assert_success(self.run_make("uninstall", gobin=gobin))
        self.assert_success(self.run_make("uninstall", gobin=gobin))

    def test_empty_directory_is_refused(self):
        for target in ("install-ox", "install-adapters", "uninstall-ox", "uninstall-adapters"):
            with self.subTest(target=target):
                result = self.run_make(target, extra=("INSTALL_BIN=",))
                self.assertNotEqual(0, result.returncode)
                self.assertIn("Refusing empty install directory", result.stderr)
                self.assertEqual([], self.commands())


if __name__ == "__main__":
    unittest.main()
