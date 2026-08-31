#!/usr/bin/env node
'use strict';

// postinstall entry point: download + verify + install the platform-native ox
// binary. Fails loudly (non-zero exit) on an unsupported platform, a network
// failure, or a checksum mismatch — we never leave an unverified or partial
// binary in place.

const { download } = require('./lib/binary');

// Escape hatch for CI / offline mirrors / `npm install --ignore-scripts` flows:
// skip the network fetch at install time. The launcher (bin/ox.js) then lazily
// downloads and verifies on first run instead.
if (process.env.OX_NPM_SKIP_DOWNLOAD === '1') {
  process.stderr.write(
    '[@sageox/ox] OX_NPM_SKIP_DOWNLOAD=1 set; skipping binary download (will fetch on first run)\n'
  );
  process.exit(0);
}

download().catch((err) => {
  process.stderr.write(`\n[@sageox/ox] installation failed: ${err.message}\n`);
  process.exit(1);
});
