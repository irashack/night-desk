'use strict';
(() => {
  const $ = (s) => document.querySelector(s);
  const all = (s) => [...document.querySelectorAll(s)];
  const read = (key) => { try { return localStorage.getItem('nd-' + key); } catch (_) { return null; } };
  const remember = (key, value) => { try { localStorage.setItem('nd-' + key, value); } catch (_) {} };
  async function clearOffline() {
    if ('serviceWorker' in navigator) {
      navigator.serviceWorker.controller?.postMessage({type: 'LOGOUT'});
      const registrations = await navigator.serviceWorker.getRegistrations();
      await Promise.all(registrations.map((r) => r.unregister()));
    }
    if ('caches' in window) await Promise.all((await caches.keys()).filter((k) => k.startsWith('nd-')).map((k) => caches.delete(k)));
  }
  if (document.body.dataset.logout) { clearOffline().catch(() => {}); return; }
  $('.logout')?.addEventListener('submit', async (e) => {
    e.preventDefault();
    try { await clearOffline(); } catch (_) {}
    e.target.submit();
  });
  document.documentElement.classList.add('enhanced');
  let mode = read('mode');
  if (!['auto', 'light', 'sepia', 'dark'].includes(mode)) mode = 'auto';
  const setMode = (value) => {
    mode = value; document.documentElement.dataset.mode = mode;
    all('[data-mode-choice]').forEach((b) => b.setAttribute('aria-pressed', String(b.dataset.modeChoice === mode)));
  };
  all('[data-mode-choice]').forEach((b) => b.addEventListener('click', () => { setMode(b.dataset.modeChoice); remember('mode', mode); }));
  setMode(mode);
  const book = $('#book'), pages = all('.page'), reduced = matchMedia('(prefers-reduced-motion: reduce)');
  let layout = read('layout'), cur = 0, busy = false;
  if (!['pages', 'list', 'reader'].includes(layout)) layout = 'pages';
  const bars = pages.map((p) => p.querySelector('.turnbar'));
  // The server leaves a working link bar at the foot of each page. Enhancement
  // collects those same links into one persistent bar for the current page.
  bars.forEach((bar) => $('#controls').append(bar));
  if (book) document.body.classList.add('has-book');
  const sync = () => {
    document.documentElement.dataset.layout = layout;
    all('[data-layout]').forEach((b) => b.setAttribute('aria-pressed', String(b.dataset.layout === layout)));
    if (!book) return;
    document.documentElement.style.setProperty('--progress', String((cur + 1) / pages.length));
    book.classList.toggle('list', layout !== 'pages');
    book.classList.toggle('reading', layout === 'reader');
    pages.forEach((p, i) => {
      p.inert = layout === 'pages' && i !== cur;
      p.setAttribute('aria-hidden', String(p.inert));
      p.classList.toggle('is-current', i === cur);
      bars[i].hidden = i !== cur;
    });
  };
  // In the scrolling layouts the bar follows the page being read.
  let following = false;
  addEventListener('scroll', () => {
    if (!book || layout === 'pages' || following) return;
    following = true;
    requestAnimationFrame(() => {
      following = false;
      const end = innerHeight + scrollY >= document.documentElement.scrollHeight - 2;
      const at = end ? pages.length - 1 : pages.findIndex((p) => p.getBoundingClientRect().bottom > innerHeight * 0.3);
      if (at >= 0 && at !== cur) { cur = at; sync(); }
    });
  }, {passive: true});
  const hashTarget = (hash) => {
    try { return document.getElementById(decodeURIComponent(hash.slice(1))); } catch (_) { return null; }
  };
  function go(to, hash = '', record = false) {
    if (!book || to < 0 || to >= pages.length || busy) return;
    const focus = () => {
      if (record && hash && location.hash !== hash) history.pushState(null, '', hash);
      pages[to].focus({preventScroll: true});
      const target = hashTarget(hash);
      if (target === pages[to]) target.scrollTop = 0;
      else if (target) target.scrollIntoView({block: 'start'});
    };
    if (to === cur) { focus(); return; }
    busy = true;
    const from = pages[cur], next = pages[to], forward = to > cur;
    next.scrollTop = 0;
    const moving = forward ? from : next, staying = forward ? next : from;
    staying.classList.add('is-under'); moving.classList.remove('is-current');
    moving.classList.add(forward ? 'turn-fwd' : 'turn-back');
    let done = false;
    const finish = () => {
      if (done) return; done = true;
      pages.forEach((p) => p.classList.remove('turn-fwd', 'turn-back', 'is-under'));
      cur = to; busy = false; sync(); focus();
    };
    if (reduced.matches) finish();
    else { moving.addEventListener('animationend', finish, {once: true}); setTimeout(finish, 900); }
  }
  all('[data-layout]').forEach((b) => b.addEventListener('click', () => {
    if (busy) return;
    layout = b.dataset.layout; remember('layout', layout); sync(); setMenu(false);
    if (book) {
      if (layout === 'pages') window.scrollTo({top: 0, behavior: 'instant'});
      else pages[cur].scrollIntoView({block: 'start'});
    }
  }));
  document.addEventListener('click', (e) => {
    const link = e.target.closest('a[href^="#"]');
    if (!link || !book || layout !== 'pages' || e.button !== 0 || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
    const target = hashTarget(link.hash), to = pages.indexOf(target?.closest('.page'));
    if (to >= 0) { e.preventDefault(); go(to, link.hash, true); }
  });
  window.addEventListener('hashchange', () => {
    const to = pages.indexOf(hashTarget(location.hash)?.closest('.page'));
    if (to >= 0 && layout === 'pages') go(to, location.hash);
  });
  document.addEventListener('keydown', (e) => {
    if (e.altKey || e.ctrlKey || e.metaKey || e.target.closest('input,textarea,select,[contenteditable]') || layout !== 'pages' || !book) return;
    if (e.key === 'ArrowRight' || e.key === 'ArrowLeft') {
      e.preventDefault(); const to = cur + (e.key === 'ArrowRight' ? 1 : -1);
      go(to, pages[to] ? '#' + pages[to].id : '', true);
    }
  });
  let pointer = null;
  book?.addEventListener('pointerdown', (e) => { if (e.isPrimary && !e.target.closest('a,button,input')) pointer = {id: e.pointerId, x: e.clientX, y: e.clientY}; });
  book?.addEventListener('pointercancel', () => { pointer = null; });
  book?.addEventListener('pointerup', (e) => {
    if (!pointer || pointer.id !== e.pointerId) return;
    const dx = e.clientX - pointer.x, dy = e.clientY - pointer.y; pointer = null;
    if (layout === 'pages' && Math.abs(dx) > 50 && Math.abs(dx) > Math.abs(dy) * 1.3) {
      const to = cur + (dx < 0 ? 1 : -1); go(to, pages[to] ? '#' + pages[to].id : '', true);
    }
  });
  // The page bar and the menu stay put; the menu holds everything else, so
  // nothing has to hide while reading.
  const menu = $('#menu'), toggle = $('.menu-toggle');
  // The panel lives in the page bar, so it always opens just above the bar.
  if (book && toggle) $('#controls').append(toggle, $('#chrome'));
  const setMenu = (open) => {
    if (!toggle) return;
    document.documentElement.classList.toggle('menu-open', open);
    toggle.setAttribute('aria-expanded', String(open));
  };
  const menuOpen = () => toggle?.getAttribute('aria-expanded') === 'true';
  toggle?.addEventListener('click', () => setMenu(!menuOpen()));
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && menuOpen()) { setMenu(false); toggle.focus(); } });
  document.addEventListener('click', (e) => { if (menuOpen() && !e.target.closest('.chrome,.menu-toggle')) setMenu(false); });
  // The bar's title opens the paper's contents, Front page first; without
  // script it stays a link to the front page.
  const where = all('#controls .where'), contents = document.createElement('nav');
  const contentsOpen = () => !contents.hidden;
  const setContents = (open) => {
    contents.hidden = !open;
    where.forEach((w) => w.setAttribute('aria-expanded', String(open)));
    if (!open) return;
    setMenu(false);
    contents.querySelectorAll('a').forEach((a, i) => a.toggleAttribute('aria-current', i === cur));
  };
  if (book && where.length) {
    contents.className = 'contents'; contents.id = 'contents'; contents.hidden = true;
    contents.setAttribute('aria-label', 'Contents');
    pages.forEach((p, i) => {
      const a = document.createElement('a'), title = document.createElement('span');
      a.href = '#' + p.id; title.textContent = (i ? '' : '⌂ ') + p.dataset.title;
      a.append(title);
      if (i) { const pg = document.createElement('span'); pg.className = 'pg'; pg.textContent = 'p.' + (i + 1); a.append(pg); } else a.className = 'home';
      contents.append(a);
    });
    $('#controls').append(contents);
    where.forEach((w) => {
      w.setAttribute('aria-controls', 'contents'); w.setAttribute('aria-expanded', 'false');
      w.setAttribute('aria-label', w.getAttribute('aria-label').replace(/^Front page/, 'Contents'));
      w.addEventListener('click', (e) => {
        if (e.button !== 0 || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
        e.preventDefault(); e.stopPropagation(); setContents(!contentsOpen());
      });
    });
    contents.addEventListener('click', (e) => { if (e.target.closest('a')) setContents(false); });
    toggle?.addEventListener('click', () => setContents(false));
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && contentsOpen()) { setContents(false); where.find((w) => !w.closest('.turnbar').hidden)?.focus(); }
    });
    document.addEventListener('click', (e) => { if (contentsOpen() && !e.target.closest('.contents')) setContents(false); });
  }
  const controls = $('#controls');
  const measure = () => document.documentElement.style.setProperty('--controls-h', (controls?.offsetHeight || 0) + 'px');
  if (book) {
    measure();
    if ('ResizeObserver' in window) new ResizeObserver(measure).observe(controls);
  }
  document.addEventListener('scroll', () => {
    if (!book || busy || layout === 'pages') return;
    let visible = 0;
    pages.forEach((p, i) => { if (p.getBoundingClientRect().top <= window.innerHeight * 0.4) visible = i; });
    if (visible !== cur) { cur = visible; sync(); }
  }, {passive: true});
  if (book) {
    const target = hashTarget(location.hash), hashPage = pages.indexOf(target?.closest('.page'));
    if (hashPage >= 0) cur = hashPage;
    sync();
    if (target) target.scrollIntoView({block: 'start'});
  } else sync();
  const status = (message) => { if ($('#status')) $('#status').textContent = message; };
  document.addEventListener('click', async (e) => {
    const button = e.target.closest('.actions button'); if (!button || button.disabled) return;
    const row = button.closest('.actions'), kind = button.dataset.kind;
    const wasOn = button.getAttribute('aria-pressed') === 'true';
    if (kind !== 'save' && wasOn) return;
    const related = all('.actions').filter((r) => r.dataset.item === row.dataset.item && r.dataset.date === row.dataset.date);
    related.forEach((r) => r.querySelectorAll('button').forEach((b) => { b.disabled = true; }));
    try {
      const body = new URLSearchParams({date: row.dataset.date, item: row.dataset.item, kind: kind === 'save' && wasOn ? 'unsave' : kind, csrf: $('meta[name=csrf]').content});
      const res = await fetch('/feedback', {method: 'POST', body, credentials: 'same-origin'});
      if (res.status === 401) { await clearOffline(); location.assign('/'); return; }
      if (res.status !== 204) throw new Error('Feedback not recorded');
      related.forEach((r) => r.querySelectorAll('button').forEach((b) => {
        if (kind !== 'save' && b.dataset.kind !== 'save') b.setAttribute('aria-pressed', String(b.dataset.kind === kind));
        if (kind === 'save' && b.dataset.kind === 'save') b.setAttribute('aria-pressed', String(!wasOn));
      }));
      status(kind === 'save' ? (wasOn ? 'Removed from saved.' : 'Saved.') : 'Feedback recorded.');
    } catch (_) { status('Could not record feedback. Please try again when online.'); }
    finally { related.forEach((r) => r.querySelectorAll('button').forEach((b) => { b.disabled = false; })); }
  });
  // An installed app or a tab left open overnight resumes the page it already
  // shows without navigating. On return, reload the front page if a newer
  // edition exists, and say so when only the saved copy can be shown.
  const shown = $('main.reader')?.dataset.date;
  if (shown && location.pathname === '/') {
    const offline = () => status('Cannot reach the paper: this is the saved ' + shown + ' edition.');
    let checked = Date.now();
    const check = async (force) => {
      if (document.visibilityState !== 'visible' || (!force && Date.now() - checked < 5 * 60 * 1000)) return;
      checked = Date.now();
      try {
        const res = await fetch('/', {cache: 'no-store', credentials: 'same-origin'});
        if (res.redirected || res.status === 401) { location.reload(); return; }
        const date = (await res.text()).match(/data-date="(\d{4}-\d{2}-\d{2})"/)?.[1];
        if (date && date !== shown) location.reload();
      } catch (_) { offline(); }
    };
    if (!navigator.onLine) offline();
    document.addEventListener('visibilitychange', () => check(false));
    window.addEventListener('pageshow', (e) => { if (e.persisted) check(true); });
    window.addEventListener('online', () => check(true));
  }
  // Notifications are opt-in per device; permission is asked only on a click.
  // Without Push (iOS outside the home-screen app) the toggle stays hidden.
  const bell = $('[data-push]');
  if (bell && 'PushManager' in window && 'Notification' in window) {
    const manager = async () => (await navigator.serviceWorker.ready).pushManager;
    const show = (on) => bell.setAttribute('aria-checked', String(!!on));
    bell.parentNode.hidden = false;
    manager().then((m) => m.getSubscription()).then(show, () => {});
    bell.addEventListener('click', async () => {
      let sub;
      try {
        const m = await manager(), old = await m.getSubscription();
        if (!old && await Notification.requestPermission() !== 'granted') return status('Notifications are blocked for this site.');
        sub = old || await m.subscribe({userVisibleOnly: true, applicationServerKey: Uint8Array.from(atob(bell.dataset.push.replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0))});
        const res = await fetch('/push/' + (old ? 'unsubscribe' : 'subscribe'), {method: 'POST', body: new URLSearchParams({subscription: JSON.stringify(sub), csrf: $('meta[name=csrf]').content}), credentials: 'same-origin'});
        if (res.status !== 204) throw new Error('Not changed');
        if (old) await old.unsubscribe();
        show(!old);
      } catch (_) { if (sub && bell.getAttribute('aria-checked') !== 'true') sub.unsubscribe(); status('Could not change notifications.'); }
    });
  }
  if ('serviceWorker' in navigator && $('meta[name=offline-owner]')?.content) {
    navigator.serviceWorker.register('/sw.js').then(async () => {
      // On the first visit the navigation preceded installation; save it once controlled.
      const registration = await navigator.serviceWorker.ready;
      const refresh = () => (navigator.serviceWorker.controller || registration.active)?.postMessage({type: 'CACHE_LATEST'});
      refresh(); window.addEventListener('online', refresh);
    }).catch(() => {});
  }
})();
