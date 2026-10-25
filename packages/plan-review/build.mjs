// Builds @sageox/plan-review from src/ into
//   dist/                   the npm package: ESM entries and their types, the
//                           framed document tools script (as a file and as a
//                           string), its stylesheet, framePlan with ox's vendored
//                           Mermaid build, and manifest.json
//   internal/plan/assets/   the ox CLI's review page: review.js, review.css
//                           and review.manifest.json, committed so that
//                           building ox never needs node
// A manifest names the package version, the protocol, a digest of the source
// that produced it, and the SHA-256 of each file it lists.
//
//   node build.mjs           build both
//   node build.mjs --check   build in memory; fail when the committed CLI
//                            assets are not what this source builds

import { build } from 'esbuild';
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = dirname(fileURLToPath(import.meta.url));
const cliAssets = join(root, '..', '..', 'internal', 'plan', 'assets');
// framePlan inlines the Mermaid build artifact renders already inline
const mermaid = join('..', '..', 'internal', 'plan', 'assets', 'mermaid.min.js');
const dist = join(root, 'dist');
const check = process.argv.includes('--check');
const pkg = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'));
const sha256 = (data) => createHash('sha256').update(data).digest('hex');
const tsc = (config) => execFileSync(join(root, 'node_modules', '.bin', 'tsc'), ['-p', join(root, config)], { stdio: 'inherit' });

// sourceDigest covers every file that decides the output bytes.
function sourceDigest() {
  const files = ['build.mjs', 'package-lock.json', 'package.json', 'tsconfig.build.json', 'tsconfig.json', mermaid];
  const walk = (dir) => readdirSync(join(root, dir), { withFileTypes: true }).forEach((e) => {
    if (e.isDirectory()) walk(dir + '/' + e.name);
    else files.push(dir + '/' + e.name);
  });
  walk('src');
  const h = createHash('sha256');
  files.sort().forEach((f) => h.update(f + '\0').update(readFileSync(join(root, f))).update('\0'));
  return h.digest('hex');
}

async function bundle(entry, format, options = {}) {
  const r = await build({
    entryPoints: [join(root, 'src', entry)],
    bundle: true,
    format,
    target: 'es2020',
    charset: 'utf8',
    legalComments: 'none',
    write: false,
    logLevel: 'error',
    ...options,
  });
  return r.outputFiles[0].text;
}

// refuse fails the build when js contains any of the strings.
function refuse(name, js, strings, why) {
  for (const s of strings) {
    if (js.toLowerCase().includes(s.toLowerCase())) throw new Error(`${name} contains "${s}": ${why}`);
  }
}

tsc('tsconfig.json'); // type-check all of src

const notice = 'Generated from packages/plan-review by `make plan-review`. Edit the source there, not this file.';
const documentCss = readFileSync(join(root, 'src', 'document.css'), 'utf8');
const hostCss = readFileSync(join(root, 'src', 'host.css'), 'utf8');
const framed = await bundle('frame.ts', 'iife', { loader: { '.css': 'text' } });
const cli = {
  'review.js': '// ' + notice + '\n' + (await bundle('local.ts', 'iife')),
  'review.css': '/* ' + notice + ' */\n' + documentCss + hostCss,
};
const npm = {
  'index.js': await bundle('index.ts', 'esm'),
  'document-tools.js': await bundle('document-tools.ts', 'esm'),
  'document-tools.iife.js': framed,
  'document-tools-script.js': 'export default ' + JSON.stringify(framed) + ';\n',
  'document-tools-script.d.ts': 'declare const script: string;\nexport default script;\n',
  'document-tools.css': documentCss,
  'host.css': hostCss,
  'frame-plan.js': await bundle('frame-plan.ts', 'esm', {
    define: { __OX_MERMAID__: JSON.stringify(readFileSync(join(root, mermaid), 'utf8')), __OX_DOCUMENT_TOOLS__: JSON.stringify(framed) },
  }),
};

for (const [name, js] of [['review.js', cli['review.js']], ['document-tools.iife.js', framed]]) {
  refuse(name, js, ['</script', '<!--'], 'it is inlined into a <script> element');
  refuse(name, js, ['alert(', 'confirm(', 'prompt('], 'a sandboxed frame blocks dialogs; report inline');
}
refuse('document-tools.iife.js', framed,
  ['localStorage', 'sessionStorage', 'document.cookie', 'indexedDB', 'fetch(', 'XMLHttpRequest', 'EventSource', 'WebSocket', 'sendBeacon',
    'serviceWorker', 'window.open', 'location.href', 'location.assign', 'location.replace', 'eval(', 'new Function'],
  'an opaque-origin frame has no storage, the host CSP no network or eval, and the tools never navigate');
refuse('document-tools.iife.js', framed, ['ox-review:', 'mountHostControls', 'X-Review-Token'],
  'the review controls, the outbox and credentials stay in the trusted host, never in the plan document');

const { PROTOCOL } = await import('data:text/javascript;base64,' + Buffer.from(npm['index.js']).toString('base64'));
const digest = sourceDigest();
function manifest(files) {
  const m = { package: pkg.name, version: pkg.version, protocol: PROTOCOL, source_sha256: digest, files: {} };
  Object.keys(files).sort().forEach((f) => { m.files[f] = sha256(files[f]); });
  return JSON.stringify(m, null, 2) + '\n';
}
cli['review.manifest.json'] = manifest({ 'review.css': cli['review.css'], 'review.js': cli['review.js'] });

if (check) {
  const stale = Object.keys(cli).filter((f) => {
    try { return readFileSync(join(cliAssets, f), 'utf8') !== cli[f]; } catch { return true; }
  });
  if (stale.length) {
    console.error('internal/plan/assets is stale: ' + stale.join(', ') + '. Run `make plan-review` and commit the result.');
    process.exit(1);
  }
  console.log('internal/plan/assets matches packages/plan-review');
} else {
  rmSync(dist, { recursive: true, force: true });
  mkdirSync(dist);
  tsc('tsconfig.build.json'); // dist/*.d.ts
  for (const f of readdirSync(dist)) npm[f] = readFileSync(join(dist, f), 'utf8');
  for (const [f, s] of Object.entries(npm)) writeFileSync(join(dist, f), s);
  writeFileSync(join(dist, 'manifest.json'), manifest(npm));
  for (const [f, s] of Object.entries(cli)) writeFileSync(join(cliAssets, f), s);
  console.log('built dist/ and internal/plan/assets/review.{js,css,manifest.json}');
}
