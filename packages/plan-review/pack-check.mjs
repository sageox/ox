// pack-check: the package the way a consumer gets it, after `npm run build`.
// It packs twice and compares the tarballs (reproducible), installs one into an
// empty project with no network (no dependencies, private or otherwise), loads
// every export, checks each file against dist/manifest.json, and type-checks a
// consumer under nodenext.

import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = dirname(fileURLToPath(import.meta.url));
const tmp = mkdtempSync(join(tmpdir(), 'plan-review-pack-'));
const run = (cmd, args, cwd) => execFileSync(cmd, args, { cwd, encoding: 'utf8', stdio: ['ignore', 'pipe', 'inherit'] });
const sha256 = (data) => createHash('sha256').update(data).digest('hex');
function pack(name) {
  const dest = join(tmp, name);
  mkdirSync(dest);
  const [r] = JSON.parse(run('npm', ['pack', '--json', '--pack-destination', dest], root));
  return join(dest, r.filename);
}

const consumerJS = `
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { PROTOCOL, connectFrame, parseDocumentMessage } from '@sageox/plan-review';
import { localPorts, startDocumentTools } from '@sageox/plan-review/document-tools';
import script from '@sageox/plan-review/document-tools-script';
import manifest from '@sageox/plan-review/manifest.json' with { type: 'json' };

const require = createRequire(import.meta.url);
assert.equal(manifest.protocol, PROTOCOL);
assert.equal(typeof connectFrame, 'function');
assert.equal(typeof startDocumentTools, 'function');
assert.equal(parseDocumentMessage({ ox: 'review', v: PROTOCOL, instance: 'i-000001', type: 'ready' }, 'i-000001').type, 'ready');
assert.ok(localPorts().host);
assert.equal(script, readFileSync(require.resolve('@sageox/plan-review/document-tools.iife.js'), 'utf8'));
require.resolve('@sageox/plan-review/document-tools.css');
const dist = require.resolve('@sageox/plan-review/manifest.json').replace(/manifest\\.json$/, '');
for (const [f, sum] of Object.entries(manifest.files)) {
  assert.equal(createHash('sha256').update(readFileSync(dist + f)).digest('hex'), sum, f);
}
`;

const consumerTS = `
import { PROTOCOL, connectFrame, type DocumentMessage, type HostMessage } from '@sageox/plan-review';
import { localPorts, startDocumentTools } from '@sageox/plan-review/document-tools';
import script from '@sageox/plan-review/document-tools-script';

const port = connectFrame(document.createElement('iframe'), 'instance-1');
port.listen((m: DocumentMessage) => {
  if (m.type === 'target') console.log(m.target.anchor, m.target.rect.bottom);
});
const mode: HostMessage = { type: 'mode', on: true, open: false };
port.send(mode);
port.close();
startDocumentTools(localPorts().document, { forwardKeys: false });
const n: number = PROTOCOL + script.length;
console.log(n);
`;

try {
  const a = pack('a'), b = pack('b');
  if (sha256(readFileSync(a)) !== sha256(readFileSync(b))) throw new Error('npm pack produced different tarballs from one build');
  const app = join(tmp, 'app');
  mkdirSync(app);
  writeFileSync(join(app, 'package.json'), JSON.stringify({ name: 'consumer', private: true, type: 'module' }));
  run('npm', ['install', '--offline', '--ignore-scripts', '--no-audit', '--no-fund', a], app);
  writeFileSync(join(app, 'check.mjs'), consumerJS);
  run('node', ['check.mjs'], app);
  writeFileSync(join(app, 'check.ts'), consumerTS);
  writeFileSync(join(app, 'tsconfig.json'), JSON.stringify({
    compilerOptions: { module: 'nodenext', moduleResolution: 'nodenext', target: 'es2020', lib: ['es2020', 'dom'], strict: true, noEmit: true, skipLibCheck: false },
    files: ['check.ts'],
  }));
  run(join(root, 'node_modules', '.bin', 'tsc'), ['-p', join(app, 'tsconfig.json')], app);
  console.log('packed ' + a.split('/').pop() + ' (sha256 ' + sha256(readFileSync(a)) + '): reproducible, installs offline, loads and type-checks');
} finally {
  rmSync(tmp, { recursive: true, force: true });
}
