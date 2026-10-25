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
  // npm versions return either an array or an object keyed by package name.
  const [r] = Object.values(JSON.parse(run('npm', ['pack', '--json', '--pack-destination', dest], root)));
  return join(dest, r.filename);
}

const consumerJS = `
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { PROTOCOL, connectFrame, forgetAccount, mountHostControls, parseDocumentMessage } from '@sageox/plan-review';
import { localPorts, startDocumentTools } from '@sageox/plan-review/document-tools';
import script from '@sageox/plan-review/document-tools-script';
import { framePlan } from '@sageox/plan-review/frame-plan';
import manifest from '@sageox/plan-review/manifest.json' with { type: 'json' };

const require = createRequire(import.meta.url);
assert.equal(manifest.protocol, PROTOCOL);
assert.equal(typeof connectFrame, 'function');
assert.equal(typeof mountHostControls, 'function');
assert.equal(typeof forgetAccount, 'function');
assert.equal(typeof startDocumentTools, 'function');
assert.equal(parseDocumentMessage({ ox: 'review', v: PROTOCOL, instance: 'i-000001', type: 'ready' }, 'i-000001').type, 'ready');
assert.ok(localPorts().host);
assert.equal(script, readFileSync(require.resolve('@sageox/plan-review/document-tools.iife.js'), 'utf8'));
require.resolve('@sageox/plan-review/document-tools.css');
require.resolve('@sageox/plan-review/host.css');
// the real build and script: an ES-module page keeps the tools before its own
// </body>, ahead of the appended build, whose text has a </body> of its own
const page = "<body><pre class='mermaid'>graph TD; A--&gt;B</pre><script type='module'>import mermaid from 'https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.esm.min.mjs';</script></body></html>";
const framed = framePlan(page, { documentTools: true });
assert.ok(framed.includes('<script>' + script + '</script></body></html><script>'));
assert.ok(framed.includes('version:"11.6.0"'));
assert.ok(!framed.includes('cdn.jsdelivr.net'));
assert.equal(framePlan('<p>no diagram</p>'), '<p>no diagram</p>');
const dist = require.resolve('@sageox/plan-review/manifest.json').replace(/manifest\\.json$/, '');
for (const [f, sum] of Object.entries(manifest.files)) {
  assert.equal(createHash('sha256').update(readFileSync(dist + f)).digest('hex'), sum, f);
}
`;

const consumerTS = `
import { PROTOCOL, connectFrame, forgetAccount, mountHostControls, type DocumentMessage, type HostMessage, type Receipt, type Transport } from '@sageox/plan-review';
import { localPorts, startDocumentTools } from '@sageox/plan-review/document-tools';
import script from '@sageox/plan-review/document-tools-script';
import { framePlan, type FramePlanOptions } from '@sageox/plan-review/frame-plan';

const port = connectFrame(document.createElement('iframe'), 'instance-1');
port.listen((m: DocumentMessage) => {
  if (m.type === 'target') console.log(m.target.anchor, m.target.rect.bottom);
  if (m.type === 'capture') console.log(m.capture.ellipse.radii[0], m.capture.targets[0].anchor);
  if (m.type === 'placed') console.log(m.items.map((p) => p.outcome));
});
const mode: HostMessage = { type: 'mode', on: true, open: false, draw: false };
port.send(mode);
const transport: Transport = { send: async (op): Promise<Receipt> => ({ state: 'owned', id: op.id, synced: true }) };
const controls = mountHostControls(port, { account: 'acct', plan: 'pln_1', slug: 'plan', committed: [], transport, reviewer: 'Devon', frame: document.createElement('iframe'), revision: 'sha256:' + '0'.repeat(64) });
controls.update([{ anchor: 'h1234abcd', status: 'flag', state: 'open' }], 0);
controls.destroy();
forgetAccount(localStorage, 'acct');
port.close();
startDocumentTools(localPorts().document, { forwardKeys: false });
const opts: FramePlanOptions = { documentTools: true, instance: 'instance-1' };
const n: number = PROTOCOL + script.length + framePlan('<p>x</p>', opts).length;
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
