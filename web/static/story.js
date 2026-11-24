'use strict';
// The reading view's comforts, kept per device: text size, and the place in
// each story, so leaving and coming back resumes where reading stopped.
(() => {
  const main = document.querySelector('main.story');
  if (!main) return;
  const read = (key) => { try { return localStorage.getItem('nd-' + key); } catch (_) { return null; } };
  const remember = (key, value) => { try { localStorage.setItem('nd-' + key, value); } catch (_) {} };
  const sizes = [16, 18, 20, 22, 25, 28, 32];
  // Until the reader picks a size, the stylesheet's default for the screen
  // applies (smaller on phones).
  const chosen = sizes.indexOf(Number(read('story-size')));
  const fallback = parseInt(getComputedStyle(document.body).getPropertyValue('--story-size'), 10) || 20;
  let size = chosen >= 0 ? chosen : Math.max(0, sizes.findIndex((s) => s >= fallback)), picked = chosen >= 0;
  const buttons = [...document.querySelectorAll('[data-size]')];
  const apply = () => {
    if (picked) document.body.style.setProperty('--story-size', sizes[size] + 'px');
    buttons.forEach((b) => { b.disabled = b.dataset.size === '-1' ? size === 0 : size === sizes.length - 1; });
  };
  buttons.forEach((b) => b.addEventListener('click', () => {
    // Keep the paragraph being read in view across the size change.
    const at = progress();
    size = Math.max(0, Math.min(sizes.length - 1, size + Number(b.dataset.size))); picked = true;
    remember('story-size', String(sizes[size])); apply(); restore(at);
  }));
  apply();
  // Positions are fractions of the story's height, so a size change or a
  // rotated phone lands near the same words. Only the newest 100 are kept.
  const key = main.dataset.story;
  const load = () => { try { return JSON.parse(read('story-places') || '{}') || {}; } catch (_) { return {}; } };
  const progress = () => {
    const max = document.documentElement.scrollHeight - innerHeight;
    return max > 0 ? Math.min(1, Math.max(0, scrollY / max)) : 0;
  };
  const restore = (at) => {
    const max = document.documentElement.scrollHeight - innerHeight;
    if (max > 0) window.scrollTo({top: at * max, behavior: 'instant'});
  };
  const save = () => {
    const places = load(), at = progress();
    delete places[key];
    if (at > 0.02 && at < 0.98) places[key] = Math.round(at * 1000) / 1000;
    const keys = Object.keys(places);
    keys.slice(0, Math.max(0, keys.length - 100)).forEach((k) => delete places[k]);
    remember('story-places', JSON.stringify(places));
  };
  const saved = load()[key];
  if (typeof saved === 'number' && !location.hash) {
    // The paper's fonts change the story's height, so wait for them.
    (document.fonts ? document.fonts.ready : Promise.resolve()).then(() => {
      restore(saved);
      const status = document.getElementById('status');
      if (status && scrollY > 0) status.textContent = 'Back where you left off.';
    });
  }
  let timer = 0;
  addEventListener('scroll', () => { clearTimeout(timer); timer = setTimeout(save, 400); }, {passive: true});
  addEventListener('pagehide', save);
})();
