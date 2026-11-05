#!/usr/bin/env node
"use strict";

// Runs the ox binary from the @sageox/ox-<os>-<arch> package npm installed
// for this machine. The release workflow publishes those packages, and this
// launcher, from the same GitHub release.

const { spawn } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");

const SUPPORTED = ["darwin-arm64", "darwin-x64", "freebsd-x64", "linux-arm64", "linux-x64"];
const INSTALL_DOCS = "https://github.com/sageox/ox#install";

function fail(message) {
  process.stderr.write(`ox: ${message}\n`);
  process.exit(1);
}

const platform = `${process.platform}-${process.arch}`;
if (!SUPPORTED.includes(platform)) {
  fail(`no prebuilt ox for ${platform} (supported: ${SUPPORTED.join(", ")}). Other install options: ${INSTALL_DOCS}`);
}

const pkg = `@sageox/ox-${platform}`;
let binDir;
try {
  binDir = path.join(path.dirname(require.resolve(`${pkg}/package.json`)), "bin");
} catch {
  fail(`${pkg} is missing. npm installs it as an optional dependency of @sageox/ox; reinstall without --omit=optional or --no-optional.`);
}

// npm can drop the executable bit; ox and its adapters all need it.
for (const name of fs.readdirSync(binDir)) {
  const file = path.join(binDir, name);
  try {
    fs.accessSync(file, fs.constants.X_OK);
  } catch {
    try {
      fs.chmodSync(file, 0o755);
    } catch {}
  }
}

const child = spawn(path.join(binDir, "ox"), process.argv.slice(2), { stdio: "inherit" });

// The terminal already sends Ctrl-C to ox; stay alive so ox decides how to exit.
process.on("SIGINT", () => {});
for (const signal of ["SIGTERM", "SIGHUP"]) {
  process.on(signal, () => child.kill(signal));
}

child.on("error", (err) => fail(err.message));
child.on("exit", (code, signal) => {
  if (signal) {
    process.removeAllListeners(signal);
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code ?? 1);
});
