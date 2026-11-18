// node gen.mjs [clip ...]  — writes the website's feature clips as HyperFrames projects under $FILM_ROOT/films/.
// $FILM_ROOT holds assets/rec (takes.mjs output plus lengths.json) and assets/fonts. The launch film and the README
// loop come from split.mjs instead.
// Every shot plays a real recording of the Ghosttree interface (assets/rec, REC device pixels at 2x)
// and frames it with a camera given in recording pixels. Mark names come from assets/rec/<take>.marks.json,
// which the recorder writes next to each take, so cuts and highlights follow the real clicks.
import fs from 'node:fs';
import path from 'node:path';

const ROOT = process.env.FILM_ROOT || path.dirname(new URL(import.meta.url).pathname);
// Recording size in device pixels: 2560x1600 for the film takes, REC=2000x1250 for the website clips (1000x625 at 2x).
const [REC_W, REC_H] = (process.env.REC || '2560x1600').split('x').map(Number);
const BLUE = '#2345c7', YELLOW = '#eed46c';
const marks = {};
const recLen = {};
for (const f of fs.readdirSync(`${ROOT}/assets/rec`)) {
  if (f.endsWith('.marks.json')) marks[f.replace('.marks.json', '')] = JSON.parse(fs.readFileSync(`${ROOT}/assets/rec/${f}`, 'utf8'));
}
for (const [k, v] of Object.entries(JSON.parse(fs.readFileSync(`${ROOT}/assets/rec/lengths.json`, 'utf8')))) recLen[k] = v;

// mark('payoff', 'click') -> time of the first mark with that name; 'click#2' -> the second.
function mark(rec, spec) {
  const [name, nth = '1'] = spec.split('#');
  const all = marks[rec].filter(m => m.name === name);
  const m = all[Number(nth) - 1];
  if (!m) throw new Error(`no mark ${spec} in ${rec}`);
  return m;
}
const r2 = (x) => Math.round(x * 1000) / 1000;

const FORMATS = {
  clip: { W: 1600, H: 1000, win: { x: 0, y: 0, w: 1600, h: 1000 }, title: null, vw: 1, radius: 0 },
};

// Camera keyframe {t, c:[x,y] or box, w} -> transform of the recording layer inside the window.
function camState(fmt, kf) {
  const win = fmt.win;
  const aspect = win.w / win.h;
  let vw = Math.min(kf.w * fmt.vw, REC_W);
  let vh = vw / aspect;
  if (vh > REC_H) { vh = REC_H; vw = vh * aspect; }
  const [cx, cy] = kf.c.length === 2 ? kf.c : [kf.c[0] + kf.c[2] / 2, kf.c[1] + kf.c[3] / 2];
  const left = kf.l;
  let x = Math.max(0, Math.min(REC_W - vw, left != null ? left : cx - vw / 2));
  let y = Math.max(0, Math.min(REC_H - vh, cy - vh / 2));
  if (kf.top) y = Math.max(0, Math.min(REC_H - vh, kf.top));
  const s = win.w / vw;
  return { x: r2(-x * s), y: r2(-y * s), scale: r2(s) };
}

const fontFaces = `
@font-face { font-family: 'Weight'; src: url('assets/fonts/fira-condensed-heavy.woff2') format('woff2'); font-weight: 900; }
@font-face { font-family: 'Plex'; src: url('assets/fonts/plex-regular.woff2') format('woff2'); font-weight: 400; }
@font-face { font-family: 'Plex'; src: url('assets/fonts/plex-medium.woff2') format('woff2'); font-weight: 500; }`;

const words = (text, accent) => text.split(' ').map(w => `<span class="w${accent && w.replace(/[.,]/g, '') === accent ? ' acc' : ''}">${w}</span>`).join(' ');

function titleTweens(sel, t, d) {
  return `tl.fromTo('${sel} .w', { opacity: 0, filter: 'blur(8px)' }, { opacity: 1, filter: 'blur(0px)', duration: 0.14, stagger: 0.07, ease: 'power2.out' }, ${r2(t)});
tl.to('${sel} .w', { opacity: 0, filter: 'blur(8px)', duration: 0.18, stagger: 0.03, ease: 'power2.in' }, ${r2(t + d - 0.24)});`;
}

// ---- a UI shot -----------------------------------------------------------------------------------------------
function uiShot(fmt, s, sceneId, opts = {}) {
  const from = typeof s.from === 'number' ? s.from : mark(s.rec, s.from.m).t + (s.from.off || 0);
  const at = (spec) => (typeof spec === 'number' ? spec : mark(s.rec, spec.m).t + (spec.off || 0) - from);
  if (from + s.dur > recLen[s.rec] + 0.01) throw new Error(`${sceneId}: ${s.rec} too short (${from}+${s.dur} > ${recLen[s.rec]})`);
  const win = fmt.win;
  const id = sceneId;
  const hl = (s.hl || []).map((h, i) => {
    const b = h.box || (() => { const m = mark(s.rec, h.m); return m.box.lines && h.line != null ? m.box.lines[h.line] : m.box; })();
    const pad = h.pad ?? 6;
    const x = b.x - pad + (h.dx || 0), y = b.y - pad + (h.dy || 0), w = (h.w || b.w) + pad * 2, hh = b.h + pad * 2;
    return { i, x, y, w, h: hh, t: at(h.t), d: h.d };
  });
  const titles = fmt.title && !opts.noTitles ? (s.titles || []) : [];
  const resolveC = (c) => (Array.isArray(c) ? c : (() => { const b = mark(s.rec, c.m).box; return [b.x, b.y, b.w, b.h]; })());
  const cams = (s.cam || []).map(k => ({ ...k, t: at(k.t), st: camState(fmt, { ...k, c: resolveC(k.c) }) }));
  const c0 = cams[0]?.st || camState(fmt, { c: [REC_W / 2, REC_H / 2], w: REC_W });
  const vidEnd = r2(s.dur);
  const enter = opts.enter ?? 'whip';
  const exit = opts.exit ?? 'whip';
  let js = `tl.set('#${id}-cam', { x: ${c0.x}, y: ${c0.y}, scale: ${c0.scale}, transformOrigin: '0 0' }, 0);\n`;
  for (const k of cams.slice(1)) js += `tl.to('#${id}-cam', { x: ${k.st.x}, y: ${k.st.y}, scale: ${k.st.scale}, duration: ${k.d ?? 0.8}, ease: '${k.ease || 'power2.inOut'}' }, ${r2(k.t)});\n`;
  if (enter === 'whip') js += `tl.fromTo('#${id}-win', { x: 160, opacity: 0, filter: 'blur(12px)' }, { x: 0, opacity: 1, filter: 'blur(0px)', duration: 0.42, ease: 'expo.out' }, 0);\n`;
  if (enter === 'rise') js += `tl.fromTo('#${id}-win', { y: ${Math.round(fmt.H * 0.5)} }, { y: 0, duration: 0.7, ease: 'expo.out' }, 0);\n`;
  if (exit === 'whip') js += `tl.to('#${id}-win', { x: -140, opacity: 0, filter: 'blur(10px)', duration: 0.25, ease: 'power3.in' }, ${r2(s.dur - 0.25)});\n`;
  for (const h of hl) {
    js += `tl.fromTo('#${id}-hl${h.i}', { scaleX: 0 }, { scaleX: 1, duration: 0.35, ease: 'power2.out' }, ${r2(h.t)});\n`;
    if (h.d) js += `tl.to('#${id}-hl${h.i}', { opacity: 0, duration: 0.2 }, ${r2(h.t + h.d)});\n`;
  }
  const titleHtml = titles.map((t, i) => {
    const d = r2(Math.min(t.d ?? s.dur - t.t, s.dur - t.t));
    return `<h2 id="${id}-t${i}" class="clip title" data-start="${r2(t.t)}" data-duration="${d}" data-track-index="${3 + i}">${words(t.text, t.accent)}</h2>`;
  }).join('\n    ');
  js += titles.map((t, i) => titleTweens(`#${id}-t${i}`, t.t, Math.min(t.d ?? s.dur - t.t, s.dur - t.t))).join('\n');
  const T = fmt.title;
  return `<template>
  <style>
    ${fontFaces}
    #root { position: absolute; inset: 0; background: ${s.ground || BLUE}; overflow: hidden; }
    #${id}-win { position: absolute; left: ${win.x}px; top: ${win.y}px; width: ${win.w}px; height: ${win.h}px; overflow: hidden;
      border-radius: ${win.open ? `${fmt.radius}px ${fmt.radius}px 0 0` : `${fmt.radius}px`}; background: #fff; box-shadow: ${fmt.radius ? '0 40px 90px rgba(6,18,70,.38)' : 'none'}; }
    #${id}-cam { position: absolute; left: 0; top: 0; width: ${REC_W}px; height: ${REC_H}px; }
    #${id}-cam video { position: absolute; left: 0; top: 0; width: ${REC_W}px; height: ${REC_H}px; }
    .hl { position: absolute; background: ${YELLOW}; mix-blend-mode: multiply; transform-origin: 0 50%; border-radius: 4px; }
    ${T ? `.title { position: absolute; left: ${T.x}px; top: ${T.y}px; width: ${T.w}px; margin: 0; font: 900 ${T.size}px/1.02 'Weight', sans-serif;
      letter-spacing: -0.02em; color: #fff; }
    .title .acc { color: ${YELLOW}; }` : ''}
  </style>
  <div id="root" data-composition-id="${id}" data-width="${fmt.W}" data-height="${fmt.H}">
    <div id="${id}-win" data-layout-allow-overflow><div id="${id}-cam">
      <video id="${id}-vid" class="clip" src="assets/rec/${s.rec}.mp4" muted playsinline data-start="0" data-duration="${vidEnd}" data-media-start="${r2(from)}" data-track-index="1"></video>
      ${hl.map(h => `<div id="${id}-hl${h.i}" class="hl" style="left:${h.x}px;top:${h.y}px;width:${h.w}px;height:${h.h}px"></div>`).join('\n      ')}
    </div></div>
    ${titleHtml}
  </div>
  <script>
    const tl = gsap.timeline({ paused: true });
    ${js.replace(/\n/g, '\n    ')}
    window.__timelines["${id}"] = tl;
  </script>
</template>
`;
}

// Website feature clips: silent loops of one real action each, no titles (the page section names it). They are
// recorded at the clip's aspect (REC=2000x1250: 1000x625 CSS px at 2x), so the camera holds the whole frame and
// never cuts a line; the pointer rests below what it points at.
const FULL = () => [{ t: 0, c: [REC_W / 2, REC_H / 2], w: REC_W }];
const CLIPS = {
  'clip-briefing': [{ id: 'c', rec: 'brief', from: { m: 'start', off: -0.3 }, dur: 6.0, enter: 'none', exit: 'none', cam: FULL(),
    hl: [{ t: 1.4, m: 'text:Refunds send their receipt through the s', line: 0 }, { t: 1.7, m: 'text:Refunds send their receipt through the s', line: 2 }] }],
  // The idea page keeps its content in the left three quarters; the frame follows it.
  'clip-workshop': [{ id: 'c', rec: 'workshop', from: { m: 'start', off: -0.2 }, dur: 11.4, enter: 'none', exit: 'none',
    cam: [{ t: 0, c: [790, 625], w: 1580 }] }],
  'clip-checklist': [{ id: 'c', rec: 'checklist', from: { m: 'list', off: -0.2 }, dur: 6.6, enter: 'none', exit: 'none', cam: FULL(),
    hl: [{ t: 1.2, m: 'text:Shoppers can pay without an account' }, { t: 2.2, m: 'text:A confirmation email arrives within a mi' }] }],
  'clip-room': [{ id: 'c', rec: 'team', from: { m: 'start', off: -0.2 }, dur: 8.0, enter: 'none', exit: 'none', cam: FULL() }],
  'clip-gardener': [{ id: 'c', rec: 'gardener', from: { m: 'expanded', off: -0.3 }, dur: 6.0, enter: 'none', exit: 'none', cam: FULL(),
    hl: [{ t: { m: 'text:the manual step is gone', off: 0.2 }, m: 'text:the manual step is gone' }] }],
  'clip-workflows': [{ id: 'c', rec: 'workflows', from: { m: 'start', off: -0.2 }, dur: 8.0, enter: 'none', exit: 'none', cam: FULL(),
    hl: [{ t: 1.2, d: 3.3, m: 'text:both need a server-side list per shopper' }] }],
  'clip-agent-memory': [
    { id: 'c1', rec: 'approve', from: { m: 'click-approve', off: -1.2 }, dur: 3.8, enter: 'none', exit: 'none', cam: FULL(),
      hl: [{ t: 1.6, m: 'banner' }] },
    { id: 'c2', rec: 'payoff', from: { m: 'start', off: 0.1 }, dur: 4.5, enter: 'cut', exit: 'none', cam: FULL(),
      hl: [{ t: 1.0, m: 'text:a repeated callback sends nothing' }] },
  ],
};

function writeProject(name, fmt, shots) {
  const dir = `${ROOT}/films/${name}`;
  fs.rmSync(dir, { recursive: true, force: true });
  fs.mkdirSync(`${dir}/compositions`, { recursive: true });
  fs.symlinkSync('../../assets', `${dir}/assets`);
  fs.copyFileSync(`${ROOT}/hyperframes.json`, `${dir}/hyperframes.json`);
  let t = 0, hosts = [];
  shots.forEach((s, i) => {
    const id = s.id;
    const enter = i === 0 || s.enter === 'cut' || s.enter === 'none' ? 'none' : (s.enter || 'whip');
    const exit = i === shots.length - 1 || s.exit === 'cut' || s.exit === 'none' ? 'none' : (s.exit || 'whip');
    const html = uiShot(fmt, s, id, { enter, exit });
    fs.writeFileSync(`${dir}/compositions/${id}.html`, html);
    hosts.push(`<div id="${id}" data-composition-id="${id}" data-composition-src="compositions/${id}.html" data-start="${r2(t)}" data-duration="${r2(s.dur)}" data-track-index="1" data-track-kind="graphics" data-width="${fmt.W}" data-height="${fmt.H}"></div>`);
    t += s.dur;
  });
  const total = r2(t);
  fs.writeFileSync(`${dir}/index.html`, `<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=${fmt.W}, height=${fmt.H}" />
    <script src="https://cdn.jsdelivr.net/npm/gsap@3.15.0/dist/gsap.min.js"></script>
    <style>
      * { margin: 0; padding: 0; box-sizing: border-box; }
      html, body { width: ${fmt.W}px; height: ${fmt.H}px; overflow: hidden; background: ${BLUE}; }
      #root { position: relative; width: 100%; height: 100%; }
    </style>
  </head>
  <body>
    <div id="root" data-composition-id="main" data-start="0" data-duration="${total}" data-width="${fmt.W}" data-height="${fmt.H}">
      ${hosts.join('\n      ')}
    </div>
    <script>
      window.__timelines["main"] = gsap.timeline({ paused: true });
    </script>
  </body>
</html>
`);
  console.log(name, fmt.W + 'x' + fmt.H, total + ' s');
}

const only = process.argv.slice(2);
const want = (n) => !only.length || only.includes(n);
for (const [n, shots] of Object.entries(CLIPS)) if (want(n)) writeProject(n, FORMATS.clip, shots);
