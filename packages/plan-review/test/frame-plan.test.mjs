// framePlan, run with a stand-in Mermaid build and document tools script in
// place of the real ones build.mjs defines.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { load } from './load.mjs';

const MERMAID = 'globalThis.mermaid={};"<!--</script></body>"';
const TOOLS = 'tools("$&")';
const { framePlan } = await load('frame-plan.ts', { __OX_MERMAID__: JSON.stringify(MERMAID), __OX_DOCUMENT_TOOLS__: JSON.stringify(TOOLS) });
const ESCAPED = 'globalThis.mermaid={};"<\\!--<\\/script></body>"';
const DIAGRAM = '<pre class="mermaid">graph TD; A--&gt;B</pre>';
const CDN = 'https://cdn.jsdelivr.net/npm/mermaid';

// Failure prevented: a framed plan's diagrams stay as source text, or the
// inlined build ends its own <script> early.
test('a CDN Mermaid tag becomes the escaped vendored build on a page with a diagram', () => {
  const tag = `<script src="${CDN}@11.6.0/dist/mermaid.min.js" integrity="sha384-x" crossorigin="anonymous"></script>`;
  assert.equal(framePlan(DIAGRAM + tag + '<script>mermaid.run()</script>'), DIAGRAM + '<script>' + ESCAPED + '</script><script>mermaid.run()</script>');
});

// Failure prevented: an ES-module page imports from a blocked CDN, the same
// import line shown as text in a code block is rewritten, or the document
// tools land inside the appended build, whose own "</body>" comes last.
test('an ES-module import reads the global; the build follows the page and the tools stay in it', () => {
  const page = `<body>${DIAGRAM}<pre>&lt;script type="module"&gt;import mermaid from '${CDN}@11/dist/mermaid.esm.min.mjs'</pre>` +
    `<script type="module">import mermaid from '${CDN}@11/dist/mermaid.esm.min.mjs';\nmermaid.run()</script></body></html>`;
  const want = page
    .replace(`import mermaid from '${CDN}@11/dist/mermaid.esm.min.mjs';`, 'const mermaid = globalThis.mermaid;')
    .replace('</body>', () => '<script>' + TOOLS + '</script></body>');
  assert.equal(framePlan(page, { documentTools: true }), want + '<script>' + ESCAPED + '</script>');
});

// Failure prevented: a "<" earlier in the module script hides its import, and
// the framed plan draws no diagram.
test('the import is found anywhere in its module script', () => {
  const imp = `import mermaid from '${CDN}/dist/mermaid.esm.min.mjs';`;
  const page = `${DIAGRAM}<script type="module">const smaller = 1 < 2;\n${imp}\nmermaid.run()</script>`;
  assert.equal(framePlan(page), page.replace(imp, 'const mermaid = globalThis.mermaid;') + '<script>' + ESCAPED + '</script>');
});

// Failure prevented: a data-src or data-type attribute, or a type that only
// starts with "module", is read as the script's own, and the page loses a
// script or has an inert one's text rewritten.
test('only a script tag\'s own src and type count', () => {
  const imp = `import mermaid from '${CDN}/dist/mermaid.esm.min.mjs';`;
  const page = DIAGRAM + `<script data-src="${CDN}/dist/mermaid.min.js">var a = 1</script>` +
    `<script type="module-template">${imp}</script><script data-type=module type=text/plain>${imp}</script>`;
  assert.equal(framePlan(page), page);
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

// Failure prevented: an unterminated <script> tag, or many module scripts that
// never close, pin the host's thread (the patterns this replaced took ~45 s on
// 1 MiB).
test('an unterminated script frames in linear time', () => {
  for (const run of [' type=module', ` src="${CDN}/dist/mermaid.js"`, ' type=module><script']) {
    const page = DIAGRAM + '<script' + run.repeat(Math.ceil((1 << 20) / run.length));
    const start = performance.now();
    assert.equal(framePlan(page, { documentTools: true }), page + '<script>' + TOOLS + '</script>');
    const ms = performance.now() - start;
    assert.ok(ms < 1000, `${run.trim()}: ${Math.round(ms)} ms on 1 MiB`);
  }
});

// Failure prevented: a dotted capital I lengthens the lowercased page, the
// splice lands inside </body>, and the tools print as page text.
test('the tools splice before </body> after a dotted capital I', () => {
  const page = '<body><p>İİİ</p></body></html>';
  assert.equal(framePlan(page, { documentTools: true }), '<body><p>İİİ</p><script>' + TOOLS + '</script></body></html>');
});
