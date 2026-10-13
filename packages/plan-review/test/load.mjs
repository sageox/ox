// load bundles one src/ module with esbuild and imports it, so tests reach
// helpers the package does not export.
import { buildSync } from 'esbuild';
import { fileURLToPath } from 'node:url';

export function load(name) {
  const out = buildSync({ entryPoints: [fileURLToPath(new URL('../src/' + name, import.meta.url))], bundle: true, format: 'esm', write: false });
  return import('data:text/javascript;base64,' + Buffer.from(out.outputFiles[0].text).toString('base64'));
}
