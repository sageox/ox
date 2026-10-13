import { envelope, parseDocumentMessage, parseHostMessage, type DocumentMessage, type HostMessage } from './protocol.js';

/** One end of the channel between host controls and document tools. */
export interface Port<Out, In> {
  send(msg: Out): void;
  /** listen sets the one handler for incoming messages; messages before it are dropped. */
  listen(fn: (msg: In) => void): void;
}

export type HostPort = Port<HostMessage, DocumentMessage>;
export type DocumentPort = Port<DocumentMessage, HostMessage>;

/**
 * localPorts connects host controls and document tools that share one window,
 * as on the ox CLI's review page. A message is one function call, so a click
 * that opens the composer has opened it when the click handler returns.
 */
export function localPorts(): { host: HostPort; document: DocumentPort } {
  let toHost: ((m: DocumentMessage) => void) | null = null;
  let toDocument: ((m: HostMessage) => void) | null = null;
  return {
    host: { send: (m) => toDocument && toDocument(m), listen: (fn) => { toHost = fn; } },
    document: { send: (m) => toHost && toHost(m), listen: (fn) => { toDocument = fn; } },
  };
}

/**
 * connectFrame is the host's end for a plan document framed with
 * sandbox="allow-scripts" and running the document tools script. It takes the
 * first `ready` from that frame's window, from an opaque origin, for this
 * instance, carrying a MessagePort, and from then on hears only that port: a
 * page the frame navigates to after `ready` cannot take the conversation over.
 * Call it, and listen, before the frame loads; messages sent before `ready`
 * are dropped.
 */
export function connectFrame(frame: HTMLIFrameElement, instance: string): HostPort & { close(): void } {
  let handler: ((m: DocumentMessage) => void) | null = null;
  let port: MessagePort | null = null;
  const onMessage = (e: MessageEvent) => {
    if (!handler || e.source !== frame.contentWindow || e.origin !== 'null' || !e.ports[0]) return;
    const m = parseDocumentMessage(e.data, instance);
    if (!m || m.type !== 'ready') return;
    window.removeEventListener('message', onMessage);
    port = e.ports[0];
    port.onmessage = (ev) => {
      const d = parseDocumentMessage(ev.data, instance);
      if (d && handler) handler(d);
    };
    handler(m);
  };
  window.addEventListener('message', onMessage);
  return {
    // Re-parse before posting: the frame's own scripts see the raw message, so
    // fields the protocol does not name (a note spread in with a stored item)
    // must never leave the host.
    send: (m) => {
      const clean = parseHostMessage(envelope(m, instance), instance);
      if (port && clean) port.postMessage(envelope(clean, instance));
    },
    listen: (fn) => { handler = fn; },
    close: () => {
      window.removeEventListener('message', onMessage);
      if (port) port.close();
      handler = null;
    },
  };
}

/**
 * connectParent is the framed document's end. It opens a MessageChannel and
 * hands the parent one end with `ready`; everything after travels on it.
 */
export function connectParent(instance: string): DocumentPort {
  const channel = new MessageChannel();
  let handler: ((m: HostMessage) => void) | null = null;
  channel.port1.onmessage = (e) => {
    const m = parseHostMessage(e.data, instance);
    if (m && handler) handler(m);
  };
  return {
    send: (m) => {
      // the document is not told its parent's origin; whoever frames it hears `ready`
      if (m.type === 'ready') window.parent.postMessage(envelope(m, instance), '*', [channel.port2]);
      else channel.port1.postMessage(envelope(m, instance));
    },
    listen: (fn) => { handler = fn; },
  };
}
