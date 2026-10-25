// Live presence, in the trusted host: who else has this plan open, and their
// pointers, drawn as colored cursors with their names. The presence service
// names everyone from their sign-in; the document tools only turn pointers
// into content locations and back (pointer.ts), under keys the host mints, so
// a framed plan never learns who points where. Nothing here is stored or
// queued: only the latest place this reviewer's pointer rests is sent, every
// peer is forgotten when the connection drops, and a reconnect starts from
// the service's own word on who is here now.

import { fnv1a } from './anchor.js';
import type { HostPort } from './ports.js';
import { MAX_POINTERS, pointerAt, type Located, type PointerAt } from './protocol.js';

/** Another connection to the plan, as the presence service describes it. */
export interface Peer {
  /** The connection: one per tab, unique in the plan's room. */
  id: string;
  /** The account's display name, from its sign-in. */
  name: string;
  /** A stable key for the account, so one person's tabs share a color. */
  user: string;
  /** The revision that connection shows: "sha256:" and the hex SHA-256 of its plan bytes. */
  revision: string;
  /** Where its pointer rests on the plan, or null. */
  at: PointerAt | null;
}

/** What a presence service says, in order. */
export type PresenceEvent =
  /** Joined, or joined again after a drop: this connection's id, and everyone else here now. */
  | { type: 'welcome'; self: string; peers: Peer[] }
  /** Someone joined or moved: the whole peer, the latest wins. */
  | { type: 'peer'; peer: Peer }
  /** Someone left, or the service expired them. */
  | { type: 'leave'; id: string }
  /** The connection dropped: nobody is known to be here until the next welcome. */
  | { type: 'lost' };

/** How a host reaches its presence service, with the reviewer's credentials. */
export interface PresenceTransport {
  /**
   * join enters the plan's room as the signed-in reviewer showing revision,
   * and stays: after a drop it says `lost`, reconnects, and says `welcome`
   * again. A service that offers this reviewer no presence on this plan
   * never says welcome, and the plan reads as it always did.
   */
  join(revision: string, on: (e: PresenceEvent) => void): PresenceLink;
}

export interface PresenceLink {
  /**
   * Where this reviewer's pointer rests now, or null. Only the latest
   * matters: drop any not yet sent. A promise returned holds the next move
   * until it settles, so moves never overlap and cannot arrive out of order.
   */
  move(at: PointerAt | null): void | Promise<unknown>;
  /** Leave the room. */
  leave(): void;
}

export interface Presence {
  /** This reviewer's pointer, as the document tools located it. */
  pointer(at: PointerAt | null): void;
  /** Where the document placed the peers' pointers. */
  located(points: Located[]): void;
  /** The document is listening: ask it to place the pointers again. */
  ready(): void;
  /** The frame moved in this window: the cursors follow it. */
  reposition(): void;
  leave(): void;
}

const SEND_EVERY = 200; // ms: the most often a moving pointer is sent (5 a second; host.css glides a cursor that long)
const COLORS = 8; // host.css .rev-c0 … .rev-c7
const MAX_NAME = 200;
const ARROW = '<svg width="16" height="20" viewBox="0 0 16 20" aria-hidden="true"><path d="M1 1l13.5 10.2-6.1.9-3.3 6.4z" fill="currentColor" stroke="#111411" stroke-width="1.2" stroke-linejoin="round"/></svg>';

const color = (user: string) => 'rev-c' + (parseInt(fnv1a(user), 16) % COLORS);
const short = (v: unknown, n: number): v is string => typeof v === 'string' && v.length > 0 && v.length <= n;

// peerOf checks one peer the transport delivered; the service is trusted, but
// its words still reach this page's markup, and a location the document
// would refuse drops the whole locate.
function peerOf(v: unknown): Peer | null {
  const p = v as Record<string, unknown> | null;
  if (!p || typeof p !== 'object' || !short(p.id, 128) || !short(p.name, MAX_NAME) || !short(p.user, 128) || !short(p.revision, 128)) return null;
  // no location, or null: pointing nowhere
  const at = p.at == null ? null : pointerAt(p.at);
  if (p.at != null && !at) return null;
  return { id: p.id, name: p.name, user: p.user, revision: p.revision, at };
}

/**
 * startPresence joins transport's room showing revision, lists who is here
 * in `here`, and draws their cursors over the plan: over frame when the plan
 * is framed, else over this window. port reaches the document tools, which
 * place each pointer.
 */
export function startPresence(port: HostPort, transport: PresenceTransport, revision: string, here: HTMLElement, frame?: HTMLIFrameElement): Presence {
  let self = '';
  let live = false;
  let gone = false;
  const peers = new Map<string, Peer>();
  // The document places pointers under keys minted here, never the
  // service's connection ids
  const keys = new Map<string, string>(), ids = new Map<string, string>();
  let minted = 0;
  let placed: Located[] = [];
  const glide = new Set<string>(); // keys whose peer moved since the last placement: they ease to the new spot
  const layer = document.createElement('div');
  layer.className = 'rev-cursors';
  layer.setAttribute('aria-hidden', 'true');
  document.body.appendChild(layer);
  const cursors = new Map<string, HTMLElement>();
  let listed = ''; // who the chips show, as JSON

  let mine: PointerAt | null = null, sent = 'null', last = 0, moving = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const link = transport.join(revision, on);

  function on(e: PresenceEvent) {
    if (gone) return;
    switch (e.type) {
      case 'welcome':
        self = typeof e.self === 'string' ? e.self : '';
        peers.clear();
        (Array.isArray(e.peers) ? e.peers : []).forEach(add);
        live = true;
        // the service knows nothing of this connection yet: tell it where the pointer is now
        sent = '';
        send(true);
        break;
      case 'peer':
        add(e.peer);
        break;
      case 'leave':
        peers.delete(e.id);
        break;
      case 'lost':
        live = false;
        peers.clear();
        break;
    }
    update();
  }
  function add(v: unknown) {
    const p = peerOf(v);
    if (!p || p.id === self) return;
    const was = peers.get(p.id);
    if (was && JSON.stringify(was.at) !== JSON.stringify(p.at)) glide.add(keyOf(p.id));
    peers.set(p.id, p);
  }
  function keyOf(id: string) {
    let k = keys.get(id);
    if (!k) {
      k = 'p' + (++minted);
      keys.set(id, k);
      ids.set(k, id);
    }
    return k;
  }

  // send passes this reviewer's latest pointer to the service, at most once
  // every SEND_EVERY ms; now sends it at once.
  function send(now: boolean) {
    if (!live || gone || (timer !== undefined && !now)) return;
    clearTimeout(timer);
    timer = setTimeout(flush, now ? 0 : Math.max(0, last + SEND_EVERY - Date.now()));
  }
  function flush() {
    timer = undefined;
    const at = mine, s = JSON.stringify(at);
    if (!live || gone || moving || s === sent) return;
    sent = s;
    last = Date.now();
    moving = true;
    Promise.resolve().then(() => link.move(at)).catch(() => {}).then(() => { moving = false; send(false); });
  }

  // update lists who is here and asks the document to place every pointer on
  // this revision; a pointer on another revision is never placed on this one.
  function update() {
    if (gone) return;
    keys.forEach((k, id) => { if (!peers.has(id)) { keys.delete(id); ids.delete(k); } });
    renderHere();
    const points: Array<{ key: string; at: PointerAt }> = [];
    peers.forEach((p) => { if (p.revision === revision && p.at && points.length < MAX_POINTERS) points.push({ key: keyOf(p.id), at: p.at }); });
    port.send({ type: 'locate', points });
    // a cursor whose peer left, or whose pointer left the plan, goes now,
    // not at the document's next answer
    cursors.forEach((el, k) => {
      const id = ids.get(k), p = id ? peers.get(id) : undefined;
      if (!p || !p.at) { el.remove(); cursors.delete(k); }
    });
  }

  // Who is here, one chip per person, in their cursor's color. A person on
  // another revision is named as such, since their pointer is never drawn
  // on this one. The chips are redrawn only when that changes, not on every
  // move: a chip's title, the only place its full name shows, would vanish
  // under the reader's hover.
  function renderHere() {
    const people = new Map<string, { name: string; same: boolean }>();
    peers.forEach((p) => {
      const was = people.get(p.user);
      people.set(p.user, { name: p.name, same: (!!was && was.same) || p.revision === revision });
    });
    const now = JSON.stringify([...people]);
    if (now === listed) return;
    listed = now;
    here.textContent = '';
    here.hidden = !people.size;
    let other = 0, otherName = '';
    people.forEach((p, user) => {
      const chip = document.createElement('span');
      chip.className = 'rev-peer ' + color(user) + (p.same ? '' : ' rev-peer-other');
      chip.textContent = (Array.from(p.name.trim())[0] || '?').toUpperCase(); // a code point, never half of one
      chip.title = p.same ? p.name + ' is here' : p.name + ' is viewing another revision of this plan';
      here.appendChild(chip);
      if (!p.same) { other++; otherName = p.name; }
    });
    if (other) {
      const note = document.createElement('span');
      note.className = 'rev-here-note';
      note.textContent = (other === 1 ? otherName : other + ' people') + ' on another revision';
      here.appendChild(note);
    }
  }

  // draw puts a cursor, with its name, where the document placed each
  // pointer, offset by the frame's box and clipped to it.
  function draw() {
    let b = { left: 0, top: 0, right: innerWidth, bottom: innerHeight };
    if (frame) {
      // the framed document's viewport: the frame's box inside its border
      const r = frame.getBoundingClientRect(), left = r.left + frame.clientLeft, top = r.top + frame.clientTop;
      b = { left, top, right: left + frame.clientWidth, bottom: top + frame.clientHeight };
    }
    const clip = { left: Math.max(b.left, 0), top: Math.max(b.top, 0), right: Math.min(b.right, innerWidth), bottom: Math.min(b.bottom, innerHeight) };
    const drawn = new Set<string>();
    placed.forEach((p) => {
      const id = ids.get(p.key), peer = id ? peers.get(id) : undefined;
      if (!peer || !peer.at) return;
      const x = b.left + p.x, y = b.top + p.y;
      if (x < clip.left || y < clip.top || x > clip.right || y > clip.bottom) return;
      let el = cursors.get(p.key);
      if (!el) {
        el = document.createElement('div');
        el.innerHTML = ARROW + '<span class="rev-cursor-name"></span>';
        layer.appendChild(el);
        cursors.set(p.key, el);
      }
      el.className = 'rev-cursor ' + color(peer.user) + (glide.has(p.key) ? ' rev-glide' : '');
      (el.lastChild as HTMLElement).textContent = peer.name;
      el.style.transform = 'translate(' + x + 'px,' + y + 'px)';
      drawn.add(p.key);
    });
    glide.clear();
    cursors.forEach((el, k) => { if (!drawn.has(k)) { el.remove(); cursors.delete(k); } });
  }

  return {
    pointer(at) {
      mine = at;
      send(false);
    },
    located(points) {
      placed = points;
      draw();
    },
    ready: update,
    reposition: draw,
    leave() {
      if (gone) return;
      gone = true;
      clearTimeout(timer);
      link.leave();
      port.send({ type: 'locate', points: [] });
      layer.remove();
      here.textContent = '';
      here.hidden = true;
    },
  };
}
