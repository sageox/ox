// A drawn region: a reviewer's freehand ellipse around part of a plan, kept
// with what it encloses and a bounded copy of what lay under it, so it can be
// shown again after the plan changes. docs/specs/plan-review-drawn-context.md
// is the format. pkg/planreview (region.go) checks the same rules on the
// server, and both run the cases in
// pkg/planreview/testdata/drawn-context/regions.json.
//
// Coordinates are CSS pixels from the top-left of the document. A region is
// stored on its round item, anchored "q-<id>" and labeled "Drawing: …" for
// readers that predate regions.

export type Point = [number, number];
/** x, y, width, height. */
export type Box = [number, number, number, number];

export interface Ellipse {
  center: Point;
  radii: [number, number];
  /** Degrees. */
  rotation: number;
  /** Where the pen landed, in degrees around the ellipse. */
  start: number;
  /** The way the pen moved. */
  dir: 1 | -1;
}

export interface Layout {
  viewport: [number, number];
  document: [number, number];
  dpr: number;
  scale: number;
  scroll: Point;
  theme: string;
  fonts: string;
  /** <details> open at capture, by summary-text hash. */
  open: string[];
  /** Checked radios and checkboxes at capture, by #id or name=value. */
  checked: string[];
}

/** An element the ellipse encloses. nth and of say which occurrence when its anchor repeats. */
export interface RegionTarget {
  anchor: string;
  tag: string;
  section: string;
  box: Box;
  cover: number;
  nth?: number;
  of?: number;
}

export interface ContextLabel {
  text: string;
  box: Box;
}

/** What lay under or beside the drawing, with the text the reader saw. */
export interface ContextItem {
  anchor: string;
  tag: string;
  box: Box;
  text: string;
  labels?: ContextLabel[];
}

/** What the document tools capture; the host stamps the rest. */
export interface Capture {
  v: 1;
  kind: 'region';
  frame: 'document';
  /** The stroke as drawn. */
  path: Point[];
  /** What the reviewer sees, and what decides the targets. */
  ellipse: Ellipse;
  bbox: Box;
  layout: Layout;
  targets: RegionTarget[];
  context: { items: ContextItem[]; truncated: boolean };
}

/** A drawing's record: a capture with the identity, revision and time its host stamped. */
export interface Region extends Capture {
  id: string;
  plan_revision: string;
  created_at: string;
}

/** The bounds of section 5 of the spec. Text is in UTF-16 units, sizes in UTF-8 bytes of compact JSON. */
export const LIMITS = {
  regionBytes: 65536,
  contextBytes: 16384,
  pathPoints: 512,
  targets: 16,
  contextItems: 48,
  itemLabels: 32,
  targetText: 2000,
  neighborText: 600,
  labelText: 80,
  layoutKeys: 128,
};

/** A drawing's anchor: "q-" and its id. */
export const DRAWING = /^q-[A-Za-z0-9_-]{8,64}$/;
const ID = /^[A-Za-z0-9_-]{8,64}$/;
const ELEMENT = /^h[0-9a-f]{8}$/;
const REVISION = /^sha256:[0-9a-f]{64}$/;
const TIME = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?Z$/;

type Obj = Record<string, unknown>;
const obj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);
const str = (v: unknown): v is string => typeof v === 'string';
const num = (v: unknown): v is number => typeof v === 'number' && isFinite(v);
// every element passes, holes included: Array.every skips a hole, which a
// structured clone keeps and JSON then writes as null
export const all = (a: unknown[], ok: (x: unknown) => boolean) => {
  for (let i = 0; i < a.length; i++) if (!ok(a[i])) return false;
  return true;
};
const nums = (v: unknown, n: number): v is number[] => Array.isArray(v) && v.length === n && all(v, num);
const box = (v: unknown): v is Box => nums(v, 4) && v[2] >= 0 && v[3] >= 0;
const strs = (v: unknown, n: number): v is string[] => Array.isArray(v) && v.length <= n && all(v, str);
const count = (v: unknown): v is number => num(v) && v >= 0 && Math.floor(v) === v;

/** utf8Bytes is the size of v as stored: compact UTF-8 JSON. */
export const utf8Bytes = (v: unknown) => new TextEncoder().encode(JSON.stringify(v)).length;

// problem returns why v is not a capture (a region, when stamped), or ''.
function problem(v: unknown, stamped: boolean): string {
  if (!obj(v)) return 'not an object';
  if (stamped) {
    if (!str(v.id) || !ID.test(v.id)) return 'id';
    if (!str(v.plan_revision) || !REVISION.test(v.plan_revision)) return 'plan_revision';
    if (!str(v.created_at) || !TIME.test(v.created_at)) return 'created_at';
  }
  if (v.v !== 1 || v.kind !== 'region' || v.frame !== 'document') return 'v, kind or frame';
  const p = v.path;
  if (!Array.isArray(p) || p.length < 3 || p.length > LIMITS.pathPoints || !all(p, (x) => nums(x, 2))) return 'path';
  const e = v.ellipse;
  if (!obj(e) || !nums(e.center, 2) || !nums(e.radii, 2) || !(e.radii[0] > 0 && e.radii[1] > 0) || !num(e.rotation) || !num(e.start) || (e.dir !== 1 && e.dir !== -1)) return 'ellipse';
  if (!box(v.bbox)) return 'bbox';
  const l = v.layout;
  if (!obj(l) || !nums(l.viewport, 2) || !nums(l.document, 2) || !nums(l.scroll, 2) || !num(l.dpr) || !num(l.scale) ||
      !str(l.theme) || !str(l.fonts) || !strs(l.open, LIMITS.layoutKeys) || !strs(l.checked, LIMITS.layoutKeys)) return 'layout';
  const ts = v.targets;
  if (!Array.isArray(ts) || !ts.length || ts.length > LIMITS.targets) return 'targets';
  for (const t of ts) {
    if (!obj(t) || !str(t.anchor) || !ELEMENT.test(t.anchor) || !str(t.tag) || !str(t.section) || !box(t.box)) return 'targets';
    if (!num(t.cover) || t.cover < 0 || t.cover > 1) return 'targets';
    if (('nth' in t) !== ('of' in t) || ('nth' in t && !(count(t.nth) && count(t.of) && t.of >= 2 && t.nth < t.of))) return 'targets';
  }
  const c = v.context;
  if (!obj(c) || !Array.isArray(c.items) || c.items.length > LIMITS.contextItems || typeof c.truncated !== 'boolean') return 'context';
  const enclosed = ts.map((t) => (t as Obj).anchor);
  for (const it of c.items) {
    if (!obj(it) || !str(it.anchor) || !ELEMENT.test(it.anchor) || !str(it.tag) || !box(it.box) || !str(it.text)) return 'context';
    if (it.text.length > (enclosed.indexOf(it.anchor) >= 0 ? LIMITS.targetText : LIMITS.neighborText)) return 'context';
    if (!('labels' in it)) continue;
    const ls = it.labels;
    if (!Array.isArray(ls) || !ls.length || ls.length > LIMITS.itemLabels) return 'context';
    if (!all(ls, (x) => obj(x) && box(x.box) && str(x.text) && x.text.length <= LIMITS.labelText)) return 'context';
  }
  if (utf8Bytes(c) > LIMITS.contextBytes) return 'context size';
  if (utf8Bytes(v) > LIMITS.regionBytes) return 'size';
  return '';
}

/** checkRegion returns why v is not a well-formed region, or '' when it is. */
export function checkRegion(v: unknown): string {
  return problem(v, true);
}

/**
 * captureOf returns a capture the document tools proposed as a fresh object
 * holding only the fields named here, or null when it is not well-formed. A
 * framed document is untrusted: nothing else it sent reaches a record.
 */
export function captureOf(v: unknown): Capture | null {
  if (problem(v, false)) return null;
  const c = v as Capture;
  const pt = (p: number[]): Point => [p[0], p[1]];
  const bx = (b: number[]): Box => [b[0], b[1], b[2], b[3]];
  const e = c.ellipse, l = c.layout;
  return {
    v: 1, kind: 'region', frame: 'document',
    path: c.path.map(pt),
    ellipse: { center: pt(e.center), radii: pt(e.radii), rotation: e.rotation, start: e.start, dir: e.dir },
    bbox: bx(c.bbox),
    layout: { viewport: pt(l.viewport), document: pt(l.document), dpr: l.dpr, scale: l.scale, scroll: pt(l.scroll), theme: l.theme, fonts: l.fonts, open: l.open.slice(), checked: l.checked.slice() },
    targets: c.targets.map((t) => {
      const o: RegionTarget = { anchor: t.anchor, tag: t.tag, section: t.section, box: bx(t.box), cover: t.cover };
      if (t.of !== undefined) { o.nth = t.nth; o.of = t.of; }
      return o;
    }),
    context: {
      items: c.context.items.map((it) => {
        const o: ContextItem = { anchor: it.anchor, tag: it.tag, box: bx(it.box), text: it.text };
        if (it.labels) o.labels = it.labels.map((x) => ({ text: x.text, box: bx(x.box) }));
        return o;
      }),
      truncated: c.context.truncated,
    },
  };
}

/** drawingLabel is the label a drawing's item carries: "Drawing: " and the text it encloses, within 70 units. */
export function drawingLabel(r: Capture): string {
  const first = r.targets[0];
  const item = r.context.items.filter((it) => it.anchor === first.anchor)[0];
  const text = ((item && item.text) || first.section).replace(/\s+/g, ' ').trim();
  const label = 'Drawing: ' + text;
  return label.length > 70 ? label.slice(0, 69) + '…' : label;
}

// --- a stroke's geometry: what the document tools capture, and what a
// host can recompute from a record's path ---

/** r1 rounds a coordinate to the tenth of a pixel a capture keeps. */
export const r1 = (n: number) => Math.round(n * 10) / 10;

/** bboxOf is the box around points, to a tenth of a pixel. */
export function bboxOf(pts: Point[]): Box {
  let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
  pts.forEach((p) => { x0 = Math.min(x0, p[0]); y0 = Math.min(y0, p[1]); x1 = Math.max(x1, p[0]); y1 = Math.max(y1, p[1]); });
  return [r1(x0), r1(y0), r1(x1 - x0), r1(y1 - y0)];
}

/**
 * fitEllipse is the ellipse a stroke means: the one with the stroke's area,
 * centre and second moments (closing the stroke back to its start), squared
 * to the page when within 10° of it, starting where the pen landed and
 * running the way it moved. A stroke that encloses almost nothing (a line)
 * gets the ellipse inscribed in its bounding box.
 */
export function fitEllipse(pts: Point[]): Ellipse {
  let A = 0, sx = 0, sy = 0, sxx = 0, syy = 0, sxy = 0;
  for (let i = 0; i < pts.length; i++) {
    const p = pts[i], q = pts[(i + 1) % pts.length], c = p[0] * q[1] - q[0] * p[1];
    A += c / 2;
    sx += (p[0] + q[0]) * c;
    sy += (p[1] + q[1]) * c;
    sxx += (p[0] * p[0] + p[0] * q[0] + q[0] * q[0]) * c;
    syy += (p[1] * p[1] + p[1] * q[1] + q[1] * q[1]) * c;
    sxy += (p[0] * q[1] + 2 * p[0] * p[1] + 2 * q[0] * q[1] + q[0] * p[1]) * c;
  }
  const bb = bboxOf(pts);
  let cx: number, cy: number, rx: number, ry: number, rot = 0;
  if (Math.abs(A) < 0.05 * bb[2] * bb[3]) {
    cx = bb[0] + bb[2] / 2; cy = bb[1] + bb[3] / 2; rx = bb[2] / 2; ry = bb[3] / 2;
  } else {
    // a uniform ellipse with semi-axes a, b has variance a²/4 and b²/4 along them
    cx = sx / (6 * A); cy = sy / (6 * A);
    const vxx = sxx / (12 * A) - cx * cx, vyy = syy / (12 * A) - cy * cy, vxy = sxy / (24 * A) - cx * cy;
    const mid = (vxx + vyy) / 2, d = Math.sqrt(((vxx - vyy) * (vxx - vyy)) / 4 + vxy * vxy);
    rot = 0.5 * Math.atan2(2 * vxy, vxx - vyy);
    rx = 2 * Math.sqrt(Math.max(mid + d, 0)); ry = 2 * Math.sqrt(Math.max(mid - d, 0));
    const snap = (10 * Math.PI) / 180;
    if (Math.abs(rot) < snap) rot = 0;
    else if (Math.PI / 2 - Math.abs(rot) < snap) { rot = 0; const t = rx; rx = ry; ry = t; }
  }
  const dx = pts[0][0] - cx, dy = pts[0][1] - cy, cos = Math.cos(rot), sin = Math.sin(rot);
  const start = Math.atan2((-dx * sin + dy * cos) / (ry || 1), (dx * cos + dy * sin) / (rx || 1));
  return { center: [r1(cx), r1(cy)], radii: [r1(rx), r1(ry)], rotation: r1((rot * 180) / Math.PI), start: r1((start * 180) / Math.PI), dir: A < 0 ? -1 : 1 };
}

/** thin drops points closer than 2 px to the last kept one, then strides the rest down to the point limit. */
export function thin(pts: Point[]): Point[] {
  const out: Point[] = [];
  pts.forEach((p) => { const q = out[out.length - 1]; if (!q || Math.abs(p[0] - q[0]) + Math.abs(p[1] - q[1]) >= 2) out.push(p); });
  if (out.length <= LIMITS.pathPoints) return out;
  const step = out.length / LIMITS.pathPoints, s: Point[] = [];
  for (let i = 0; i < LIMITS.pathPoints; i++) s.push(out[Math.floor(i * step)]);
  return s;
}

// --- ink: how a drawing is painted, from the record alone, so every host
// paints it the same way (spec section 1). A broad nib held almost flat: the
// line is widest on the vertical turns and a hairline along the horizontal
// runs, so an ellipse around a line of text stays clear of the lines above
// and below it.

const NIB = { width: 12, angle: (-12 * Math.PI) / 180 };

/** ellipsePoints samples e from where the pen landed, the way it moved, for one turn plus extra radians, about every 3 px. */
export function ellipsePoints(e: Ellipse, extra: number): Point[] {
  const rx = e.radii[0], ry = e.radii[1], rot = (e.rotation * Math.PI) / 180, t0 = (e.start * Math.PI) / 180;
  const around = Math.PI * (3 * (rx + ry) - Math.sqrt((3 * rx + ry) * (rx + 3 * ry))); // Ramanujan's perimeter
  const sweep = 2 * Math.PI + extra;
  const n = Math.max(48, Math.min(512, Math.ceil((around * sweep) / (2 * Math.PI) / 3)));
  const cos = Math.cos(rot), sin = Math.sin(rot), out: Point[] = [];
  for (let i = 0; i <= n; i++) {
    const t = t0 + (e.dir * sweep * i) / n, x = rx * Math.cos(t), y = ry * Math.sin(t);
    out.push([e.center[0] + x * cos - y * sin, e.center[1] + x * sin + y * cos]);
  }
  return out;
}

// smooth cuts corners twice (Chaikin), keeping both ends where the pen was.
function smooth(pts: Point[]): Point[] {
  for (let k = 0; k < 2 && pts.length > 2; k++) {
    const out: Point[] = [pts[0]];
    for (let i = 0; i < pts.length - 1; i++) {
      const a = pts[i], b = pts[i + 1];
      out.push([0.75 * a[0] + 0.25 * b[0], 0.75 * a[1] + 0.25 * b[1]], [0.25 * a[0] + 0.75 * b[0], 0.25 * a[1] + 0.75 * b[1]]);
    }
    out.push(pts[pts.length - 1]);
    pts = out;
  }
  return pts;
}

/**
 * inkPaths is the SVG path data for a line of pen ink through pts, shifted by
 * off: sweep is the nib's sweep, filled; hair a 1 px line along its centre, so
 * the stretches that run along the nib never vanish.
 */
export function inkPaths(pts: Point[], off: Point): { sweep: string; hair: string } {
  pts = smooth(pts.map((p): Point => [p[0] + off[0], p[1] + off[1]]));
  if (pts.length < 2) return { sweep: '', hair: '' };
  const at = [0];
  for (let i = 1; i < pts.length; i++) at.push(at[i - 1] + Math.hypot(pts[i][0] - pts[i - 1][0], pts[i][1] - pts[i - 1][1]));
  const total = at[at.length - 1];
  // full width 24 px after landing; lifts over the last 56 px
  const width = (d: number) => Math.max(0.1, Math.min(1, d / 24, (total - d) / 56));
  const nx = (Math.cos(NIB.angle) * NIB.width) / 2, ny = (Math.sin(NIB.angle) * NIB.width) / 2;
  const xy = (p: Point, s: number) => r1(p[0] + nx * s) + ' ' + r1(p[1] + ny * s);
  let sweep = '', hair = 'M' + xy(pts[0], 0);
  for (let j = 0; j < pts.length - 1; j++) {
    const a = pts[j], b = pts[j + 1], wa = width(at[j]), wb = width(at[j + 1]);
    sweep += 'M' + xy(a, wa) + 'L' + xy(b, wb) + 'L' + xy(b, -wb) + 'L' + xy(a, -wa) + 'Z';
    hair += 'L' + xy(b, 0);
  }
  return { sweep, hair };
}

/** inkLine is what gets inked for a drawing: its ellipse, run 0.3 rad (about 17°) past its start. */
export function inkLine(e: Ellipse): Point[] {
  return ellipsePoints(e, 0.3);
}
