// Nearhost's service worker. It caches the shell only: the offline page,
// the icons and the manifest. It never caches the page itself, a console
// view, the event stream, documents, pairing, auth or anything on another
// origin.
const CACHE = 'nearhost-shell-{{.Version}}';
// A VPN tunnel with no network swallows requests rather than failing them,
// so navigations give up after a few seconds.
const TIMEOUT_MS = 4000;
const withTimeout = p => Promise.race([p, new Promise((_, no) => setTimeout(() => no(new Error('timeout')), TIMEOUT_MS))]);
const SHELL = ['/offline.html', '/icon-180.png', '/icon-192.png', '/icon-512.png', '/manifest.webmanifest'];
self.addEventListener('install', e => { e.waitUntil(caches.open(CACHE).then(c => c.addAll(SHELL))); self.skipWaiting(); });
self.addEventListener('activate', e => {
  e.waitUntil(caches.keys()
    .then(keys => Promise.all(keys.filter(k => k.startsWith('nearhost-shell-') && k !== CACHE).map(k => caches.delete(k))))
    .then(() => self.clients.claim()));
});
self.addEventListener('fetch', e => {
  const u = new URL(e.request.url);
  if (u.origin !== location.origin) return;
  if (e.request.mode === 'navigate') {
    // A POST (an approval answer) goes straight to the network.
    if (e.request.method !== 'GET') return;
    e.respondWith(withTimeout(fetch(e.request)).catch(() => caches.match('/offline.html')));
    return;
  }
  if (SHELL.includes(u.pathname)) {
    e.respondWith(caches.match(e.request).then(r => r || fetch(e.request)));
  }
});
