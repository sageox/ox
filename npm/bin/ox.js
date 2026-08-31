#!/usr/bin/env node
'use strict';

// npm `bin` launcher for @sageox/ox. This thin JS shim is what `npx @sageox/ox`
// and a global `ox` resolve to; it execs the downloaded native binary, forwarding
// argv, stdio, and exit status. Keeping a committed JS launcher (rather than
// pointing `bin` straight at the downloaded file) means the bin symlink is always
// valid — even before postinstall runs or if it was skipped.

const { ensure, run, isInstalled } = require('../lib/binary');

async function main() {
  if (!isInstalled()) {
    // postinstall skipped (e.g. --ignore-scripts) or previously failed: fetch now.
    try {
      await ensure();
    } catch (err) {
      process.stderr.write(`[@sageox/ox] ${err.message}\n`);
      process.exit(1);
    }
  }
  run(process.argv.slice(2));
}

main();
