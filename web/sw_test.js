'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const {readFileSync} = require('node:fs');
const vm = require('node:vm');
const origin = 'https://paper.test';
function harness() {
  const handlers = {}, stores = new Map();
  const key = (v) => new URL(typeof v === 'string' ? v : v.url, origin).href;
  const cache = () => {
    const entries = new Map();
    return {
      async match(req) { return entries.get(key(req))?.clone(); },
      async keys() { return [...entries.keys()].map((url) => new Request(url)); },
      async delete(req) { return entries.delete(key(req)); },
      async put(req, res) { entries.set(key(req), res.clone()); },
      async addAll(urls) { for (const url of urls) await this.put(url, new Response('shell')); },
    };
  };
  const caches = {
    async open(name) { if (!stores.has(name)) stores.set(name, cache()); return stores.get(name); },
    async keys() { return [...stores.keys()]; },
    async delete(name) { return stores.delete(name); },
  };
  let network = async () => { throw new Error('offline'); };
  const context = vm.createContext({self: {location: {origin}, addEventListener(type, fn) { handlers[type] = fn; }, clients: {claim: async () => {}}, skipWaiting: async () => {}}, URL, Response, Request, caches, fetch: (...args) => network(...args)});
  vm.runInContext(readFileSync(__dirname + '/static/sw.js', 'utf8'), context);
  return {
    stores, caches,
    setNetwork(fn) { network = fn; },
    async dispatch(type, data) {
      const pending = []; let response;
      handlers[type]({...data, waitUntil(p) { pending.push(p); }, respondWith(p) { response = p; }});
      const result = response ? await response : undefined;
      await Promise.all(pending); return result;
    },
    async navigate(path) { return this.dispatch('fetch', {request: {url: origin + path, method: 'GET', mode: 'navigate'}}); },
  };
}
const sha = 'a'.repeat(64);
const html = (owner = 'owner-a', date = '2026-10-27') => `<meta name="offline-owner" content="${owner}"><main data-date="${date}"><img src="/img/${sha}.jpg"><script src="/static/app.js?v=one"></script></main>`;
const documentResponse = (owner, date) => new Response(html(owner, date), {headers: {'Content-Type': 'text/html'}});
test('latest HTML is network first; shell and retained images are cache first', async () => {
  const h = harness(); await h.dispatch('install', {}); await h.dispatch('activate', {});
  let calls = [];
  h.setNetwork(async (req) => {
    const path = typeof req === 'string' ? req : new URL(req.url).pathname; calls.push(path);
    return path.startsWith('/img/') ? new Response('jpeg', {headers: {'Content-Type': 'image/jpeg'}}) : path.startsWith('/static/') ? new Response('script') : documentResponse();
  });
  let res = await h.navigate('/'); assert.match(await res.text(), /owner-a/); assert.equal(calls.length, 3);
  h.setNetwork(async () => { throw new Error('offline'); });
  res = await h.navigate('/'); assert.match(await res.text(), /owner-a/);
  res = await h.navigate('/e/2026-10-27'); assert.match(await res.text(), /owner-a/);
  res = await h.navigate('/e/2026-10-26'); assert.equal(res.status, 503);
  res = await h.dispatch('fetch', {request: {url: origin + '/img/' + sha + '.jpg', method: 'GET'}}); assert.equal(await res.text(), 'jpeg');
  res = await h.dispatch('fetch', {request: {url: origin + '/static/app.js', method: 'GET'}}); assert.equal(await res.text(), 'shell');
});
test('only the latest date is retained; an owner change discards old paper data', async () => {
  const h = harness();
  h.setNetwork(async (req) => typeof req === 'string' ? new Response('asset') : documentResponse('owner-a', '2026-10-27'));
  await h.navigate('/');
  h.setNetwork(async (req) => typeof req === 'string' ? new Response('asset') : documentResponse('owner-b', '2026-10-28'));
  await h.navigate('/');
  h.setNetwork(async () => { throw new Error('offline'); });
  assert.equal((await h.navigate('/e/2026-10-27')).status, 503);
  assert.match(await (await h.navigate('/')).text(), /owner-b/);
});
test('authorization denial and unmapped HTML never fall back to old personal HTML', async () => {
  const h = harness();
  h.setNetwork(async (req) => typeof req === 'string' ? new Response('asset') : documentResponse()); await h.navigate('/');
  h.setNetwork(async () => new Response('Sign in', {status: 401})); assert.equal((await h.navigate('/')).status, 401);
  h.setNetwork(async () => { throw new Error('offline'); }); assert.equal((await h.navigate('/')).status, 503);
  h.setNetwork(async () => new Response('No paper for you yet')); await h.navigate('/');
  h.setNetwork(async () => { throw new Error('offline'); }); assert.equal((await h.navigate('/')).status, 503);
});
test('logout clears shell and paper caches without deleting unrelated apps', async () => {
  const h = harness(); await h.dispatch('install', {}); await h.caches.open('other-app');
  h.setNetwork(async (req) => typeof req === 'string' ? new Response('asset') : documentResponse()); await h.navigate('/');
  await h.dispatch('message', {data: {type: 'LOGOUT'}}); assert.deepEqual(await h.caches.keys(), ['other-app']);
  h.setNetwork(async () => { throw new Error('offline'); }); assert.equal((await h.navigate('/')).status, 503);
});

test('republishing a date prunes images no longer in the edition', async () => {
  const h = harness();
  h.setNetwork(async (req) => typeof req === 'string' ? new Response('jpeg', {headers: {'Content-Type': 'image/jpeg'}}) : documentResponse()); await h.navigate('/');
  const old = '/img/' + sha + '.jpg';
  assert.ok(await (await h.caches.open('nd-paper-v1')).match(old));
  h.setNetwork(async (req) => typeof req === 'string' ? new Response('asset') : new Response(html().replace(/<img[^>]+>/, ''))); await h.navigate('/');
  assert.equal(await (await h.caches.open('nd-paper-v1')).match(old), undefined);
});
