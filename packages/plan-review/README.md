# @sageox/plan-review

Plan review in the browser, shared by the ox CLI and any host that shows
SageOx plans. It has two halves that talk only through a small message
protocol:

- **Document tools** run inside the plan. They find what a reviewer points at
  (an element, or the words they selected) and paint the marks the host sends.
  They read no storage and make no request, so they run in an opaque-origin
  frame.
- **The host** is trusted. It owns review mode, the composer, drafts,
  identity and submission. Nothing a document sends can submit, approve,
  resolve or name a reviewer.

The ox CLI does not install this package. It builds the same source into
`internal/plan/assets/review.js` and `review.css`, committed with
`review.manifest.json`, so building ox needs neither node nor a registry.

| Export | What it is |
|---|---|
| `@sageox/plan-review` | `PROTOCOL`, the message types, `parseDocumentMessage`, `parseHostMessage`, and `connectFrame`, the host's end of a framed plan |
| `@sageox/plan-review/document-tools-script` | The document tools for a framed plan, as a string to inline (default export) |
| `@sageox/plan-review/document-tools.iife.js` | The same script, as a file |
| `@sageox/plan-review/document-tools` | `startDocumentTools` and `localPorts`, for a plan in the host's own window |
| `@sageox/plan-review/document-tools.css` | The marks' styles for that same-window case; the framed script carries its own |
| `@sageox/plan-review/manifest.json` | Version, protocol, a digest of the source, and the SHA-256 of every other file in `dist/` |

## Install

The package is published to GitHub Packages under the `@sageox` scope. It is
public, as is this repository, and has no dependencies.

```ini
# .npmrc
@sageox:registry=https://npm.pkg.github.com
//npm.pkg.github.com/:_authToken=${NODE_AUTH_TOKEN}
```

GitHub Packages asks for a token even to install. Use one with
`read:packages`, or, in GitHub Actions, `GITHUB_TOKEN` once the package's
settings grant your repository read access.

Pin the exact version and commit your lockfile:

```sh
npm install --save-exact @sageox/plan-review@0.1.0
```

Pinning matters because the host end and the document script must come from
one version: the protocol moves with the package. `manifest.json` names the
protocol a build speaks.

## Frame a plan

1. **Name the frame load.** Mint an instance id for every load: 8–64
   characters of `[A-Za-z0-9_-]`. Messages from an earlier load of the same
   frame carry the old id and are dropped.

2. **Connect before the frame loads.** The document says `ready` once.

   ```js
   import { connectFrame } from '@sageox/plan-review';
   import documentTools from '@sageox/plan-review/document-tools-script';

   const instance = crypto.randomUUID();
   const port = connectFrame(iframe, instance);
   port.listen(onDocumentMessage);
   ```

3. **Inject the script before the plan's last `</body>`** and frame it with
   `sandbox="allow-scripts"` only. Splice the text rather than call `replace`
   with a string: the script contains `$&`, which a replacement string expands,
   and an earlier `</body>` can sit inside the plan's own scripts.

   ```js
   const tag = `<script data-instance="${instance}">${documentTools}</script>`;
   const last = [...planHTML.matchAll(/<\/body>/gi)].pop();
   const html = last ? planHTML.slice(0, last.index) + tag + planHTML.slice(last.index) : planHTML + tag;
   iframe.setAttribute('sandbox', 'allow-scripts');
   iframe.srcdoc = html;
   ```

   The script contains no `</script` and no `<!--` (the build refuses either),
   so it inlines as is. A document served from a URL may carry the id in the
   frame URL's fragment, `#i=<instance>`, instead of the attribute.

4. **Keep the frame opaque.** The script runs under this policy, which is the
   one to serve a framed plan with:

   ```
   sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline';
   style-src 'unsafe-inline'; img-src data:; font-src data:; connect-src 'none';
   form-action 'none'; base-uri 'none'; object-src 'none'
   ```

   Do not add `allow-same-origin`: the plan is untrusted content. A `srcdoc`
   frame inherits the host page's CSP, so that policy must allow the inline
   script too. Serve a plan from a URL with `frame-ancestors` naming your host,
   so no other page can frame it.

   The plan's own scripts see every message you send it, and no CSP stops a
   frame from navigating itself, which can carry them away. Send only what the
   plan may know: the protocol carries no reviewer names and no notes, and
   your composer and rail stay in your page.

5. **Drive it.** `connectFrame` takes the first `ready` from that frame's
   window, from the opaque origin `"null"`, for this instance and protocol.
   It carries a `MessagePort` the document opened, and every later message
   travels on that port, so a page the frame navigates to afterwards is not
   heard. Each message must have the shape below, and your handler gets a
   fresh object holding only these fields.

   | Host → document | Effect |
   |---|---|
   | `{type:'mode', on, open}` | Review mode on or off, and whether your composer is open (so Esc is the document's to forward) |
   | `{type:'paint', items}` | Paint these marks; answered by `placed` |
   | `{type:'focus', anchor}` | Tint the words this anchor's note is about; `null` clears |
   | `{type:'reveal', anchor, nth}` | Scroll to the `nth` placed mark with this anchor and flash it |

   | Document → host | Meaning |
   |---|---|
   | `{type:'ready'}` | Listening: send `mode` and `paint` |
   | `{type:'target', target}` | In review mode the reviewer picked an element (`h` anchor) or highlighted words (`q` anchor, with `quote`). `target.rect` is where your composer opens below, in the frame's viewport pixels |
   | `{type:'refused', reason}` | A highlight that cannot anchor: `cross-section`, or `repeated` with the `quote` and its `count` |
   | `{type:'dismiss'}` | A click that picked nothing, in review mode or out of it: close your composer |
   | `{type:'placed', items}` | Every painted mark in reading order, with its section and label. A mark you painted that is not here has lost its text |
   | `{type:'key', key}` | `r` or `Escape`, pressed inside the frame |

   A `PaintItem` is `{anchor, kind: 'unsent'|'committed', status, state?, glyph,
   tint, quote?, section?, label?}`, with `glyph` inserted as text. Only
   anchors the document computes, `h` or `q` plus eight hex digits, can be
   painted; a message carrying any other is dropped whole, so list the rest
   (drawings, say) only in your own UI. See `src/protocol.ts`.

## A plan in your own window

This is how the CLI's review page runs:

```js
import { localPorts, startDocumentTools } from '@sageox/plan-review/document-tools';
import '@sageox/plan-review/document-tools.css';

const { host, document: tools } = localPorts();
host.listen(onDocumentMessage); // before starting: the tools say ready once
startDocumentTools(tools);
```

Messages between local ports are plain function calls, so a click has opened
your composer by the time its handler returns.

## Anchors

An element's anchor is `"h"` plus FNV-1a over its normalized section heading
and text. A highlight's anchor is `"q"` plus the same hash over the heading
and its words. Every Ledger already holds anchors computed this way, and
`AnchorFor` in `github.com/sageox/ox/pkg/planreview` computes the same ones in
Go. The tests check both against `pkg/planreview/testdata/anchors.json`.

## Developing

Edit `src/`, never `internal/plan/assets/review.*`.

- `make plan-review` builds `dist/` and regenerates the CLI's assets.
- `make plan-review-check` is what CI runs: the assets match the source, the
  tests pass, and the packed tarball is reproducible, installs offline, loads
  and type-checks.
- `make plan-review && make test-browser` runs the real-Chrome proofs, the
  framed ones included.

## Releasing

1. Raise `version` in `package.json`, run `make plan-review`, and merge the
   source with the regenerated assets.
2. Tag that commit `packages/plan-review/v<version>` and push the tag.
3. Run the **Publish @sageox/plan-review** workflow from `main` with the
   version. It checks out the tag, refuses one whose commit is not on `main`,
   runs `make plan-review-check`, publishes under the dist-tag `latest`
   (`next` for a prerelease), and writes the tarball's integrity to the run
   summary.
