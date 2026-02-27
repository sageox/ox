// initialize mermaid (guard against blocked CDN)
if (typeof mermaid !== 'undefined') {
  mermaid.initialize({ startOnLoad: false, theme: 'dark' });
}

// render mermaid diagrams in pre-rendered markdown
document.addEventListener('DOMContentLoaded', () => {
  if (typeof mermaid === 'undefined') return;
  document.querySelectorAll('.message-content pre code.language-mermaid, .message-content .language-mermaid').forEach((mermaidEl, i) => {
    const code = mermaidEl.textContent;
    const msg = mermaidEl.closest('.message');
    const id = 'mermaid-' + (msg ? msg.id : 'g') + '-' + i;
    const container = document.createElement('div');
    container.className = 'mermaid-container';
    mermaidEl.parentElement.replaceWith(container);
    mermaid.render(id, code).then(result => {
      container.innerHTML = result.svg;
    }).catch(() => {
      container.innerHTML = '<pre class="mermaid-error">' + code + '</pre>';
    });
  });
});

// view mode: conversation (default) or full
function setViewMode(mode) {
  document.body.classList.remove('view-conversation', 'view-full');
  document.body.classList.add('view-' + mode);

  // update toggle buttons
  document.querySelectorAll('.view-btn').forEach(btn => {
    btn.classList.toggle('view-btn-active', btn.dataset.view === mode);
  });

  // in full mode, show system messages; in conversation mode, hide them
  document.querySelectorAll('.message-system, .message-info').forEach(el => {
    el.style.display = mode === 'full' ? '' : 'none';
  });

  try { localStorage.setItem('sageox-view-mode', mode); } catch(e) {}
}

// restore view mode from localStorage
document.addEventListener('DOMContentLoaded', () => {
  let saved = 'conversation';
  try { saved = localStorage.getItem('sageox-view-mode') || 'conversation'; } catch(e) {}
  setViewMode(saved);
});

// scroll to chapter
function scrollToChapter(id) {
  const el = document.getElementById('chapter-' + id);
  if (el) {
    el.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }
}

// navigate to specific message by ID
function navigateToMessage(seq) {
  const msg = document.getElementById('msg-' + seq);
  if (msg) {
    // if inside a closed work block, open it first
    const workBlock = msg.closest('.work-block-tools');
    if (workBlock) {
      const details = workBlock.closest('details');
      if (details && !details.open) details.open = true;
    }

    msg.scrollIntoView({ behavior: 'smooth', block: 'center' });
    msg.focus();
    msg.style.transition = 'box-shadow 0.3s ease';
    msg.style.boxShadow = '0 0 0 3px var(--color-secondary), 0 4px 20px rgba(200,162,120,0.4)';
    setTimeout(() => { msg.style.boxShadow = ''; }, 2000);
  }
}

// keyboard navigation
const ahaMoments = document.querySelectorAll('.message-aha');
let currentAhaIndex = -1;

document.addEventListener('keydown', (e) => {
  if (e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA') return;

  if (e.key === 'j' || e.key === 'ArrowDown') {
    navigateMessage(1);
  } else if (e.key === 'k' || e.key === 'ArrowUp') {
    navigateMessage(-1);
  } else if (e.key === 'Enter' || e.key === ' ') {
    e.preventDefault();
    toggleCurrentDetails();
  } else if (e.key === 'a' || e.key === 'A') {
    navigateAhaMoment(e.shiftKey ? -1 : 1);
  } else if (e.key === 'v') {
    // toggle view mode
    const current = document.body.classList.contains('view-full') ? 'full' : 'conversation';
    setViewMode(current === 'full' ? 'conversation' : 'full');
  } else if (e.key >= '1' && e.key <= '9') {
    scrollToChapter(parseInt(e.key));
  } else if (e.key === '?' || e.key === '/') {
    showShortcuts();
  }
});

let currentMessageIndex = -1;
const messages = document.querySelectorAll('.message');

function navigateMessage(direction) {
  const visible = Array.from(messages).filter(m => m.offsetParent !== null);
  if (visible.length === 0) return;
  const curIdx = visible.indexOf(messages[currentMessageIndex]);
  const newIdx = Math.max(0, Math.min(visible.length - 1, (curIdx < 0 ? 0 : curIdx) + direction));
  currentMessageIndex = Array.from(messages).indexOf(visible[newIdx]);
  visible[newIdx].scrollIntoView({ behavior: 'smooth', block: 'center' });
  visible[newIdx].focus();
}

function navigateAhaMoment(direction) {
  if (ahaMoments.length === 0) return;
  currentAhaIndex = (currentAhaIndex + direction + ahaMoments.length) % ahaMoments.length;
  if (currentAhaIndex < 0) currentAhaIndex = ahaMoments.length - 1;
  ahaMoments[currentAhaIndex].scrollIntoView({ behavior: 'smooth', block: 'center' });
  ahaMoments[currentAhaIndex].focus();
  currentMessageIndex = Array.from(messages).indexOf(ahaMoments[currentAhaIndex]);
}

function toggleCurrentDetails() {
  if (currentMessageIndex >= 0) {
    const details = messages[currentMessageIndex].querySelector('details');
    if (details) details.open = !details.open;
  }
}

// iframe height communication
if (window !== window.parent) {
  function postHeight() {
    window.parent.postMessage(
      { type: 'sageox-session-resize', height: document.documentElement.scrollHeight },
      '*'
    );
  }
  window.addEventListener('load', postHeight);
  window.addEventListener('resize', postHeight);
  new MutationObserver(postHeight).observe(document.body, { childList: true, subtree: true, attributes: true });
}

function showShortcuts() {
  const existing = document.querySelector('.shortcuts-modal');
  if (existing) { existing.remove(); return; }
  const modal = document.createElement('div');
  modal.className = 'shortcuts-modal';
  modal.innerHTML = '<div class="shortcuts-content"><h3>Keyboard Shortcuts</h3><ul>' +
    '<li><kbd>j</kbd> / <kbd>\u2193</kbd> Next message</li>' +
    '<li><kbd>k</kbd> / <kbd>\u2191</kbd> Previous message</li>' +
    '<li><kbd>a</kbd> Next key moment</li>' +
    '<li><kbd>Shift+a</kbd> Previous key moment</li>' +
    '<li><kbd>v</kbd> Toggle view mode</li>' +
    '<li><kbd>1-9</kbd> Jump to chapter</li>' +
    '<li><kbd>Enter</kbd> / <kbd>Space</kbd> Toggle details</li>' +
    '<li><kbd>?</kbd> Toggle this help</li>' +
    '</ul><p class="shortcuts-close">Press any key to close</p></div>';
  document.body.appendChild(modal);
  setTimeout(() => {
    document.addEventListener('keydown', function closeModal() {
      modal.remove();
      document.removeEventListener('keydown', closeModal);
    }, { once: true });
  }, 100);
}
