# Plan review: original context for drawn annotations

**Status:** candidate (#1292). Production regions (#1293) stay gated on this
prototype *and* on consuming-host qualification (section 8). Builds on the
Ledger contract (`plan-review-ledger-contract.md`, frozen in #1288), which
`pkg/planreview` implements.

**Goal:** a reviewer circles part of a plan, the plan changes, and everyone can
still see what was circled. Coordinates alone cannot do this: the page reflows
with every edit, window and font. So a drawing records, beside its geometry,
the revision it was drawn on, the layout it was drawn in, the elements it
encloses, and a bounded copy of what lay under it.

| Where (`pkg/planreview/testdata/`, beside the Ledger contract's fixtures) | What |
|---|---|
| `drawn-context/revisions/{a,b,c}.html` | An authored plan (CSS tabs, `<details>`, table, inline SVG, Mermaid via CDN), then **b**: content added above every drawn element, and **c**: a drawn sentence reworded, a drawn Mermaid node renamed, a drawn list item duplicated |
| `drawn-context/proto/draw.js` | Document-tool prototype: the gesture, capture, placement, View original |
| `drawn-context/proto/host.html` | Opaque-origin harness: the trusted host that frames the plan |
| `drawn-context/proto/serve.go` | Local-renderer prototype: `go run ./pkg/planreview/testdata/drawn-context/proto/serve.go` from the repository root serves a revision top-level and framed (section 6) |
| `drawn-context/captures.json` | Ten hand-like strokes around parts of revision a, captured in Chrome |
| `drawn-context/expect.json` | Where each capture lands on each revision and layout, and the environment that observed it |
| `review-contract/region-items/` | Two drawings stored as round items. The module's `TestContractFixtures` and the CLI's `TestReviewContractFixtures` both assemble them as its `expect.json` says |

The evidence below was produced once with headless Chrome 155 on macOS 27,
driving `serve.go` and the harness with real pointer input. That driver is not
kept: it exercises prototype code, not ox. To replay the placements, run
`serve.go` and open
`/host/b?records=/fixtures/captures.json&expect=/fixtures/expect.json`. The
harness sizes its frame to the layout (`&layout=w1024`, default `base`) and
prints PASS or FAIL per capture. Offsets reproduce only with the same browser
and fonts as `expect.json`'s `environment`.

---

## 1. What a drawing records

```jsonc
{
  "v": 1, "kind": "region",
  "id": "r3-mermaid-nodes",           // the drawing's identity, independent of what it encloses
  "plan_revision": "sha256:95eb8bed…", // SHA-256 of the plan-of-record bytes the page displayed
  "created_at": "2026-10-10T20:00:02Z",
  "frame": "document",                // coordinate frame of every point, box and the ellipse
  "path": [[478.7, 339.9], …],        // the stroke as drawn, in order
  "ellipse": {"center": [597.6, 373.4], "radii": [155.5, 67.1],
              "rotation": 0, "start": -146.9, "dir": 1},  // what the reviewer sees (degrees)
  "bbox": [442.1, 306.3, 311, 134.2], // of the ellipse
  "layout": {
    "viewport": [1280, 800], "document": [1280, 1333], "dpr": 1, "scale": 1,
    "scroll": [0, 0], "theme": "dark", "fonts": "loaded",
    "open": [],                       // <details> open at capture, by summary-text hash ("dde1a7455")
    "checked": ["#tab-canary"]        // checked radios and checkboxes, by id or name=value
  },
  "targets": [                        // what the ellipse encloses
    {"anchor": "h4d3dcf2e", "tag": "p", "section": "Flow", "box": [496.9, 379.4, 45.8, 24], "cover": 1}
  ],
  "context": {                        // what lay under and around it (section 5)
    "items": [{"anchor": "h4d3dcf2e", "tag": "p", "box": [496.9, 379.4, 45.8, 24], "text": "Queue"}, …],
    "truncated": false
  }
}
```

- **Frame `document`:** CSS pixels from the top-left of the document (not the
  viewport), x right, y down. A point is `clientX + scrollX`,
  `clientY + scrollY` at the moment of the pointer event. Boxes are
  `getBoundingClientRect()` shifted the same way.
- **Revision:** the renderer that served the page stamps the digest of the
  bytes it rendered from (`plan.html` for an authored plan). For a page over
  1 MiB, that digest equals the `oid` of the plan's LFS pointer, so a host
  holding only a stub still knows which revision a drawing names. A framed
  document is untrusted, so the host stamps the revision of the instance it
  framed and ignores any claim from the document (section 6).
- **Shape:** a reviewer draws freehand and sees a perfect ellipse: the one
  with the stroke's area, centre and second moments (the stroke closed back to
  its start), squared to the page when within 10° of it, starting where the pen
  landed (`start`) and running the way it moved (`dir`). A stroke that encloses
  almost nothing, such as a line, gets the ellipse inscribed in its bounding
  box. The record keeps both: `ellipse` is what the reviewer saw and what
  decides the targets; `path` is the evidence it was fitted from.
- **Identity:** `id` is minted once by whoever freezes the drawing — the
  trusted host when framed, the local review chrome when top-level — never
  derived from the targets. Two drawings around one paragraph are two
  records. A host mints a UUID; the fixtures rename theirs to read as names.
- **Targets:** the innermost markable elements (review.js's selectors for
  generated and authored pages) with at least half of a 5×5 sample grid
  inside the ellipse. An ellipse inside one element, such as a node of a
  large diagram, falls back to the smallest element holding its center, with
  partial `cover`. A region with more than 16 innermost targets uses the
  outermost covered elements instead (a section rather than its every row).
  Still more than 16 is refused `too-large`, and nothing enclosed is
  `no-target`. `nth` and `of` record which occurrence when an anchor repeats.
- **Anchors** are review.js's content hash, `h` + FNV-1a over the normalized
  section heading and element text, with one difference: the text excludes
  `<style>` and `<script>`. Mermaid names each diagram `mermaid-<Date.now()>`
  and repeats that id in the SVG's `<style>`, so the text of any element
  containing a rendered diagram changes on every render. Two loads of
  revision a gave different `textContent` for the diagram; with `<style>`
  removed they matched. Two of revision a's 28 markable elements contain a
  diagram, and review.js anchors for those two changed on every load until
  the package dropped `<style>` and `<script>` from anchor text (#1324).
  For all 28, ox's Go `AnchorFor` over the same heading and text gives the
  prototype's anchor. Mermaid's HTML labels are `<p>` elements, so a stroke
  around two nodes targets their labels, plus an edge label it mostly
  encloses (capture `r3`), not the whole diagram.
- **Ink:** every host paints the ellipse the same way, from the record alone:
  a 12 px broad nib held at −12° (a hairline along the top and bottom,
  swelling where the ellipse turns through the vertical), tapered where the
  pen lands and lifts, and run 17° past its start so it closes like a pen-drawn
  O. While the pen is down the raw stroke shows faintly, then settles into the
  ellipse. Painting changes no record: retuning the nib left the regenerated
  `captures.json` byte-identical.

## 2. Scroll, zoom, resize, expanded sections

| Change | Defined behavior | Shown by |
|---|---|---|
| Scroll before drawing | No effect: the frame is the document | Visible captures made at scroll offsets 0–635 all place `exact` on a fresh load at the top |
| Scroll during a stroke | The point now under the still pointer is recorded, so the stroke stays on the content | `r8`: an 80 px scroll mid-stroke, targets the three table rows |
| Browser zoom | Changes the CSS viewport, so it is a resize | 125% of 1280 px gives the same outcomes as a 1024 px window |
| Device pixel ratio | No effect on CSS-px geometry | 2× DPR: every outcome the same as at 1× |
| Pinch zoom (visual viewport) | No effect: client coordinates and boxes stay in layout-viewport px | `r10`: drawn at scale 2, `exact` at scale 1 |
| Resize during a stroke | The stroke is abandoned, `layout-changed` | Refused in the evidence run |
| Resize, toggle or tab change after capture | Placement is derived again; nothing stored changes | A live 1280→1024 resize moved every drawing −128 px without a reload |
| Scroll inside an element (an overflowing table or panel) | Placement is derived again: the drawing follows its target, or is `hidden` once the target scrolls out of view | On a probe page, a 40 px scroll moved a drawing 0,−40, and scrolling its target out of view hid it |
| `<details>` closed, or a CSS tab unchecked, since capture | The drawing is `hidden`, never placed elsewhere. Show reopens what capture recorded and any `<details>` around a target | `r5`, `r6`: `hidden` on every revision, placed after Show |

Current Chrome hides a closed `<details>`'s content with
`content-visibility: hidden` and still lays it out, so `getBoundingClientRect`
returns the box it would have open. The first evidence run placed a drawing
inside a closed section as `exact`. Visibility must come from
`checkVisibility()`, with a walk of closed `<details>` ancestors where that API
is missing.

## 3. Placement on later renders

Placement is derived on every render and never stored. Each target is looked
up by its exact anchor. Text similarity is never used.

| Outcome | Rule | Painted |
|---|---|---|
| `exact` | Every target found once, visible, same size and relative position (±1 px), no offset | The ellipse |
| `moved` | As `exact`, all targets shifted by one offset | The ellipse, shifted with them |
| `reflowed` | Targets found, geometry changed | A thin dashed ellipse around the targets as they now are, never the drawing |
| `hidden` | A target exists but is not visible | Nothing; Show (section 2) |
| `missing` | A target's anchor is absent | Nothing; View original |
| `ambiguous` | A target's anchor occurs a different number of times than at capture | Nothing; View original |

Ten captures of revision a, placed on three revisions in seven layouts, gave
210 outcomes, each the one the rules predict (`expect.json`). At the capture
layout:

| Capture | a | b (content added above) | c (drawn content changed) |
|---|---|---|---|
| `r1`, `r2`: two strokes around one sentence | exact | moved 0,102 | **missing**: the sentence now reads "under heavy load" |
| `r3`: Queue and Worker nodes | exact | moved 0,63 | moved −8,0: the diagram recentered, those nodes unchanged |
| `r9`: the Retry budget node | exact | moved 0,63 | **missing**: renamed Backoff budget |
| `r4`: a list item | exact | moved 0,102 | **ambiguous**: the item now appears twice |
| `r5`, `r6`: inside closed notes, an unchecked tab | hidden | hidden | hidden |
| `r7`, `r8`, `r10`: figure, table, sentence at 2× pinch | exact | moved 0,102 | figure moved 0,78; table exact; `r10` missing |

At 1024 px and at 125% zoom every placed drawing moves −128,0, because the
760 px column re-centers. At 600 px the column reflows, and every drawing whose
targets are found and visible is `reflowed`.

In c the drawn sentence gains one word. Released ox's save-time remap rebinds
an orphaned mark on a list item, row or section by token similarity (Dice ≥
0.72); this sentence and its rewording score 0.96. Placement reports `missing`
and leaves the drawing off the page.

## 4. View original

The host redraws a drawing from the record alone, in the record's frame:

- each context item as a box with its text;
- diagram labels at their own boxes, with any text the labels don't account
  for (a figure's caption) along the bottom of the item;
- then the inked ellipse;
- and, when the context was cut, a "context truncated" note.

It names the revision and layout, says when that revision is not the one on
screen, and inserts text as text nodes, never markup. It needs neither the
framed document nor the revision's bytes. On revision c the harness shows the
original sentence and the "Retry budget" node inside their ellipses.

## 5. Inline context, not an attachment

**Decision:** the original context travels inline in the record, bounded. No
attachment.

Synthetic strokes over 42 authored plans from one team Ledger, at 1280×800.
Sizes are UTF-8 bytes. The raster column is a Chrome screenshot of the same box:
what an image attachment would weigh.

| Stroke around | Records | Record p50 / p95 / max | 2× PNG p50 / p95 / max | Context truncated |
|---|---|---|---|---|
| one element | 1,379 | 1.1 / 1.7 / 3.2 KB | 22 / 69 / 156 KB | 1.9% |
| a section | 157 | 3.3 / 8.2 / 13.4 KB | 253 KB / 1.42 MB / 2.1 MB | 8.9% |
| a viewport | 240 | 3.7 / 5.7 / 6.9 KB | 317 / 503 / 559 KB | 7.9% |
| the whole page | 32 | 6.3 / 12.8 / 13.0 KB | — | 37.5% |

Capture, including the ellipse fit, took at most 5.1 ms.

The text caps come from the same plans. Of their 1,982 visible leaf elements,
the median holds 105 characters of text and p99 holds 603; 3 exceed 2,000. An
element the drawing encloses therefore keeps up to 2,000 characters, and a
neighbour up to 600. An earlier cap of 280 for every item cut the text of 24% of
single-element drawings.

Refused: 9 viewport and 10 whole-page strokes covered more than 16 separate
elements (`too-large`); 8 section and 15 viewport strokes enclosed nothing
markable (`no-target`).

Why inline:

- **Size:** inline is 10–100× smaller. At p95 a section raster passes the
  1 MiB threshold above which the Ledger stores plan HTML through LFS. That
  would put review attachments on the upload-before-commit and hydration path,
  with missing-byte failures to diagnose.
- **Fidelity without permissions:** a document can rasterize itself. SVG
  `foreignObject` → canvas → `toDataURL` worked in Chrome, sandboxed
  included. But the image lacks the page's stylesheet and fonts, so a faithful
  image means copying computed styles node by node. Screen-capture APIs need
  permissions the sandbox denies.
- **Dependencies:** View original needs no other bytes. `plan_revision` is a
  reference for hosts that keep revisions, not a dependency. A host that
  fetches it must report "revision not available here" when it cannot, for
  example an unhydrated LFS stub.

**Limits:**

| What | Limit |
|---|---|
| path | 512 points, thinned beyond that |
| targets | 16 |
| context items | 48; an enclosed element's text ≤ 2,000 characters, a neighbour's ≤ 600; ≤ 32 labels of ≤ 80 characters |
| serialized context | 16 KiB of UTF-8; observed max 12,249 bytes |
| whole record (host refuses larger) | 64 KiB of UTF-8; observed max 13,379 bytes |

Every cut (an item, a label, a clipped text) sets `context.truncated`, and View
original says so.

These sit inside the 1 MiB review request cap (`reviewBodyLimit`) and the
Ledger contract's 8 MiB record limit (§3.6).

The attachment criterion (durable publication before references, missing-byte
diagnostics) does not arise.

## 6. Localhost and an opaque-origin sandbox

`serve.go` serves the same stored bytes three ways:

- **`/doc/<rev>`:** top-level on a loopback origin, as `ox plan review` does.
- **`/doc/<rev>?embed=1`:** framed, with this CSP:

  ```
  sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline';
  style-src 'unsafe-inline'; img-src data:; font-src data:; connect-src 'none';
  form-action 'none'; base-uri 'none'; object-src 'none'
  ```

- **`/host/<rev>`:** `host.html`, which frames `/doc/<rev>?embed=1` in
  `<iframe sandbox="allow-scripts">`.

Mermaid's CDN tag is replaced by ox's vendored 11.6.0, inlined, as artifact
renders already do. No permission beyond `allow-scripts` and inline script and
style was needed.

**Inside the frame:**

- the origin is `null`;
- `localStorage`, `document.cookie` and parent DOM access throw `SecurityError`;
- `fetch` is blocked;
- Mermaid renders without `unsafe-eval`.

**Same results framed and top-level:**

- Two strokes drawn through the frame produced captures identical to the same
  strokes top-level at the same width: path, ellipse, bbox, targets, context.
- Placed inside the frame, the ten fixture captures gave `expect.json`'s outcome
  (observed top-level) on every revision at five layouts: 150 of 150.

**Host rules:**

- Accept a message only from the frame's window, with origin `"null"` and the
  current instance id (passed in the frame URL's fragment).
- Accept a capture only if every field the host later reads is well-formed:
  path, ellipse, targets, layout, and each context item and label. It must
  also be at most 64 KiB of UTF-8.
- Mint `id`, stamp `plan_revision` and `created_at`.
- The document accepts messages only from its parent.
- A forged capture posted from another window was ignored.

**The CLI's review layer does not run here; the document tools do.** Before
#1290, `review.js` read `localStorage` at load outside a `try`, so framed with
`sandbox="allow-scripts"` it threw `SecurityError` and never drew its toolbar
(with `allow-same-origin` added, it started). It is now built from
`packages/plan-review` and returns before drawing anything when storage is
denied. A host instead injects the package's document tools, which need no
storage and talk to the host by `postMessage`
(`TestBrowser_DocumentToolsInOpaqueFrame` in `cmd/ox`). Drafts and the outbox
for hosted review live in the host (#1291).

## 7. Released readers and writers

**Candidate stored form:** a drawing is a round item. Its legacy fields project
it for readers that predate regions, and the record rides in `region`:

```jsonc
{"anchor": "q-<id>", "section": "Risks",
 "label": "Drawing: The retry path can double-fire under load when the queue is …",
 "status": "request-change", "note": "Both retries fire here.", "reviewer": "Devon",
 "region": { /* section 1 */ }}
```

- **`q-<id>` anchor:** released readers exempt `q` anchors from save-time
  label remapping, and so does the module's `Rebind`. That is the only
  exemption either has; it was written for highlights and is pinned by
  `TestRemapFeedback_LeavesHighlightsOnTheirWords` (CLI) and
  `TestRebind_MovesAMarkOnlyWhenTheMoveIsUnambiguous` (module).
  The `-` keeps a drawing's anchor apart from a highlight's (`q` + 8 hex). One
  anchor per drawing also keeps released assembly — latest mark per anchor and
  reviewer — from merging two drawings, and lets
  `ox plan feedback resolve <slug> q-<id>` close exactly one.
- **Label:** "Drawing: " plus the first target's text, within the 70-unit
  label length.

**Measured** with the official `ox_0.20.0_darwin_arm64` asset (checksum
verified) and a build of this branch, as in the Ledger contract's section 4:

| | v0.20.0 | this branch (main) |
|---|---|---|
| `plan feedback show --json` on `region-items` | Three items, each drawing by its label and note; the resolved drawing shows open (per-entry resolutions unreadable, Ledger contract §4) | Three items as `expect.json`; the resolved drawing closed |
| Rendered page | Drawings as rail rows tagged "text changed" and in the orphan bar; the element mark on its paragraph; no drawing painted on an element | Same |
| Save of revision c | Saves a new dated plan rather than revising this one, so its remap never runs here; its `remap.go:162` has the same `q` exemption | A control mark with a dead `h` anchor, labeled like a list item, is rebound `label-exact`; a drawing with the identical label is not |
| `feedback apply` of an item carrying `region` | Stored with `region` dropped, and the round id too, silently | Stored with `region` dropped, silently |
| `feedback apply` with `items: []`, drawings under another key | Refused: `feedback has no items` | Refused, the same |

The module keeps drawings intact. Its `Capture` and `Assemble` read
`region-items` as `expect.json` says. Its writer, run on that round's items
(`ParseRoundRequest`, then `Record`), keeps each drawing's `region`
byte-identical in the immutable record it names
`round-<time>-<id>.<digest>.json`. That was checked once with a throwaway test
against the merged module, not kept.

What #1293 needs from this:

- **Readers before writers.** Ship readers that understand `region` first;
  writers come from `pkg/planreview`, which already keeps it.
- **Requests and exports that carry a drawing use a shape both releases
  refuse:** an empty `items`, with the drawings under another key. Both
  releases otherwise accept the item and lose the drawing silently.
- **Stored rounds keep the legacy fields** for readers that predate regions.

## 8. Consuming-host qualification

A host that displays plans in its own sandboxed viewer proves, in that viewer:

1. **Environment:** `draw.js` injected into the framed document reports, in
   its `ready` message, origin `null`, storage, cookies, parent access and
   network denied, and diagrams rendered — and capture still works.
2. **Capture:** strokes around the same elements of revision a yield the same
   anchors as `captures.json`, pass the host rules of section 6, and stay
   within section 5's limits.
3. **Placement:** the host's own captures land in the same outcome classes on
   a, b and c as `expect.json`: `exact` at the capture layout; `moved` on b;
   `missing` or `ambiguous` where c changed the drawn content; `hidden`, then
   placed after Show. Offsets may differ with fonts and platform; outcome
   classes may not.
4. **View original:** renders in the trusted host from the record alone.
5. **Storage:** assembling `pkg/planreview/testdata/review-contract/region-items`
   yields its `expect.json` (the module's `Assemble` does), and its writer keeps
   `region`.

Production regions (#1293) wait for these to pass. Qualification on hosts
outside this repository is private-owner follow-up.

## 9. Not covered

- **Writers, the toolbar and the outbox:** #1291, #1293. `draw.js` is a
  prototype: it sits in the module's testdata for hosts to qualify with, and
  no ox binary or module code runs it.
- **Other engines and inputs:** only Chrome on macOS and mouse input were
  exercised. `checkVisibility()` exists in current Firefox and Safari; Safari's
  canvas rules for `foreignObject` were not tested.
- **ADR-025:** its draft section 5 proposes a raster attachment for drawings.
  Section 5 here argues for inline context instead; amending ADR-025 belongs
  with #1293.
