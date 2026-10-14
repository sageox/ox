// framePlan, run with a stand-in Mermaid build and document tools script in
// place of the real ones build.mjs defines.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { load } from './load.mjs';

const MERMAID = 'globalThis.mermaid={};"<!--</script>"';
const TOOLS = 'tools("$&")';
const { framePlan } = await load('frame-plan.ts', { __OX_MERMAID__: JSON.stringify(MERMAID), __OX_DOCUMENT_TOOLS__: JSON.stringify(TOOLS) });
const ESCAPED = 'globalThis.mermaid={};"<\\!--<\\/script>"';
const DIAGRAM = '<pre class="mermaid">graph TD; A--&gt;B</pre>';
const CDN = 'https://cdn.jsdelivr.net/npm/mermaid';

// Failure prevented: a framed plan's diagrams stay as source text, or the
// inlined build ends its own <script> early.
test('a CDN Mermaid tag becomes the escaped vendored build on a page with a diagram', () => {
  const tag = `<script src="${CDN}@11.6.0/dist/mermaid.min.js" integrity="sha384-x" crossorigin="anonymous"></script>`;
  assert.equal(framePlan(DIAGRAM + tag + '<script>mermaid.run()</script>'), DIAGRAM + '<script>' + ESCAPED + '</script><script>mermaid.run()</script>');
});

// Failure prevented: an ES-module page imports from a blocked CDN, or the same
// import line shown as text in a code block is rewritten.
test('an ES-module import reads the global, and the build follows the page', () => {
  const page = `${DIAGRAM}<pre>&lt;script type="module"&gt;import mermaid from '${CDN}@11/dist/mermaid.esm.min.mjs'</pre>` +
    `<script type="module">import mermaid from '${CDN}@11/dist/mermaid.esm.min.mjs';\nmermaid.run()</script></html>`;
  assert.equal(framePlan(page), page.replace(`import mermaid from '${CDN}@11/dist/mermaid.esm.min.mjs';`, 'const mermaid = globalThis.mermaid;') + '<script>' + ESCAPED + '</script>');
});

// Failure prevented: every plan ox renders carries the 2.7 MB build, diagram or not.
test('a page with no diagram is left as it is', () => {
  const page = `<p>No diagram</p><script src="${CDN}@11.6.0/dist/mermaid.min.js"></script>`;
  assert.equal(framePlan(page), page);
});

// Failure prevented: the document tools land inside the plan's own script, or
// "$&" in them expands into the matched text.
test('the document tools go before the last </body>, verbatim', () => {
  const page = '<body><script>var s = "</body>";</script><p>x</p></body></html>';
  assert.equal(framePlan(page, { documentTools: true, instance: 'load-0001' }),
    '<body><script>var s = "</body>";</script><p>x</p><script data-instance="load-0001">tools("$&")</script></body></html>');
  assert.throws(() => framePlan(page, { documentTools: true, instance: '"><x' }));
});
