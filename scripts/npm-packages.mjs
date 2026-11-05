#!/usr/bin/env node
// Builds the npm packages for one ox release from its GoReleaser archives:
// one @sageox/ox-<os>-<arch> package per archive, holding ox and its adapters,
// and the @sageox/ox launcher from npm/ox pinned to them.
//
// Usage: node scripts/npm-packages.mjs --version 0.26.0 --archives <dir> --out <dir>

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

// GoReleaser os/arch -> npm os/cpu. Must match the launcher's SUPPORTED list.
const PLATFORMS = [
  { goos: "darwin", goarch: "arm64", os: "darwin", cpu: "arm64" },
  { goos: "darwin", goarch: "amd64", os: "darwin", cpu: "x64" },
  { goos: "freebsd", goarch: "amd64", os: "freebsd", cpu: "x64" },
  { goos: "linux", goarch: "arm64", os: "linux", cpu: "arm64" },
  { goos: "linux", goarch: "amd64", os: "linux", cpu: "x64" },
];

const { values } = parseArgs({
  options: {
    version: { type: "string" },
    archives: { type: "string" },
    out: { type: "string" },
  },
});
const { version, archives, out } = values;
if (!version || !archives || !out) {
  throw new Error("usage: npm-packages.mjs --version <x.y.z> --archives <dir> --out <dir>");
}
if (!/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(version)) {
  throw new Error(`not a release version: ${version}`);
}

const launcher = JSON.parse(fs.readFileSync(path.join(repoRoot, "npm/ox/package.json"), "utf8"));
const expected = PLATFORMS.map((p) => `@sageox/ox-${p.os}-${p.cpu}`).sort();
if (JSON.stringify(Object.keys(launcher.optionalDependencies).sort()) !== JSON.stringify(expected)) {
  throw new Error(`npm/ox/package.json optionalDependencies must be exactly: ${expected.join(", ")}`);
}

fs.mkdirSync(out, { recursive: true });

for (const p of PLATFORMS) {
  const name = `@sageox/ox-${p.os}-${p.cpu}`;
  const archive = path.join(archives, `ox_${version}_${p.goos}_${p.goarch}.tar.gz`);
  if (!fs.existsSync(archive)) {
    throw new Error(`missing ${archive}`);
  }

  const extracted = fs.mkdtempSync(path.join(os.tmpdir(), "ox-npm-"));
  execFileSync("tar", ["-xzf", archive, "-C", extracted]);

  const dir = path.join(out, `ox-${p.os}-${p.cpu}`);
  const binDir = path.join(dir, "bin");
  fs.rmSync(dir, { recursive: true, force: true });
  fs.mkdirSync(binDir, { recursive: true });

  const binaries = fs
    .readdirSync(extracted)
    .filter((f) => f === "ox" || f.startsWith("ox-adapter-"))
    .filter((f) => fs.statSync(path.join(extracted, f)).isFile());
  if (!binaries.includes("ox")) {
    throw new Error(`${archive} has no ox binary`);
  }
  for (const f of binaries) {
    fs.copyFileSync(path.join(extracted, f), path.join(binDir, f));
    fs.chmodSync(path.join(binDir, f), 0o755);
  }
  fs.copyFileSync(path.join(repoRoot, "LICENSE"), path.join(dir, "LICENSE"));
  fs.rmSync(extracted, { recursive: true, force: true });

  const pkg = {
    name,
    version,
    description: `The ox binary and adapters for ${p.os}-${p.cpu}. Install @sageox/ox instead.`,
    license: launcher.license,
    homepage: launcher.homepage,
    repository: { ...launcher.repository, directory: "npm/ox" },
    os: [p.os],
    cpu: [p.cpu],
    files: ["bin"],
    preferUnplugged: true,
  };
  fs.writeFileSync(path.join(dir, "package.json"), JSON.stringify(pkg, null, 2) + "\n");
  console.log(`${name}@${version}: ${binaries.length} binaries`);
}

const mainDir = path.join(out, "ox");
fs.rmSync(mainDir, { recursive: true, force: true });
fs.mkdirSync(path.join(mainDir, "bin"), { recursive: true });
fs.copyFileSync(path.join(repoRoot, "npm/ox/bin/ox.js"), path.join(mainDir, "bin/ox.js"));
fs.chmodSync(path.join(mainDir, "bin/ox.js"), 0o755);
fs.copyFileSync(path.join(repoRoot, "npm/ox/README.md"), path.join(mainDir, "README.md"));
fs.copyFileSync(path.join(repoRoot, "LICENSE"), path.join(mainDir, "LICENSE"));
const main = { ...launcher, version };
main.optionalDependencies = Object.fromEntries(expected.map((n) => [n, version]));
fs.writeFileSync(path.join(mainDir, "package.json"), JSON.stringify(main, null, 2) + "\n");
console.log(`@sageox/ox@${version}: launcher`);
