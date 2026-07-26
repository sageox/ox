// ox plan — injected chrome boot script for an AUTHORED HTML plan (a
// developer-hand-built page ox did not generate). Runs BEFORE assets/review.js
// in the injected bundle — script tags execute in document order, and that
// ordering is load-bearing: review.js reads its config off document.body's
// data-slug / data-review-endpoint / data-review-token attributes, which this
// script must set FIRST. Vanilla, zero deps, file://-safe: an authored page
// carries no bundler, so this has to run standalone in any browser.
(function () {
  var body = document.body;
  if (!body) return;

  var data = {};
  try {
    var island = document.getElementById('ox-chrome-data');
    if (island) data = JSON.parse(island.textContent || '{}') || {};
  } catch (e) {
    data = {}; // malformed island — degrade to "no enrichment", never throw
  }

  // materialize body[data-*] BEFORE review.js's IIFE runs, so it picks up the
  // live-server wiring (or stays in static/offline mode when unset).
  try {
    if (data.slug) body.setAttribute('data-slug', data.slug);
    if (data.endpoint) body.setAttribute('data-review-endpoint', data.endpoint);
    if (data.token) body.setAttribute('data-review-token', data.token);
  } catch (e) {}

  // an authored page has no [section id]/.ox-chip/.stat/.bar-row convention of
  // its own — this is the Agentation-style "click any element" granularity
  // review.js falls back to (SELECTOR = window.OX_REVIEW_SELECTOR || …).
  try {
    window.OX_REVIEW_SELECTOR = 'h1,h2,h3,h4,p,li,tr,pre,blockquote,figure,[data-ox-section]';
  } catch (e) {}

  var TYPE_LABEL = {
    'collision': 'Collision', 'prior-art': 'Prior art', 'expert-routing': 'Expert',
    'aligns': 'Aligns', 'conflicts': 'Conflicts', 'expert-perspective': 'Expert view'
  };

  function esc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }
  function safeURL(u) {
    var raw = String(u || '').trim();
    return /^https?:\/\//i.test(raw) ? esc(raw) : '';
  }

  // floating "OX" toggle + panel: collision / prior-art / expert-routing /
  // aligns / conflicts / expert-perspective chips, plus the surfaced context
  // items. Omitted entirely when there's nothing to show — an empty button is
  // chrome without information (Linear register: no UI element without
  // information value).
  function buildOverlay() {
    var signals = Array.isArray(data.signals) ? data.signals : [];
    var context = Array.isArray(data.context) ? data.context : [];
    if (!signals.length && !context.length) return;

    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'ox-chrome-btn';
    btn.textContent = 'OX';
    btn.setAttribute('aria-label', 'SageOx team-context enrichment');
    body.appendChild(btn);

    var panel = document.createElement('div');
    panel.className = 'ox-chrome-panel';
    var html = '<div class="ox-chrome-panel-h">Team context</div>';
    signals.forEach(function (s) {
      var type = s && s.type ? String(s.type) : '';
      var label = esc((s && s.label) || TYPE_LABEL[type] || type);
      var why = esc(s && s.why);
      var href = s ? safeURL(s.url) : '';
      var link = href ? ' <a href="' + href + '" target="_blank" rel="noopener noreferrer">view ↗</a>' : '';
      html += '<div class="ox-chrome-chip" data-type="' + esc(type) + '"><b>' + label + '</b><span>' + why + '</span>' + link + '</div>';
    });
    if (context.length) {
      html += '<div class="ox-chrome-context-h">Context surfaced</div>';
      context.forEach(function (c) {
        html += '<div class="ox-chrome-context-item"><b>' + esc(c && c.kind) + '</b>' + esc(c && c.title) + '</div>';
      });
    }
    panel.innerHTML = html;
    body.appendChild(panel);

    btn.onclick = function (ev) {
      ev.stopPropagation();
      panel.classList.toggle('open');
    };
    document.addEventListener('click', function (ev) {
      if (panel.classList.contains('open') && ev.target !== btn && !panel.contains(ev.target)) {
        panel.classList.remove('open');
      }
    });
  }

  // slim credit strip, always last in the body — "earned" wording
  // (footer_credit) only appears when enrichment actually fired; the
  // always-on "Rendered by ox plan" nod matches the ox-generated render's
  // same subtle brand mark so an authored plan reads as recognizably ox too.
  function buildFooter() {
    var parts = [];
    if (data.footer_credit) parts.push('Team context enriched by SageOx');
    var sessionHref = safeURL(data.session_url);
    if (sessionHref) parts.push('<a href="' + sessionHref + '" target="_blank" rel="noopener noreferrer">View session</a>');
    parts.push('Rendered by <code>ox plan</code> · SageOx');
    var foot = document.createElement('div');
    foot.className = 'ox-chrome-foot';
    foot.innerHTML = parts.join(' · ');
    body.appendChild(foot);
  }

  try { buildOverlay(); } catch (e) {}
  try { buildFooter(); } catch (e) {}
})();
