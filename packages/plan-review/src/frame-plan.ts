// framePlan prepares a stored plan for a host that frames it with
// sandbox="allow-scripts" under an inline-only script policy (README, "Frame a
// plan"). That policy blocks the jsDelivr <script> plans load Mermaid with, so
// the page gets ox's vendored build inline instead: the file
// `ox plan render --artifact` inlines, so every host draws a diagram with the
// same Mermaid.

// build.mjs defines both as string literals.
declare const __OX_MERMAID__: string;
declare const __OX_DOCUMENT_TOOLS__: string;

// A host runs this on untrusted pages, often on its request path, so every
// scan is linear: script tags are found in one pass, each ending at its own
// ">", and the CDN checks run only inside a tag or a module script's text up
// to its next "<". A pattern with an unbounded run on both sides of a literal
// backtracks quadratically on an unterminated tag.
const SCRIPT_TAG = /<script\b[^<>]*>/gi;
const CDN_SRC =
  /\bsrc\s*=\s*["']https:\/\/cdn\.jsdelivr\.net\/npm\/mermaid(?:@[^"'/<]+)?\/dist\/mermaid(?:\.min)?\.js["']/i;
// The module import is anchored to its <script type="module"> tag, so the
// same line shown as text, where the tag is escaped, is left alone.
const MODULE_TYPE = /\btype\s*=\s*["']?module/;
const MODULE_IMPORT =
  /\bimport\s+mermaid\s+from\s*["']https:\/\/cdn\.jsdelivr\.net\/npm\/mermaid(?:@[^"'/<]+)?\/dist\/mermaid\.esm(?:\.min)?\.mjs["']\s*;?/;
const BODY_CLOSE = /<\/body>/gi;
// ox's renderer adds the CDN tag to every plan; only a page with a diagram
// needs the 2.7 MB build.
const DIAGRAM = /\bclass\s*=\s*["'][^"']*\bmermaid\b/i;
const INSTANCE = /^[A-Za-z0-9_-]{8,64}$/;

// Inside a <script>, "</script" ends the element and "<!--" changes how the
// parser reads to its end; in a JS string or regex the escaped forms are the
// same text. The vendored build has no "</script" today, and three "<!--".
const MERMAID = __OX_MERMAID__.replace(/<(\/script|!--)/gi, '<\\$1');

export interface FramePlanOptions {
  /** Splice the document tools in, so a host can review the plan. */
  documentTools?: boolean;
  /** The frame load's instance id, for a host that sets the page as srcdoc; a host serving the page from a URL passes it as #i=<instance> instead. */
  instance?: string;
}

/** framePlan returns the stored plan html ready to frame. A page without Mermaid changes only by the document tools splice. */
export function framePlan(html: string, opts: FramePlanOptions = {}): string {
  // Splice into the page as stored: the build this inlines or appends below
  // carries a "</body>" of its own.
  const out = opts.documentTools ? spliceTools(html, opts.instance) : html;
  return DIAGRAM.test(html) ? inlineMermaid(out) : out;
}

function inlineMermaid(html: string): string {
  const parts: string[] = [];
  let from = 0;
  let inlined = false;
  let imported = false;
  for (const m of html.matchAll(SCRIPT_TAG)) {
    const end = m.index + m[0].length;
    if (CDN_SRC.test(m[0])) {
      // a page that loads Mermaid twice gets it once
      parts.push(html.slice(from, m.index), '<script>' + (inlined ? '' : MERMAID));
      inlined = true;
      from = end;
    } else if (m[0].startsWith('<script') && MODULE_TYPE.test(m[0])) {
      const next = html.indexOf('<', end);
      const imp = MODULE_IMPORT.exec(html.slice(end, next < 0 ? html.length : next));
      if (imp) {
        parts.push(html.slice(from, end + imp.index), 'const mermaid = globalThis.mermaid;');
        imported = true;
        from = end + imp.index + imp[0].length;
      }
    }
  }
  parts.push(html.slice(from));
  // after the page: a classic script runs while the page parses, before any module script
  return parts.join('') + (imported && !inlined ? '<script>' + MERMAID + '</script>' : '');
}

function spliceTools(html: string, instance: string | undefined): string {
  if (instance !== undefined && !INSTANCE.test(instance)) throw new Error('framePlan: instance must be 8-64 characters of [A-Za-z0-9_-]');
  const tag = (instance ? '<script data-instance="' + instance + '">' : '<script>') + __OX_DOCUMENT_TOOLS__ + '</script>';
  // Splice, never String.replace: the script contains "$&". The last </body>,
  // because an earlier one can sit inside the plan's own scripts. Found in the
  // page itself: lowercasing can lengthen it ("İ" becomes two code units).
  let at = -1;
  for (const m of html.matchAll(BODY_CLOSE)) at = m.index;
  return at < 0 ? html + tag : html.slice(0, at) + tag + html.slice(at);
}
