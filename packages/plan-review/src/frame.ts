// The document tools a host injects into a plan it frames with
// sandbox="allow-scripts": an opaque origin with no storage and no access to
// the host, and, under the host's CSP, no fetch. The script needs none of
// them. It talks to the parent window only, for the instance the host named in
// the script tag's data-instance attribute or the frame URL's #i= fragment.

import css from './document.css';
import { startDocumentTools } from './document-tools.js';
import { connectParent } from './ports.js';

(function () {
  const me = document.currentScript;
  const instance = (me && me.getAttribute('data-instance')) || (/[#&]i=([A-Za-z0-9_-]{8,64})/.exec(location.hash) || [])[1] || '';
  if (window.parent === window || !/^[A-Za-z0-9_-]{8,64}$/.test(instance)) return;
  const start = () => {
    const style = document.createElement('style');
    style.textContent = css;
    (document.head || document.documentElement).appendChild(style);
    startDocumentTools(connectParent(instance), { forwardKeys: true });
  };
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start);
  else start();
})();
