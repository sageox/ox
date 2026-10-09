(() => {
  'use strict';
  const token = new URLSearchParams(location.hash.slice(1)).get('token');
  const $ = id => document.getElementById(id);
  const node = (tag, cls, text) => {
    const el = document.createElement(tag);
    if (cls) el.className = cls;
    if (text !== undefined) el.textContent = text;
    return el;
  };
  const state = { rows: [], selected: new Set(), focus: '', query: '', filter: 'all', finished: false, submitting: false, request: 0 };
  // Keep only the current conversation in the DOM. The CLI owns the bounded
  // preview cache and rechecks the source snapshot on every detail request.
  const openings = new Map(), pending = new Map(), failed = new Set(), queryMatches = new Set();
  // Copy short labels so a substring cannot keep a long pasted request alive.
  // Full requests exist only while searching or reading the current detail.
  const openingLabel = opening => {
    const text = opening || 'No opening request available';
    const label = Array.from(text.slice(0, text.length > 240 ? 239 : 240)).join('');
    return text.length > 240 ? label + '…' : label;
  };
  const labels = { ready: 'Ready', ineligible: 'Unavailable', in_progress: 'In progress', already_imported: 'Already imported', recorded_live: 'Recorded by ox', needs_summarizer: 'Needs a summarizer', not_shared: 'Kept local' };
  let observer, detailAbort, scanAbort;
  const agentLabel = agent => agent === 'claude' ? 'Claude Code' : agent === 'codex' ? 'Codex' : agent;
  const dateLabel = value => value && !Number.isNaN(Date.parse(value)) ? new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric', year: 'numeric' }).format(new Date(value)) : 'Date unavailable';
  const announce = message => { $('announcement').textContent = message; };
  async function api(path, body, signal) {
    const response = await fetch(path, { method: body === undefined ? 'GET' : 'POST', credentials: 'omit', cache: 'no-store', signal, headers: { 'X-Import-Token': token || '', ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
    const result = await response.json();
    if (!response.ok) throw new Error(result.error || 'The local browser could not complete this request.');
    return result;
  }
  function updateCount() {
    $('selected-count').textContent = `${state.selected.size} session${state.selected.size === 1 ? '' : 's'} selected`;
    $('submit').disabled = state.submitting || state.finished;
  }
  async function getOpening(id) {
    if (openings.has(id) || failed.has(id) || state.finished || state.query) return;
    if (pending.has(id)) return pending.get(id).promise;
    const controller = new AbortController();
    const promise = api(`/api/preview?id=${encodeURIComponent(id)}&excerpt=1`, undefined, controller.signal).then(preview => {
      if (controller.signal.aborted || state.finished) return;
      openings.set(id, openingLabel(preview.opening_request));
      const title = document.querySelector(`[data-title="${CSS.escape(id)}"]`);
      if (title) title.textContent = openings.get(id);
    }).catch(() => {
      if (controller.signal.aborted || state.finished) return;
      failed.add(id);
      const title = document.querySelector(`[data-title="${CSS.escape(id)}"]`);
      if (title) title.textContent = 'Content preview unavailable';
    }).finally(() => { if (pending.get(id)?.promise === promise) pending.delete(id); });
    pending.set(id, { promise, controller });
    return promise;
  }
  function cancelOpeningLoads() {
    for (const request of pending.values()) request.controller.abort();
    pending.clear();
  }
  function renderList() {
    if (observer) observer.disconnect();
    const q = state.query.toLowerCase();
    const shown = state.rows.filter(row => (state.filter === 'all' || (state.filter === 'ready' ? row.state === 'ready' : row.state !== 'ready')) && (!q || queryMatches.has(row.native_id) || `${row.native_id} ${agentLabel(row.agent)}`.toLowerCase().includes(q)));
    $('list-count').textContent = `${shown.length} shown · ${state.rows.length} total`;
    const fragment = document.createDocumentFragment();
    for (const row of shown) {
      const id = row.native_id;
      const item = node('div', `session${id === state.focus ? ' active' : ''}${row.state !== 'ready' ? ' unavailable' : ''}`);
      const check = node('input'); check.type = 'checkbox'; check.checked = state.selected.has(id); check.disabled = row.state !== 'ready' || state.finished; check.dataset.select = id;
      check.setAttribute('aria-label', `Include session ${id.slice(0, 8)}`);
      check.addEventListener('change', () => { check.checked ? state.selected.add(id) : state.selected.delete(id); updateCount(); });
      const button = node('button'); button.type = 'button'; button.dataset.focus = id; button.setAttribute('aria-pressed', String(id === state.focus));
      const title = node('strong', '', openings.get(id) || (failed.has(id) ? 'Content preview unavailable' : 'Loading opening request…')); title.dataset.title = id;
      button.append(title, node('small', '', `${agentLabel(row.agent)} · ${dateLabel(row.started_at)} · ${row.messages} messages`), node('small', 'state', `${labels[row.state] || 'Unavailable'} · ${id.slice(0, 8)}`));
      button.addEventListener('click', () => focus(id)); item.append(check, button); fragment.append(item);
    }
    if (!shown.length) fragment.append(node('p', 'muted notice', 'No matching sessions. Your selection is preserved.'));
    $('session-list').replaceChildren(fragment);
    observer = new IntersectionObserver(entries => { for (const entry of entries) if (entry.isIntersecting) { observer.unobserve(entry.target); getOpening(entry.target.dataset.focus); } }, { root: document.querySelector('.picker'), rootMargin: '80px' });
    for (const button of $('session-list').querySelectorAll('[data-focus]')) observer.observe(button);
  }
  async function focus(id) {
    if (state.finished) return;
    detailAbort?.abort(); detailAbort = new AbortController();
    state.focus = id; const request = ++state.request;
    renderList();
    $('session-detail').replaceChildren(node('p', 'muted', 'Reading the redacted conversation…'));
    try {
      const preview = await api(`/api/preview?id=${encodeURIComponent(id)}`, undefined, detailAbort.signal);
      if (state.focus !== id || state.request !== request || state.finished) return;
      openings.set(id, openingLabel(preview.opening_request)); failed.delete(id);
      if (state.query) {
        (preview.opening_request || '').toLowerCase().includes(state.query.toLowerCase()) ? queryMatches.add(id) : queryMatches.delete(id);
      }
      renderList(); renderDetail(preview);
    } catch (error) {
      if (state.focus !== id || state.request !== request || state.finished) return;
      failed.add(id);
      const retry = node('button', '', 'Retry preview'); retry.type = 'button'; retry.addEventListener('click', () => { failed.delete(id); focus(id); });
      $('session-detail').replaceChildren(node('h2', '', 'Preview unavailable'), node('p', 'muted', error.message), retry);
    }
  }
  function renderDetail(preview) {
    const row = state.rows.find(r => r.native_id === preview.native_id);
    const fragment = document.createDocumentFragment();
    fragment.append(node('span', 'badge', labels[row.state] || 'Unavailable'), node('h2', '', preview.opening_request || 'No opening request available'), node('div', 'meta', `${agentLabel(row.agent)} · ${dateLabel(row.started_at)} · ${row.messages} messages · ${row.native_id}`));
    if (row.state !== 'ready') fragment.append(node('p', 'notice', row.reason || 'You can read this session. It is unavailable for import in this run.'));
    fragment.append(node('div', 'eyebrow', 'Conversation map · Human requests in order'));
    const map = node('div', 'prompt-map'); map.setAttribute('aria-label', 'Jump to a human request');
    const heading = node('h3', '', 'Opening request');
    const quote = node('div', 'quote', preview.opening_request || 'No opening request available');
    for (const [i, prompt] of (preview.prompts || []).entries()) {
      const button = node('button'); button.type = 'button'; button.setAttribute('aria-pressed', String(i === 0)); button.append(node('b', '', `Request ${i + 1}`), node('span', '', prompt.content));
      button.addEventListener('click', () => { map.querySelectorAll('button').forEach(b => b.setAttribute('aria-pressed', String(b === button))); heading.textContent = i === 0 ? 'Opening request' : `Human request ${i + 1}`; quote.textContent = prompt.content; });
      map.append(button);
    }
    fragment.append(map, heading, quote);
    const reply = node('div', 'block'); reply.append(node('h3', '', 'Last AI reply'), node('p', 'text', preview.last_reply || 'No AI text reply available'), node('p', 'muted', 'A source excerpt; completion is not inferred.')); fragment.append(reply);
    const reader = node('details'); reader.append(node('summary', '', 'Read the retained conversation'));
    const conversation = node('div', 'conversation');
    const entries = preview.entries || [];
    const promptIndices = new Set((preview.prompts || []).map(prompt => prompt.entry_index));
    let toolGroup = null;
    for (const [i, entry] of entries.entries()) {
      if (entry.type === 'tool') {
        if (!toolGroup) { toolGroup = node('details', 'tool-group'); toolGroup.append(node('summary', '', 'Tool activity')); conversation.append(toolGroup); }
        const block = node('article', 'entry'); block.append(node('div', `eyebrow${entry.is_error ? ' warning' : ''}`, `${entry.tool_name || 'Tool'}${entry.is_error ? ' · Failed' : ''}`));
        for (const [label, value] of [['Content', entry.content], ['Input', entry.tool_input], ['Output', entry.tool_output]]) if (value) block.append(node('div', 'eyebrow', label), node('p', 'text', value));
        toolGroup.append(block); continue;
      }
      toolGroup = null;
      if (entry.type === 'system' || (entry.type === 'user' && !promptIndices.has(i))) {
        const context = node('details', 'entry'); context.append(node('summary', '', 'Session context'), node('p', 'text', entry.content || '')); conversation.append(context); continue;
      }
      const article = node('article', 'entry'); article.id = `entry-${i}`; article.append(node('div', 'eyebrow', entry.type === 'user' ? 'Human request' : entry.type === 'assistant' ? 'AI reply' : 'Session entry'), node('p', 'text', entry.content || '')); conversation.append(article);
    }
    reader.append(conversation); fragment.append(reader);
    $('session-detail').replaceChildren(fragment); $('session-detail').scrollTop = 0;
  }
  let scanGeneration = 0;
  async function scanOpenings() {
    const generation = ++scanGeneration;
    scanAbort?.abort(); cancelOpeningLoads(); queryMatches.clear();
    renderList();
    if (!state.query) { $('scan-status').textContent = ''; return; }
    const controller = scanAbort = new AbortController(), query = state.query.toLowerCase();
    // Labels cannot answer a new full-text query. Stream at most two complete
    // openings, retain only matching IDs and copied labels, then discard text.
    const queue = state.rows.filter(row => !failed.has(row.native_id));
    let next = 0, completed = 0;
    $('scan-status').textContent = queue.length ? 'Searching requests…' : '';
    const worker = async () => {
      while (next < queue.length && generation === scanGeneration && !state.finished) {
        const row = queue[next++], id = row.native_id;
        try {
          const preview = await api(`/api/preview?id=${encodeURIComponent(id)}&excerpt=1`, undefined, controller.signal);
          if (generation !== scanGeneration || controller.signal.aborted || state.finished) return;
          openings.set(id, openingLabel(preview.opening_request));
          if ((preview.opening_request || '').toLowerCase().includes(query)) queryMatches.add(id);
        } catch (error) {
          if (generation !== scanGeneration || controller.signal.aborted || state.finished) return;
          failed.add(id); queryMatches.delete(id);
        }
        completed++; renderList();
        $('scan-status').textContent = completed < queue.length ? `Reading ${completed}/${queue.length}` : '';
      }
    };
    await Promise.all([worker(), worker()]);
    if (generation === scanGeneration && !state.finished && state.query && failed.size) $('scan-status').textContent = `${failed.size} unreadable`;
  }
  async function finish(canceled) {
    if (state.submitting || state.finished) return;
    state.submitting = true; updateCount();
    try {
      await api(canceled ? '/api/cancel' : '/api/selection', canceled ? {} : { ids: state.rows.filter(row => state.selected.has(row.native_id)).map(row => row.native_id) });
      state.finished = true; ++state.request; ++scanGeneration; observer?.disconnect(); detailAbort?.abort(); scanAbort?.abort(); cancelOpeningLoads();
      $('scan-status').textContent = '';
      document.querySelectorAll('button,input,select').forEach(el => { el.disabled = true; });
      announce(canceled ? 'Review canceled. Nothing was imported. You can close this tab.' : 'Your selection returned to the terminal. Review and confirm there to import. You can close this tab.');
    } catch (error) { announce(error.message + ' Your selection is still here.'); }
    finally { state.submitting = false; updateCount(); }
  }
  $('search').addEventListener('input', event => { state.query = event.target.value; scanOpenings(); });
  $('eligibility').addEventListener('change', event => { state.filter = event.target.value; renderList(); });
  $('all').addEventListener('click', () => { state.rows.filter(row => row.state === 'ready').forEach(row => state.selected.add(row.native_id)); renderList(); updateCount(); });
  $('none').addEventListener('click', () => { state.selected.clear(); renderList(); updateCount(); });
  $('submit').addEventListener('click', () => finish(false)); $('cancel').addEventListener('click', () => finish(true));
  async function start() {
    if (!token) { $('session-detail').replaceChildren(node('h2', '', 'Open this browser from the terminal'), node('p', 'muted', 'This local reader needs the session review link opened by ox.')); announce('Return to the terminal and open the browser again.'); return; }
    try {
      const result = await api('/api/sessions'); state.rows = result.sessions || [];
      for (const row of state.rows) if (row.selected && row.state === 'ready') state.selected.add(row.native_id);
      const destination = node('span', '', `Destination: ${result.team || 'Your team'} · ${result.repo_id}`);
      const visibility = node('span', result.visibility === 'public' ? 'public' : '', result.visibility === 'public' ? 'PUBLIC · Anyone can read imported sessions' : result.visibility === 'private' ? 'Private · Readable by team members' : 'Visibility unknown · Review in the terminal');
      $('destination').replaceChildren(destination, visibility);
      renderList(); updateCount();
      if (state.rows.length) await focus((state.rows.find(row => state.selected.has(row.native_id)) || state.rows[0]).native_id);
      else $('session-detail').replaceChildren(node('h2', '', 'No sessions in this review'), node('p', 'muted', 'Return to the terminal to change the session filters.'));
    } catch (error) { $('session-detail').replaceChildren(node('h2', '', 'Local review unavailable'), node('p', 'muted', error.message)); announce('Return to the terminal and open the browser again.'); }
  }
  start();
})();
