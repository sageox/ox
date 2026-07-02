// ox plan review — offline shell. Network-first for the plan page; when the
// review server is down (process exited, idle-closed, machine napped), a
// reload serves the last-seen copy instead of a connection error, so the plan
// stays readable and the review layer (review.js) can show its disconnected
// banner. Marks live in localStorage, so nothing is lost while offline.
// Scope: only GET / on this origin — every other request passes through.
var CACHE = 'ox-plan-review-v1';
self.addEventListener('install', function () { self.skipWaiting(); });
self.addEventListener('activate', function (e) { e.waitUntil(self.clients.claim()); });
self.addEventListener('fetch', function (e) {
  var url = new URL(e.request.url);
  if (e.request.method !== 'GET' || url.origin !== self.location.origin || url.pathname !== '/') return;
  e.respondWith(
    fetch(e.request).then(function (r) {
      if (r.ok) {
        var copy = r.clone();
        caches.open(CACHE).then(function (c) { c.put('/', copy); }).catch(function () {});
      }
      return r;
    }).catch(function () {
      return caches.match('/').then(function (hit) {
        return hit || new Response(
          'The review server is not running. Restart it with:  ox plan review <slug>',
          { status: 503, headers: { 'Content-Type': 'text/plain; charset=utf-8' } }
        );
      });
    })
  );
});
