// framePlan prepares a stored plan for a host that frames it with
// sandbox="allow-scripts" under an inline-only script policy (README, "Frame a
// plan"). That policy blocks the jsDelivr <script> plans load Mermaid with, so
// the page gets ox's vendored build inline instead: the file
// `ox plan render --artifact` inlines, so every host draws a diagram with the
// same Mermaid.

// build.mjs defines both as string literals.
declare const __OX_MERMAID__: string;
declare const __OX_DOCUMENT_TOOLS__: string;

// The module import is anchored to its <script type="module"> tag, so the
// same line shown as text, where the tag is escaped, is left alone.
const CLASSIC_TAG =
  /<script\b[^<>]*\bsrc\s*=\s*["']https:\/\/cdn\.jsdelivr\.net\/npm\/mermaid(?:@[^"'/<]+)?\/dist\/mermaid(?:\.min)?\.js["'][^<>]*>/gi;
const MODULE_IMPORT =
  /(<script\b[^<>]*\btype\s*=\s*["']?module["']?[^<>]*>[^<]*?)\bimport\s+mermaid\s+from\s*["']https:\/\/cdn\.jsdelivr\.net\/npm\/mermaid(?:@[^"'/<]+)?\/dist\/mermaid\.esm(?:\.min)?\.mjs["']\s*;?/g;
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
  let out = html;
  if (DIAGRAM.test(out)) {
    let inlined = false;
    let imported = false;
    out = out
      .replace(CLASSIC_TAG, () => {
        // a page that loads Mermaid twice gets it once
        const tag = '<script>' + (inlined ? '' : MERMAID);
        inlined = true;
        return tag;
      })
      .replace(MODULE_IMPORT, (_, open: string) => {
        imported = true;
        return open + 'const mermaid = globalThis.mermaid;';
      });
    // after the page: a classic script runs while the page parses, before any module script
    if (imported && !inlined) out += '<script>' + MERMAID + '</script>';
  }
  if (!opts.documentTools) return out;
  if (opts.instance !== undefined && !INSTANCE.test(opts.instance)) throw new Error('framePlan: instance must be 8-64 characters of [A-Za-z0-9_-]');
  const tag = (opts.instance ? '<script data-instance="' + opts.instance + '">' : '<script>') + __OX_DOCUMENT_TOOLS__ + '</script>';
  // Splice, never String.replace: the script contains "$&". The last </body>,
  // because an earlier one can sit inside the plan's own scripts.
  const at = out.toLowerCase().lastIndexOf('</body>');
  return at < 0 ? out + tag : out.slice(0, at) + tag + out.slice(at);
}
