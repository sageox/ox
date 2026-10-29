(() => {
  var __defProp = Object.defineProperty;
  var __returnValue = (v) => v;
  function __exportSetter(name, newValue) {
    this[name] = __returnValue.bind(null, newValue);
  }
  var __export = (target, all) => {
    for (var name in all)
      __defProp(target, name, {
        get: all[name],
        enumerable: true,
        configurable: true,
        set: __exportSetter.bind(all, name)
      });
  };

  // internal/usagepane/mod/hooks/scene.ts
  var exports_scene = {};
  __export(exports_scene, {
    strokeWeight: () => strokeWeight,
    promptSvg: () => promptSvg,
    phaseOf: () => phaseOf,
    laneSvg: () => laneSvg,
    formatDuration: () => formatDuration,
    fitSource: () => fitSource,
    dotCount: () => dotCount,
    caption: () => caption,
    SVG_SOURCE_CAP: () => SVG_SOURCE_CAP,
    ANIM_MS: () => ANIM_MS
  });

  // internal/usagepane/mod/hooks/logic.ts
  function utf8Bytes(s) {
    let n = 0;
    for (let i = 0;i < s.length; i++) {
      const c = s.charCodeAt(i);
      if (c < 128) {
        n += 1;
      } else if (c < 2048) {
        n += 2;
      } else if (c >= 55296 && c <= 56319 && i + 1 < s.length) {
        n += 4;
        i++;
      } else {
        n += 3;
      }
    }
    return n;
  }
  function formatTokens(n) {
    const t = Math.max(0, Math.round(n));
    if (t < 1000) {
      return `~${t}`;
    }
    const k = t / 1000;
    return k < 10 ? `~${(Math.round(k * 10) / 10).toFixed(1).replace(/\.0$/, "")}k` : `~${Math.round(k)}k`;
  }

  // internal/usagepane/mod/hooks/tokens.ts
  var LIGHT = {
    stream: "#4e7d4c",
    node: "#3d643b",
    text: "#4a4d42",
    quiet: "#6b6d60",
    danger: "#9f4838"
  };
  var DARK = {
    stream: "#99c693",
    node: "#7aaa77",
    text: "#b7b6a3",
    quiet: "#8c8e7e",
    danger: "#d77e6c"
  };
  var FONT_SANS = "Inter, ui-sans-serif, system-ui, -apple-system, sans-serif";
  var FONT_MONO = "ui-monospace, SFMono-Regular, Menlo, monospace";
  var TOKENS = {
    height: 24,
    width: 640,
    narrowBelow: 400,
    midY: 12,
    textY: 16,
    digestPx: 11,
    labelPx: 10,
    labelTracking: 0.8,
    sansCharW: 5.6,
    monoCharW: 6.6,
    nodeR: 3,
    ringR: 3.5,
    dotR: 2.5,
    ringDotR: 2.25,
    quietW: 1.5,
    hairW: 1,
    openDash: "1.5 4",
    stubLen: 34,
    stubH: 2,
    weightAt: [
      { min: 1e4, w: 3 },
      { min: 1000, w: 2 },
      { min: 0, w: 1 }
    ],
    tokensPerDot: 1000,
    maxDots: 12,
    drawMs: 600,
    fadeMs: 400,
    dotMs: 1400,
    dotStaggerMs: 90,
    easeHouse: "0.4 0 0.2 1",
    easeOut: "0 0 0.58 1"
  };

  // internal/usagepane/mod/hooks/scene.ts
  var ANIM_MS = 2400;
  var SVG_SOURCE_CAP = 131072;
  var LANE_NAMES = {
    "team-context": "team context",
    ledger: "ledger",
    murmurs: "murmurs",
    code: "code",
    cloud: "cloud"
  };
  function dotCount(tokens) {
    const t = Math.max(0, tokens);
    return Math.min(TOKENS.maxDots, Math.max(1, Math.ceil(t / TOKENS.tokensPerDot)));
  }
  function strokeWeight(tokens) {
    for (const step of TOKENS.weightAt) {
      if (tokens >= step.min) {
        return step.w;
      }
    }
    return 1;
  }
  function formatDuration(ms) {
    const s = Math.max(0.1, ms / 1000);
    if (s < 10) {
      return `${s.toFixed(1)}s`;
    }
    if (s < 60) {
      return `${Math.round(s)}s`;
    }
    const m = Math.floor(s / 60);
    return `${m}m ${Math.round(s - m * 60)}s`;
  }
  function phaseOf(e, now, reducedMotion = false) {
    if (e.endedAt === undefined) {
      return "running";
    }
    if (reducedMotion) {
      return "settled";
    }
    return now - e.endedAt < ANIM_MS ? "flight" : "settled";
  }
  function outcomeOf(e) {
    if (e.endedAt === undefined) {
      return "open";
    }
    if (e.interrupted) {
      return "interrupted";
    }
    if (e.failed) {
      return "failed";
    }
    return e.tokens > 0 ? "flowed" : "empty";
  }
  function esc(s) {
    return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&#39;");
  }
  function num(n) {
    return String(Math.round(n * 100) / 100);
  }
  function sec(ms) {
    return `${num(ms / 1000)}s`;
  }
  function hash(s) {
    let h = 5381;
    for (let i = 0;i < s.length; i++) {
      h = (h << 5) + h + s.charCodeAt(i) >>> 0;
    }
    return h.toString(36);
  }
  var SEP = { text: " · " };
  function itemsPhrase(e) {
    if (e.items === undefined || e.items <= 0) {
      return null;
    }
    return `${e.items} ${e.itemsNoun ?? "items"}`;
  }
  function laneDigest(e) {
    const outcome = outcomeOf(e);
    const dur = e.endedAt === undefined ? "" : formatDuration(e.endedAt - e.startedAt);
    switch (outcome) {
      case "open":
        return [{ text: e.step }];
      case "failed":
        return [{ text: "failed", cls: "bad" }, { text: ` while ${e.step}` }, SEP, { text: dur, cls: "n" }];
      case "interrupted":
        return [{ text: `interrupted while ${e.step}` }, SEP, { text: dur, cls: "n" }];
      case "empty":
        return [{ text: e.label }, SEP, { text: e.direction === "in" ? "nothing came back" : "nothing sent" }, SEP, { text: dur, cls: "n" }];
      case "flowed": {
        const items = itemsPhrase(e);
        const amount = [{ text: formatTokens(e.tokens), cls: "n" }];
        if (items) {
          amount.push({ text: e.direction === "in" ? ` from ${items}` : ` in ${items}` });
        }
        return [{ text: e.label }, SEP, ...amount, SEP, { text: dur, cls: "n" }];
      }
    }
  }
  function attachmentPart(e) {
    const items = itemsPhrase(e);
    if (items) {
      return [{ text: items }];
    }
    return [{ text: `${LANE_NAMES[e.lane]} ` }, { text: formatTokens(e.tokens), cls: "n" }];
  }
  function attachmentWords(e) {
    return itemsPhrase(e) ?? `${LANE_NAMES[e.lane]} ${formatTokens(e.tokens)} tokens`;
  }
  function caption(input) {
    if (Array.isArray(input)) {
      return promptCaption(input);
    }
    const e = input;
    const dur = e.endedAt === undefined ? "" : formatDuration(e.endedAt - e.startedAt);
    switch (outcomeOf(e)) {
      case "open":
        return `${e.step}, still running`;
      case "failed":
        return `failed while ${e.step} after ${dur}; nothing came back`;
      case "interrupted":
        return `interrupted while ${e.step} after ${dur}; nothing came back`;
      case "empty":
        return `${e.label}: ${e.direction === "in" ? "nothing came back" : "nothing sent"}, ${dur}`;
      case "flowed": {
        const tok = `${formatTokens(e.tokens)} tokens`;
        if (e.direction === "out") {
          return `${e.label}: ${tok} sent to ${LANE_NAMES[e.lane]}, ${dur}`;
        }
        const items = itemsPhrase(e);
        return items ? `${e.label}: ${tok} from ${items}, ${dur}` : `${e.label}: ${tok}, ${dur}`;
      }
    }
  }
  function promptCaption(events) {
    if (events.length === 0) {
      return "nothing from SageOx with this prompt";
    }
    const running = events.find((e) => e.endedAt === undefined);
    if (running) {
      return `${running.step}, still running`;
    }
    const ok = events.filter((e) => !e.failed && !e.interrupted && e.tokens > 0);
    const bad = events.length - ok.length;
    const parts = ok.map(attachmentWords);
    let s = parts.length ? `with this prompt: ${parts.join(", ")}` : "nothing from SageOx with this prompt";
    if (ok.length > 1) {
      s += `; ${formatTokens(ok.reduce((n, e) => n + e.tokens, 0))} tokens in all`;
    }
    if (bad > 0) {
      s += `; ${bad} failed`;
    }
    return s;
  }
  function geo(width) {
    const w = Math.min(2400, Math.max(200, Math.round(width ?? TOKENS.width)));
    if (w < TOKENS.narrowBelow) {
      return { w, a: 4, b: 104, digestX: 114, labels: false };
    }
    return { w, a: 50, b: 230, digestX: 298, labels: true };
  }
  function segWidth(s) {
    return s.text.length * (s.cls === "n" ? TOKENS.monoCharW : TOKENS.sansCharW);
  }
  function fitDigest(segs, room) {
    const total = (xs) => xs.reduce((n, s) => n + segWidth(s), 0);
    if (segs.length === 0 || total(segs) <= room) {
      return segs;
    }
    const [first, ...rest] = segs;
    const restW = total(rest);
    const chars = Math.floor((room - restW) / TOKENS.sansCharW) - 1;
    if (chars >= 12 && first.cls === undefined) {
      return [{ text: `${first.text.slice(0, chars).trimEnd()}…` }, ...rest];
    }
    if (first.cls === undefined && rest[0] === SEP && total(rest.slice(1)) <= room) {
      return rest.slice(1);
    }
    const out = [];
    let used = TOKENS.sansCharW;
    for (const s of segs) {
      const w = segWidth(s);
      if (used + w > room) {
        const left = Math.floor((room - used) / (s.cls === "n" ? TOKENS.monoCharW : TOKENS.sansCharW));
        if (left > 0) {
          out.push({ text: s.text.slice(0, left).trimEnd(), cls: s.cls });
        }
        break;
      }
      out.push(s);
      used += w;
    }
    out.push({ text: "…" });
    return out;
  }
  function css(p) {
    return [
      `.l{fill:${p.text}}`,
      `.d{fill:${p.text}}`,
      `.bad{fill:${p.danger}}`,
      `.flow,.dot-out{stroke:${p.stream}}`,
      `.dot-in{fill:${p.stream}}`,
      `.open,.ring{stroke:${p.quiet}}`,
      `.nq{fill:${p.quiet}}`,
      `.nd{fill:${p.node}}`,
      `.s0,.s1{stop-color:${p.quiet}}`
    ].join("");
  }
  function style() {
    return "<style>" + `.l{font:500 ${TOKENS.labelPx}px ${FONT_MONO};letter-spacing:${TOKENS.labelTracking}px}` + `.d{font:400 ${TOKENS.digestPx}px ${FONT_SANS}}` + `.n{font-family:${FONT_MONO};font-variant-numeric:tabular-nums}` + ".bad{font-weight:500}" + ".flow{fill:none;stroke-linecap:round}" + `.open{fill:none;stroke-width:${TOKENS.quietW};stroke-dasharray:${TOKENS.openDash};stroke-linecap:round}` + `.ring{fill:none;stroke-width:${TOKENS.quietW}}` + ".dot-out{fill:none;stroke-width:1.25}" + ".s0{stop-opacity:1}.s1{stop-opacity:0}" + css(LIGHT) + `@media (prefers-color-scheme: dark){${css(DARK)}}` + "@media (prefers-reduced-motion: reduce){*{animation:none!important;transition:none!important}}" + `@media (max-width: ${TOKENS.narrowBelow - 1}px){.l{display:none}}` + "</style>";
  }
  function anim(attr, values, durMs, beginMs, extra = "") {
    return `<animate attributeName="${attr}" values="${values}" dur="${sec(durMs)}" begin="${sec(beginMs)}" fill="freeze"${extra}/>`;
  }
  function fadeIn(since) {
    return since < TOKENS.fadeMs ? anim("opacity", "0;1", TOKENS.fadeMs, -since) : "";
  }
  function digestSvg(segs, x) {
    if (segs.length === 0) {
      return "";
    }
    const runs = segs.map((s) => s.cls ? `<tspan class="${s.cls}">${esc(s.text)}</tspan>` : esc(s.text)).join("");
    return `<text class="d" x="${x}" y="${TOKENS.textY}">${runs}</text>`;
  }
  function draw(m, now, opts, level) {
    const g = geo(opts.width);
    const reduced = opts.reducedMotion === true || level === 2;
    const phase = phaseOf(m, now, reduced);
    const y = TOKENS.midY;
    const inbound = m.direction === "in";
    const from = inbound ? g.a : g.b;
    const to = inbound ? g.b : g.a;
    const dir = inbound ? 1 : -1;
    const gap = TOKENS.nodeR + 2;
    const x0 = from + dir * gap;
    const x1 = to - dir * gap;
    const parts = [];
    const sinceStart = Math.max(0, now - m.startedAt);
    const sinceEnd = m.endedAt === undefined ? 0 : Math.max(0, now - m.endedAt);
    const animate = phase === "flight";
    if (g.labels && level === 0) {
      parts.push(`<text class="l" x="0" y="${TOKENS.textY}">SAGEOX</text>`);
      parts.push(`<text class="l" x="${g.b + 8}" y="${TOKENS.textY}">SESSION</text>`);
    }
    const landed = m.outcome === "flowed";
    parts.push(`<circle class="nq" cx="${from}" cy="${y}" r="${TOKENS.nodeR}"/>`);
    if (m.outcome === "open") {
      const ringX = to;
      const end = ringX - dir * (TOKENS.ringR + 2);
      const fade = reduced ? "" : fadeIn(sinceStart);
      parts.push(`<g>${fade}<line class="open" x1="${x0}" y1="${y}" x2="${end}" y2="${y}"/>`);
      parts.push(`<circle class="ring" cx="${ringX}" cy="${y}" r="${TOKENS.ringR}"/></g>`);
    } else if (m.outcome === "flowed") {
      parts.push(`<circle class="nd" cx="${to}" cy="${y}" r="${TOKENS.nodeR}"/>`);
      const w = strokeWeight(m.tokens);
      if (animate && sinceEnd < TOKENS.fadeMs) {
        parts.push(`<g opacity="0">${anim("opacity", "1;0", TOKENS.fadeMs, -sinceEnd)}` + `<line class="open" x1="${x0}" y1="${y}" x2="${x1}" y2="${y}"/></g>`);
      }
      const draw2 = animate && sinceEnd < TOKENS.drawMs ? anim("stroke-dashoffset", "1;0", TOKENS.drawMs, -sinceEnd, ` calcMode="spline" keyTimes="0;1" keySplines="${TOKENS.easeOut}"`) : "";
      const dash = draw2 ? ' pathLength="1" stroke-dasharray="1" stroke-dashoffset="0"' : "";
      parts.push(`<line class="flow" x1="${x0}" y1="${y}" x2="${x1}" y2="${y}" stroke-width="${w}"${dash}>${draw2}</line>`);
      if (animate) {
        const n = level === 0 ? dotCount(m.tokens) : Math.min(3, dotCount(m.tokens));
        const cls = inbound ? "dot-in" : "dot-out";
        const r = inbound ? TOKENS.dotR : TOKENS.ringDotR;
        for (let i = 0;i < n; i++) {
          const begin = i * TOKENS.dotStaggerMs - sinceEnd;
          if (begin + TOKENS.dotMs <= 0) {
            continue;
          }
          parts.push(`<circle class="${cls}" cx="${x1}" cy="${y}" r="${r}" opacity="0">` + anim("cx", `${x0};${x1}`, TOKENS.dotMs, begin, ` calcMode="spline" keyTimes="0;1" keySplines="${TOKENS.easeHouse}"`) + anim("opacity", "0;0.9;0.8;0", TOKENS.dotMs, begin, ' keyTimes="0;0.12;0.88;1"') + "</circle>");
        }
      }
    } else {
      const id = `f${hash(m.key)}`;
      const sx = inbound ? x0 : x0 - TOKENS.stubLen;
      const grad = inbound ? 'x1="0" x2="1"' : 'x1="1" x2="0"';
      parts.push(`<defs><linearGradient id="${id}" ${grad} y1="0" y2="0">` + '<stop offset="0" class="s0"/><stop offset="1" class="s1"/></linearGradient></defs>');
      const fade = animate ? fadeIn(sinceEnd) : "";
      parts.push(`<circle class="nq" cx="${to}" cy="${y}" r="${TOKENS.nodeR}"/>`);
      parts.push(`<rect x="${sx}" y="${y - TOKENS.stubH / 2}" width="${TOKENS.stubLen}" height="${TOKENS.stubH}" fill="url(#${id})">${fade}</rect>`);
    }
    if (level < 2 && (level === 0 || m.outcome !== "flowed")) {
      parts.push(digestSvg(fitDigest(m.digest, g.w - g.digestX - 2), g.digestX));
    }
    return parts.join("");
  }
  var ARIA_MAX = 400;
  function wrap(m, now, opts, level, caption2) {
    const g = geo(opts.width);
    const label = caption2.length > ARIA_MAX ? `${caption2.slice(0, ARIA_MAX - 1)}…` : caption2;
    return `<svg xmlns="http://www.w3.org/2000/svg" width="${g.w}" height="${TOKENS.height}" viewBox="0 0 ${g.w} ${TOKENS.height}" ` + `preserveAspectRatio="xMinYMid meet" role="img" aria-label="${esc(label)}">` + style() + draw(m, now, opts, level) + "</svg>";
  }
  function fitSource(render, cap = SVG_SOURCE_CAP) {
    for (const level of [0, 1, 2]) {
      const s = render(level);
      if (utf8Bytes(s) <= cap) {
        return s;
      }
    }
    return render(2);
  }
  function eventModel(e) {
    return {
      key: e.id,
      direction: e.direction,
      tokens: e.tokens,
      startedAt: e.startedAt,
      endedAt: e.endedAt,
      outcome: outcomeOf(e),
      digest: laneDigest(e)
    };
  }
  function laneSvg(event, now, opts = {}) {
    const m = eventModel(event);
    const label = caption(event);
    return fitSource((level) => wrap(m, now, opts, level, label));
  }
  function promptSvg(events, now, opts = {}) {
    const label = caption(events);
    const running = events.find((e) => e.endedAt === undefined);
    const ok = events.filter((e) => !e.failed && !e.interrupted && e.tokens > 0);
    const bad = events.length - ok.length;
    const startedAt = events.reduce((t, e) => Math.min(t, e.startedAt), events[0]?.startedAt ?? now);
    const endedAt = running || events.length === 0 ? undefined : events.reduce((t, e) => Math.max(t, e.endedAt ?? t), startedAt);
    const digest = [];
    ok.forEach((e, i) => {
      if (i > 0) {
        digest.push(SEP);
      }
      digest.push(...attachmentPart(e));
    });
    if (bad > 0) {
      if (digest.length) {
        digest.push(SEP);
      }
      digest.push({ text: `${bad} failed`, cls: "bad" });
    }
    let outcome;
    if (events.length === 0) {
      outcome = "empty";
    } else if (running) {
      outcome = "open";
    } else if (ok.length) {
      outcome = "flowed";
    } else {
      outcome = events.some((e) => e.failed) ? "failed" : "empty";
    }
    const m = {
      key: events.map((e) => e.id).join("|") || "prompt",
      direction: "in",
      tokens: ok.reduce((n, e) => n + e.tokens, 0),
      startedAt,
      endedAt: events.length === 0 ? now : endedAt,
      outcome,
      digest: running ? [{ text: running.step }] : digest.length ? digest : [{ text: "nothing came back" }]
    };
    return fitSource((level) => wrap(m, now, opts, level, label));
  }

  // design/sageox-flow/entry.ts
  globalThis.scene = exports_scene;
})();
