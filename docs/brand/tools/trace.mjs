// Usage: node trace.mjs SRC_DIR OUT_DIR
import sharp from 'sharp';
import potrace from 'potrace';
import { writeFileSync } from 'node:fs';
const [src, out] = process.argv.slice(2);
const INK = '#171719', CREAM = '#EFE4D5', RED = '#C54E30';

async function raw(file) {
  const { data, info } = await sharp(file).removeAlpha().raw().toBuffer({ resolveWithObject: true });
  return { data, w: info.width, h: info.height, px: (x, y) => { const i = (y * info.width + x) * 3; return [data[i], data[i + 1], data[i + 2]]; } };
}
// Build a black-on-white PNG buffer of the pixels matching pred.
async function mask(img, pred) {
  const buf = Buffer.alloc(img.w * img.h);
  for (let y = 0; y < img.h; y++) for (let x = 0; x < img.w; x++) buf[y * img.w + x] = pred(img.px(x, y), x, y) ? 0 : 255;
  return sharp(buf, { raw: { width: img.w, height: img.h, channels: 1 } }).png().toBuffer();
}
function trace(png) {
  return new Promise((ok, bad) => potrace.trace(png, { turdSize: 20, optTolerance: 0.4, alphaMax: 1.0, threshold: 128 }, (e, svg) => e ? bad(e) : ok(svg.match(/ d="([^"]+)"/)[1])));
}
// The rule is a parallelogram: find its left/right extent on its top and bottom rows.
function rule(img, pred) {
  let top = -1, bot = -1;
  for (let y = 0; y < img.h; y++) for (let x = 0; x < img.w; x++) if (pred(img.px(x, y), x, y)) { if (top < 0) top = y; bot = y; break; }
  const ext = y => { let l = -1, r = -1; for (let x = 0; x < img.w; x++) if (pred(img.px(x, y), x, y)) { if (l < 0) l = x; r = x; } return [l, r]; };
  const t = ext(top + 3), b = ext(bot - 3);
  return { top, bot, poly: `${t[0]},${top} ${t[1]},${top} ${b[1]},${bot} ${b[0]},${bot}` };
}
function bbox(img, pred) {
  let x0 = 1e9, y0 = 1e9, x1 = -1, y1 = -1;
  for (let y = 0; y < img.h; y++) for (let x = 0; x < img.w; x++) if (pred(img.px(x, y), x, y)) { x0 = Math.min(x0, x); y0 = Math.min(y0, y); x1 = Math.max(x1, x); y1 = Math.max(y1, y); }
  return { x0, y0, x1, y1 };
}

async function art(file, letterPred, rulePred) {
  const img = await raw(file);
  const d = await trace(await mask(img, letterPred));
  const r = rule(img, rulePred);
  const a = bbox(img, letterPred), b = bbox(img, rulePred);
  return { d, r, box: { x0: Math.min(a.x0, b.x0), y0: Math.min(a.y0, b.y0), x1: Math.max(a.x1, b.x1), y1: Math.max(a.y1, b.y1) } };
}
function svg({ d, r, box }, { fg, rule: rc, bg, pad = 0, square = false, radius = 0, title }) {
  let w = box.x1 - box.x0 + 1, h = box.y1 - box.y0 + 1, x = box.x0, y = box.y0;
  if (square) { const s = Math.max(w, h); x -= (s - w) / 2; y -= (s - h) / 2; w = h = s; }
  x -= pad; y -= pad; w += 2 * pad; h += 2 * pad;
  const back = bg ? `<rect x="${x}" y="${y}" width="${w}" height="${h}"${radius ? ` rx="${radius * w}"` : ''} fill="${bg}"/>` : '';
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="${x} ${y} ${w} ${h}" role="img" aria-label="${title}"><title>${title}</title>${back}<path fill="${fg}" fill-rule="evenodd" d="${d}"/><polygon fill="${rc}" points="${r.poly}"/></svg>\n`;
}

const dark = ([r, g, b]) => r < 110 && g < 110 && b < 110;
const red = ([r, g, b]) => r > 140 && g < 120 && b < 100;
// ND from the single-colour render (black on white): letters above the rule.
const mono = await raw(`${src}/03-nd-mark-mono.png`);
const ruleTop = rule(mono, (p, x, y) => dark(p) && y > 870).top;
const nd = await art(`${src}/03-nd-mark-mono.png`, (p, x, y) => dark(p) && y < ruleTop - 5, (p, x, y) => dark(p) && y >= ruleTop - 5);
const small = await art(`${src}/04-nd-mark-small.png`, ([r, g, b], x, y) => r > 150 && g > 150 && b > 140, red);
const word = await art(`${src}/06-wordmark-light.png`, dark, red);

const W = (n, s) => writeFileSync(`${out}/${n}`, s);
W('nd-mark.svg', svg(nd, { fg: CREAM, rule: RED, title: 'Night Desk' }));
W('nd-mark-ink.svg', svg(nd, { fg: INK, rule: RED, title: 'Night Desk' }));
W('nd-mark-mono.svg', svg(nd, { fg: '#000', rule: '#000', title: 'Night Desk' }));
W('nd-mark-small.svg', svg(small, { fg: CREAM, rule: RED, title: 'Night Desk' }));
W('wordmark-dark.svg', svg(word, { fg: CREAM, rule: RED, pad: 24, title: 'Night Desk' }));
W('wordmark-light.svg', svg(word, { fg: INK, rule: RED, pad: 24, title: 'Night Desk' }));
// Square tiles: the app icon master and the favicon.
const tile = (a, frac) => { const w = a.box.x1 - a.box.x0 + 1; return (w / frac - w) / 2; };
W('icon.svg', svg(nd, { fg: CREAM, rule: RED, bg: INK, square: true, pad: tile(nd, 0.72), title: 'Night Desk' }));
W('icon-maskable.svg', svg(nd, { fg: CREAM, rule: RED, bg: INK, square: true, pad: tile(nd, 0.56), title: 'Night Desk' }));
W('favicon.svg', svg(small, { fg: CREAM, rule: RED, bg: INK, square: true, pad: tile(small, 0.86), radius: 0.18, title: 'Night Desk' }));
W('mask-icon.svg', svg(small, { fg: '#000', rule: '#000', square: true, title: 'Night Desk' }));
console.log(JSON.stringify({ nd: nd.box, small: small.box, word: word.box, rules: [nd.r.poly, small.r.poly, word.r.poly] }));
