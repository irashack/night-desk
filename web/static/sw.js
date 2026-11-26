'use strict';
const SHELL = 'nd-shell-v2', PAPER = 'nd-paper-v1';
const SHELL_URLS = ['/static/app.css', '/static/app.js', '/static/icon-32.png', '/static/apple-touch-icon.png', '/static/icon-192.png', '/static/icon-512.png', '/static/icon-maskable-512.png', '/static/fonts/oldstandard-regular.woff2', '/static/fonts/oldstandard-bold.woff2', '/static/fonts/unifrakturcook-bold.woff2', '/static/fonts/librefranklin-var.woff2'];
let generation = 0, work = Promise.resolve();
const serial = (fn) => { const result = work.then(fn); work = result.catch(() => {}); return result; };
async function clearPaper() { generation++; await caches.delete(PAPER); }
async function clearAll() { generation++; await Promise.all((await caches.keys()).filter((k) => k.startsWith('nd-')).map((k) => caches.delete(k))); }
self.addEventListener('install', (event) => { event.waitUntil(caches.open(SHELL).then((c) => c.addAll(SHELL_URLS)).then(() => self.skipWaiting())); });
self.addEventListener('activate', (event) => {
  event.waitUntil(Promise.all([self.clients.claim(), caches.keys().then((keys) => Promise.all(keys.filter((k) => k.startsWith('nd-') && k !== SHELL && k !== PAPER).map((k) => caches.delete(k))))]));
});
async function saveLatest(response, revision) {
  if (revision !== generation) return;
  const text = await response.clone().text();
  const owner = text.match(/<meta name="offline-owner" content="([A-Za-z0-9_-]+)">/)?.[1];
  const date = text.match(/data-date="(\d{4}-\d{2}-\d{2})"/)?.[1];
  if (!owner || !date || response.redirected || response.status !== 200) { await clearPaper(); return; }
  let cache = await caches.open(PAPER);
  const old = await cache.match('/__nd_owner');
  if (old && await old.text() !== owner) { await clearPaper(); revision = generation; cache = await caches.open(PAPER); }
  // Keep exactly one edition and its images, not an ever-growing archive.
  const oldDate = await cache.match('/__nd_date');
  if (oldDate && await oldDate.text() !== date) { await caches.delete(PAPER); cache = await caches.open(PAPER); }
  await cache.put('/__nd_owner', new Response(owner)); await cache.put('/__nd_date', new Response(date));
  await cache.put('/', response.clone()); await cache.put('/e/' + date, response.clone());
  const urls = new Set([...text.matchAll(/(?:src|href)="(\/(?:img\/[a-f0-9]{64}\.jpg|static\/[^"<>]+))"/g)].map((m) => m[1]));
  const keep = new Set(['/__nd_owner', '/__nd_date', '/', '/e/' + date, ...urls]);
  for (const req of await cache.keys()) if (!keep.has(new URL(req.url).pathname)) await cache.delete(req);
  for (const path of urls) {
    const image = path.startsWith('/img/');
    try {
      const res = await fetch(path, {credentials: 'same-origin'});
      if (res.ok && !res.redirected && (image ? res.headers.get('Content-Type')?.startsWith('image/jpeg') : true)) {
        // Logout invalidates even an in-flight edition/image fill.
        if (revision !== generation) return;
        await (image ? cache : await caches.open(SHELL)).put(path, res);
      }
    } catch (_) {}
  }
}
self.addEventListener('message', (event) => {
  if (event.data?.type === 'LOGOUT') { generation++; event.waitUntil(serial(clearAll)); }
  if (event.data?.type === 'CACHE_LATEST') {
    const revision = generation;
    event.waitUntil(serial(async () => { try { await saveLatest(await fetch('/', {cache: 'no-store', credentials: 'same-origin'}), revision); } catch (_) {} }));
  }
});
self.addEventListener('fetch', (event) => {
  const req = event.request, url = new URL(req.url);
  if (url.origin !== self.location.origin || req.method !== 'GET') return;
  if (url.pathname.startsWith('/static/')) {
    event.respondWith((async () => {
      const c = await caches.open(SHELL), hit = await c.match(req); if (hit) return hit;
      const res = await fetch(req); if (res.ok) await c.put(req, res.clone()); return res;
    })()); return;
  }
  if (url.pathname.startsWith('/img/')) {
    event.respondWith((async () => { const c = await caches.open(PAPER); return await c.match(req) || fetch(req); })()); return;
  }
  // Offline, a notification click shows the saved default paper.
  if (req.mode === 'navigate' && url.pathname === OPEN) {
    event.respondWith(fetch(req).catch(async () => await (await caches.open(PAPER)).match('/') || fetch(req)));
    return;
  }
  if (req.mode !== 'navigate' || (url.pathname !== '/' && !/^\/e\/\d{4}-\d{2}-\d{2}$/.test(url.pathname))) return;
  const revision = generation;
  const network = fetch(req);
  event.waitUntil(network.then((res) => { const copy = res.clone(); return serial(async () => {
    if (url.pathname === '/') await saveLatest(copy, revision);
    // A refusal or redirect may mean access ended (an unmapped reader gets
    // 404); a server error says nothing about it, so the saved copy stays.
    else if (res.redirected || (!res.ok && res.status < 500)) await clearPaper();
  }); }).catch(() => {}));
  event.respondWith(network.catch(async () => {
    const cached = await (await caches.open(PAPER)).match(url.pathname);
    return cached || new Response('This paper is not saved offline. Connect and try again.', {status: 503, headers: {'Content-Type': 'text/plain', 'Content-Security-Policy': "default-src 'none'; frame-ancestors 'none'; base-uri 'none'"}});
  }));
});
// Pushes carry no payload: the notification is fixed, and a newer one replaces it.
self.addEventListener('push', (event) => {
  event.waitUntil(self.registration.showNotification('Night Desk', {body: "Today's paper is ready", icon: '/static/icon-192.png', tag: 'nd-edition'}));
});
// Pushes do not say which paper they are for, so a click goes through
// /push/open, which redirects to the paper that announced the newest edition.
const OPEN = '/push/open';
self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  event.waitUntil((async () => {
    const open = (await self.clients.matchAll({type: 'window'})).find((c) => new URL(c.url).origin === self.location.origin);
    if (!open) return self.clients.openWindow(OPEN);
    await open.focus();
    await open.navigate(OPEN);
  })());
});
