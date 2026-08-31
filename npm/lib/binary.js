'use strict';

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const crypto = require('node:crypto');
const { spawnSync } = require('node:child_process');

const { target, assetName } = require('./platform');

const REPO = 'sageox/ox';
const pkg = require('../package.json');

// Package version, WITHOUT a leading 'v', matches goreleaser's {{ .Version }}.
// The release CI pins this to the tag before publishing, so the wrapper always
// fetches the assets from its own matching release.
const VERSION = pkg.version;

const PKG_ROOT = path.join(__dirname, '..');
// The native binary is downloaded at install time; it is NOT shipped in the npm
// tarball (vendor/ is created post-publish and excluded from the files allowlist).
const VENDOR_DIR = path.join(PKG_ROOT, 'vendor');
const BINARY_PATH = path.join(VENDOR_DIR, 'ox');

function releaseBaseUrl() {
  // The git tag carries the leading 'v'; the asset filename does not.
  return `https://github.com/${REPO}/releases/download/v${VERSION}`;
}

function log(msg) {
  process.stderr.write(`[@sageox/ox] ${msg}\n`);
}

async function fetchBuffer(url) {
  // Node >= 18 ships a global fetch that follows GitHub's redirect to the release
  // CDN automatically (node:https would need manual redirect handling).
  const res = await fetch(url, { redirect: 'follow' });
  if (!res.ok) {
    throw new Error(`download failed: ${url} -> HTTP ${res.status} ${res.statusText}`);
  }
  return Buffer.from(await res.arrayBuffer());
}

// goreleaser checksums.txt lines are "<sha256>  <filename>"; some tools emit
// "<sha256> *<filename>" (binary mode). Match install.sh's tolerance for both.
function findChecksum(text, name) {
  for (const rawLine of text.split('\n')) {
    const line = rawLine.trim();
    if (!line) continue;
    const parts = line.split(/\s+/);
    if (parts.length < 2) continue;
    const file = parts[parts.length - 1].replace(/^\*/, '');
    if (file === name) return parts[0].toLowerCase();
  }
  return null;
}

function isInstalled() {
  try {
    fs.accessSync(BINARY_PATH, fs.constants.X_OK);
    return true;
  } catch {
    return false;
  }
}

async function download({ force = false } = {}) {
  const t = target();
  if (!t.supported) {
    throw new Error(
      `unsupported platform: ${t.platform}/${t.arch}\n` +
        '@sageox/ox ships prebuilt binaries for: darwin/x64, darwin/arm64, linux/x64, ' +
        'linux/arm64, freebsd/x64.\n' +
        'For other targets (including Windows) install from source:\n' +
        `  go install github.com/${REPO}/cmd/ox@latest\n` +
        '  or see https://sageox.ai/docs/cli'
    );
  }

  if (!force && isInstalled()) {
    return BINARY_PATH;
  }

  const name = assetName(VERSION, t);
  const archiveUrl = `${releaseBaseUrl()}/${name}`;
  const checksumsUrl = `${releaseBaseUrl()}/checksums.txt`;

  log(`downloading ${name} (v${VERSION})...`);
  const archive = await fetchBuffer(archiveUrl);

  // Verify integrity BEFORE extracting or installing anything. This mirrors
  // scripts/install.sh, which treats a missing or failing checksum as a HARD
  // failure — the wrapper is commonly run via `npx`, so integrity is not advisory.
  // (Note: this is the SAME check install.sh performs — SHA-256 against the
  // release checksums.txt. The Ed25519 signatures produced by scripts/sign-manifest
  // protect the binary's compiled-in data at runtime, not the tarball on the wire.)
  log('verifying checksum...');
  const checksumsText = (await fetchBuffer(checksumsUrl)).toString('utf8');
  const expected = findChecksum(checksumsText, name);
  if (!expected) {
    throw new Error(
      `release checksums.txt does not list ${name}; refusing to install an unverified binary`
    );
  }
  const actual = crypto.createHash('sha256').update(archive).digest('hex').toLowerCase();
  if (actual !== expected) {
    throw new Error(
      `checksum mismatch for ${name}\n` +
        `  expected: ${expected}\n` +
        `  actual:   ${actual}\n` +
        'refusing to install a corrupt or tampered binary'
    );
  }

  // Extract only the `ox` binary. Every supported platform (darwin/linux/freebsd)
  // ships GNU/BSD tar, and this is exactly what install.sh uses (tar -xzf), so we
  // avoid pulling a tar implementation into the dependency tree.
  fs.mkdirSync(VENDOR_DIR, { recursive: true });
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'sageox-ox-'));
  try {
    const archivePath = path.join(tmp, name);
    fs.writeFileSync(archivePath, archive);

    const res = spawnSync('tar', ['-xzf', archivePath, '-C', tmp, 'ox'], { stdio: 'inherit' });
    if (res.error) throw res.error;
    if (res.status !== 0) {
      throw new Error(`failed to extract ox from ${name} (tar exit ${res.status})`);
    }

    const extracted = path.join(tmp, 'ox');
    if (!fs.existsSync(extracted)) {
      throw new Error(`archive ${name} did not contain an 'ox' binary`);
    }

    fs.copyFileSync(extracted, BINARY_PATH);
    fs.chmodSync(BINARY_PATH, 0o755);
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }

  log(`installed ox v${VERSION} -> ${BINARY_PATH}`);
  return BINARY_PATH;
}

// Ensure the binary exists (download+verify if the postinstall was skipped, e.g.
// `npm install --ignore-scripts`). Used by the launcher for lazy first-run fetch.
async function ensure() {
  if (isInstalled()) return BINARY_PATH;
  return download();
}

// Exec the native ox binary, forwarding stdio and exit status. Never returns.
function run(args) {
  const res = spawnSync(BINARY_PATH, args, { stdio: 'inherit' });
  if (res.error) throw res.error;
  // Terminated by a signal (e.g. Ctrl-C): exit non-zero rather than reporting 0.
  if (res.status === null) {
    process.exit(1);
  }
  process.exit(res.status);
}

module.exports = { download, ensure, run, isInstalled, BINARY_PATH, VERSION };
