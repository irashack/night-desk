// Usage: node spots.mjs HERO.png OUT_DIR
// Cuts the lamp, mug and newspaper out of the hero illustration as cream line
// art over transparency (SVG), and draws a matching moon.
import sharp from 'sharp';
import potrace from 'potrace';
import { writeFileSync } from 'node:fs';
const [hero, out] = process.argv.slice(2);
const CREAM = '#EFE4D5', RED = '#C54E30';
const { data, info } = await sharp(hero).removeAlpha().raw().toBuffer({ resolveWithObject: true });
const W = info.width, H = info.height;
const ink = new Uint8Array(W * H);
for (let i = 0; i < W * H; i++) { const r = data[i * 3], g = data[i * 3 + 1]; ink[i] = r > 190 && g > 168 ? 1 : 0; }
// Desk line rows (473-485) belong to no object.
const deskTop = 472;
// Label connected components above the desk line.
const label = new Int32Array(W * H);
let n = 0; const boxes = [];
for (let i = 0; i < W * deskTop; i++) {
  if (!ink[i] || label[i]) continue;
  n++; const st = [i]; label[i] = n; let x0 = 1e9, y0 = 1e9, x1 = -1, y1 = -1, area = 0;
  while (st.length) {
    const j = st.pop(), x = j % W, y = (j / W) | 0; area++;
    x0 = Math.min(x0, x); x1 = Math.max(x1, x); y0 = Math.min(y0, y); y1 = Math.max(y1, y);
    for (const k of [j - 1, j + 1, j - W, j + W]) if (k >= 0 && k < W * deskTop && ink[k] && !label[k] && Math.abs((k % W) - x) <= 1) { label[k] = n; st.push(k); }
  }
  boxes[n] = { x0, y0, x1, y1, area };
}
const big = boxes.map((b, i) => ({ ...b, i })).filter(b => b && b.area > 300);
console.log(big.map(b => `${b.i}: ${b.x1 - b.x0 + 1}x${b.y1 - b.y0 + 1}+${b.x0}+${b.y0} a=${b.area}`).join('\n'));
const lamp = big.find(b => b.x1 - b.x0 > 400);
const parts = { lamp: [lamp.i], mug: big.filter(b => b !== lamp && b.x1 < 840).map(b => b.i), paper: big.filter(b => b !== lamp && b.x0 >= 840).map(b => b.i) };

async function traceLabels(ids, pad) {
  let x0 = 1e9, y0 = 1e9, x1 = -1, y1 = -1;
  for (const id of ids) { const b = boxes[id]; x0 = Math.min(x0, b.x0); y0 = Math.min(y0, b.y0); x1 = Math.max(x1, b.x1); y1 = Math.max(y1, b.y1); }
  const set = new Set(ids), buf = Buffer.alloc(W * H, 255);
  for (let i = 0; i < W * deskTop; i++) if (set.has(label[i])) buf[i] = 0;
  const png = await sharp(buf, { raw: { width: W, height: H, channels: 1 } }).png().toBuffer();
  const d = await new Promise((ok, bad) => potrace.trace(png, { turdSize: 10, optTolerance: 0.3, threshold: 128 }, (e, s) => e ? bad(e) : ok(s.match(/ d="([^"]+)"/)[1])));
  return { d, box: { x0: x0 - pad, y0: y0 - pad, x1: x1 + pad, y1: y1 + pad } };
}
function square({ x0, y0, x1, y1 }) { const w = x1 - x0, h = y1 - y0, s = Math.max(w, h); return [x0 - (s - w) / 2, y0 - (s - h) / 2, s, s]; }
function svg(title, vb, body) {
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="${vb.map(v => +v.toFixed(1)).join(' ')}" role="img" aria-label="${title}"><title>${title}</title>${body}</svg>\n`;
}
const file = (name, s) => writeFileSync(`${out}/${name}`, s);

// Lamp: its glow is the one gradient, a soft cone under the shade.
{
  const t = await traceLabels(parts.lamp, 40);
  const b = t.box; b.y1 = deskTop + 40;
  const vb = square(b);
  file('spot-lamp.svg', svg('Desk lamp', vb,
    `<defs><linearGradient id="glow" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#F0A04A" stop-opacity=".8"/><stop offset=".55" stop-color="#D9853A" stop-opacity=".25"/><stop offset="1" stop-color="#D9853A" stop-opacity="0"/></linearGradient>` +
    `<filter id="soft" x="-50%" y="-50%" width="200%" height="200%"><feGaussianBlur stdDeviation="8"/></filter></defs>` +
    `<path fill="url(#glow)" filter="url(#soft)" d="M812 238 L1008 232 L1075 468 L765 468 Z"/>` +
    `<path fill="${CREAM}" fill-rule="evenodd" d="${t.d}"/>` +
    `<rect x="${t.box.x0 + 10}" y="${deskTop + 1}" width="${t.box.x1 - t.box.x0 - 20}" height="12" rx="6" fill="${CREAM}"/>`));
}
for (const [name, title] of [['mug', 'Coffee mug'], ['paper', 'Folded newspaper']]) {
  const t = await traceLabels(parts[name], 30);
  // Each object stands on a short piece of the desk line.
  const line = `<rect x="${t.box.x0 + 10}" y="${deskTop + 1}" width="${t.box.x1 - t.box.x0 - 20}" height="12" rx="6" fill="${CREAM}"/>`;
  const extra = name === 'paper' ? '' : '';
  t.box.y1 = Math.max(t.box.y1, deskTop + 40);
  file(`spot-${name}.svg`, svg(title, square(t.box), `<path fill="${CREAM}" fill-rule="evenodd" d="${t.d}"/>${line}${extra}`));
}
// Moon: drawn to match, a crescent and three small stars over the desk line.
file('spot-moon.svg', svg('Crescent moon', [0, 0, 512, 512],
  `<path fill="${CREAM}" d="M282 130a120 120 0 1 0 98 196A100 100 0 1 1 282 130z"/>` +
  [[130, 150, 8], [400, 170, 6], [170, 330, 5]].map(([x, y, r]) => `<path fill="${CREAM}" d="M${x} ${y - r * 2}L${x + r * 0.5} ${y - r * 0.5}L${x + r * 2} ${y}L${x + r * 0.5} ${y + r * 0.5}L${x} ${y + r * 2}L${x - r * 0.5} ${y + r * 0.5}L${x - r * 2} ${y}L${x - r * 0.5} ${y - r * 0.5}Z"/>`).join('') +
  `<rect x="96" y="410" width="320" height="12" rx="6" fill="${CREAM}"/>`));
