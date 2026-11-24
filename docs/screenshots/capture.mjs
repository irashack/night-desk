// Regenerates the README screenshots from the fictional demo paper.
//
//   node docs/screenshots/capture.mjs            # starts the demo reader itself
//   node docs/screenshots/capture.mjs http://127.0.0.1:8080   # uses a running one
//
// Needs Go and Playwright with Chromium (`npm i -g playwright` and
// `npx playwright install chromium`, or any setup where require('playwright')
// resolves). Images are written next to this script.
import { execFileSync, spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { createRequire } from 'node:module';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const require = createRequire(import.meta.url);
const { chromium, devices } = require('playwright');

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, '..', '..');

// startDemo builds and runs the demo reader on a free loopback port and
// resolves with its origin once the editions are published. It builds first,
// rather than using go run, so that stopping it stops the server itself.
function startDemo() {
  const dir = mkdtempSync(join(tmpdir(), 'nightdesk-demo-bin-'));
  const bin = join(dir, 'demo');
  execFileSync('go', ['build', '-o', bin, './docs/screenshots/demo'], { cwd: root, stdio: 'inherit' });
  const child = spawn(bin, ['-addr', '127.0.0.1:0'], { stdio: ['ignore', 'pipe', 'inherit'] });
  const stop = () => { child.kill('SIGINT'); rmSync(dir, { recursive: true, force: true }); };
  return new Promise((ok, fail) => {
    let out = '';
    child.on('exit', (code) => fail(new Error(`demo exited with ${code}`)));
    child.stdout.on('data', (chunk) => {
      out += chunk;
      const m = out.match(/(http:\/\/127\.0\.0\.1:\d+)\/login/);
      if (m) ok({ origin: m[1], stop });
    });
  });
}

const shots = [
  { file: 'desktop-pages-light.png', device: 'desktop', mode: 'light', layout: 'pages', path: '/' },
  { file: 'desktop-pages-sepia.png', device: 'desktop', mode: 'sepia', layout: 'pages', path: '/' },
  { file: 'desktop-pages-dark.png', device: 'desktop', mode: 'dark', layout: 'pages', path: '/' },
  // The scrolling layouts are shown part-way down, where they differ from Pages.
  { file: 'desktop-list.png', device: 'desktop', mode: 'light', layout: 'list', path: '/', scroll: '#section-world', offset: 360 },
  { file: 'desktop-reader.png', device: 'desktop', mode: 'light', layout: 'reader', path: '/', scroll: '#section-world', offset: 80 },
  { file: 'desktop-search.png', device: 'desktop', mode: 'light', layout: 'pages', path: '/search?q=bridge' },
  { file: 'phone-front-dark.png', device: 'phone', mode: 'dark', layout: 'pages', path: '/' },
  { file: 'phone-article.png', device: 'phone', mode: 'light', layout: 'list', path: '/', scroll: '#item-northgate-reopens', offset: 16 },
  { file: 'phone-story.png', device: 'phone', mode: 'sepia', layout: 'pages', path: '/read/2026-11-19/northgate-reopens', scroll: '.story-text', offset: 420 },
];

const contexts = {
  desktop: { viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 },
  phone: { ...devices['iPhone 15'] },
};

const given = process.argv[2];
const demo = given ? { origin: given.replace(/\/+$/, '').replace(/\/login$/, ''), stop: () => {} } : await startDemo();
const browser = await chromium.launch();
try {
  for (const shot of shots) {
    const context = await browser.newContext({ ...contexts[shot.device], colorScheme: shot.mode === 'dark' ? 'dark' : 'light', reducedMotion: 'reduce', serviceWorkers: 'block' });
    await context.addInitScript(([mode, layout]) => {
      localStorage.setItem('nd-mode', mode);
      localStorage.setItem('nd-layout', layout);
    }, [shot.mode, shot.layout]);
    const page = await context.newPage();
    // The demo's identity provider signs in without a prompt and returns to /.
    await page.goto(demo.origin + '/login');
    await page.waitForURL(demo.origin + '/');
    await page.goto(demo.origin + shot.path);
    await page.evaluate(() => document.fonts.ready);
    await page.waitForLoadState('networkidle');
    if (shot.scroll) {
      await page.evaluate(([selector, offset]) => {
        const top = document.querySelector(selector).getBoundingClientRect().top + window.scrollY;
        window.scrollTo({ top: top - offset, behavior: 'instant' });
      }, [shot.scroll, shot.offset ?? 0]);
    }
    await page.waitForTimeout(500);
    await page.screenshot({ path: join(here, shot.file) });
    console.log('wrote', shot.file);
    await context.close();
  }
} finally {
  await browser.close();
  demo.stop();
}
