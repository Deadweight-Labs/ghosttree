// node gen.mjs [name ...]  — writes HyperFrames projects under $FILM_ROOT/films/ from the shot lists below.
// $FILM_ROOT holds assets/rec (takes.mjs output plus lengths.json), assets/fonts, and for the launch film
// assets/music and assets/sfx (not in this repository; see docs/images/src/README.md).
// Every UI shot plays a real recording of the Ghosttree interface (assets/rec, 2560x1600 = 1280x800 CSS px at 2x)
// and frames it with a camera given in recording pixels. Mark names come from assets/rec/<take>.marks.json,
// which the recorder writes next to each take, so cuts and highlights follow the real clicks.
import fs from 'node:fs';
import path from 'node:path';

const ROOT = process.env.FILM_ROOT || path.dirname(new URL(import.meta.url).pathname);
const REC_W = 2560, REC_H = 1600;
const BLUE = '#2345c7', CANVAS = '#e6edf0', YELLOW = '#eed46c', INK = '#000';
const marks = {};
const sfxPath = `${ROOT}/assets/sfx/lengths.json`;
const sfxLen = fs.existsSync(sfxPath) ? JSON.parse(fs.readFileSync(sfxPath, 'utf8')) : {};
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
  landscape: { W: 1920, H: 1080, win: { x: 160, y: 244, w: 1600, h: 836, open: true }, title: { x: 160, y: 70, size: 84, w: 1600 }, vw: 1, radius: 22 },
  vertical: { W: 1080, H: 1920, win: { x: 40, y: 500, w: 1000, h: 820 }, title: { x: 80, y: 196, size: 92, w: 920 }, vw: 0.62, radius: 22 },
  hero: { W: 1280, H: 720, win: { x: 90, y: 140, w: 1100, h: 580, open: true }, title: { x: 90, y: 44, size: 58, w: 1100 }, vw: 1, radius: 16 },
  clip: { W: 1600, H: 1000, win: { x: 0, y: 0, w: 1600, h: 1000 }, title: null, vw: 1, radius: 0 },
};

// Camera keyframe {t, c:[x,y] or box, w} -> transform of the 2560x1600 layer inside the window.
function camState(fmt, kf) {
  const win = fmt.win;
  const aspect = win.w / win.h;
  let vw = Math.min(kf.w * (kf.vfix ? 1 : fmt.vw), REC_W);
  const vert = fmt === FORMATS.vertical;
  if (vert && kf.wv) vw = kf.wv;
  let vh = vw / aspect;
  if (vh > REC_H) { vh = REC_H; vw = vh * aspect; }
  const [cx, cy] = kf.c.length === 2 ? kf.c : [kf.c[0] + kf.c[2] / 2, kf.c[1] + kf.c[3] / 2];
  const left = vert && kf.lv != null ? kf.lv : kf.l;
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
  const vo = fmt === FORMATS.vertical ? (VERT[id] || []) : [];
  const cams = (s.cam || []).map((k0, i) => ({ ...k0, ...(vo[i] || {}) })).map(k => ({ ...k, t: at(k.t), st: camState(fmt, { ...k, c: resolveC(k.c) }) }));
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

// ---- type scenes -----------------------------------------------------------------------------------------------
function typeScene(fmt, kind, id, dur) {
  const big = fmt.W > fmt.H ? 1 : 0.86;
  const S = (n) => Math.round(n * (fmt.H / 1080) * (fmt.W > fmt.H ? 1 : 0.62) * (fmt === FORMATS.hero ? 1 : 1));
  const css = `${fontFaces}
    #root { position: absolute; inset: 0; overflow: hidden; }
    .line { position: absolute; left: 0; right: 0; margin: 0; text-align: center; font-weight: 900; font-family: 'Weight', sans-serif; letter-spacing: -0.02em; }
    .sub { position: absolute; left: 0; right: 0; margin: 0; text-align: center; font: 500 ${S(40)}px/1.3 'Plex', sans-serif; }`;
  let body = '', js = '';
  const v = fmt.W < fmt.H;
  if (kind === 'zero') {
    const size = v ? 132 : S(128);
    body = `
    <div id="${id}-bg" style="position:absolute;inset:0;background:${CANVAS}"></div>
    <h2 id="${id}-a" class="clip line" data-start="0.15" data-duration="2.25" data-track-index="2" style="top:${v ? 640 : S(330)}px;font-size:${size}px;line-height:1.02;color:${INK};padding:0 ${v ? 60 : 0}px">${words(v ? 'Next week, a new session starts from zero.' : 'Next week, a new session')}${v ? '' : `<br>${words('starts from zero.')}`}</h2>
    <h2 id="${id}-b" class="clip line" data-start="2.45" data-duration="${r2(dur - 2.45)}" data-track-index="3" style="top:${v ? 700 : S(400)}px;font-size:${size}px;line-height:1.02;color:${INK};padding:0 ${v ? 60 : 0}px">${words('Unless your project remembers.')}</h2>
    <div id="${id}-flood" style="position:absolute;inset:0;background:${BLUE}"></div>`;
    js = `tl.fromTo('#${id}-a .w', { opacity: 0, filter: 'blur(8px)' }, { opacity: 1, filter: 'blur(0px)', duration: 0.14, stagger: 0.09, ease: 'power2.out' }, 0.15);
    tl.fromTo('#${id}-a .w:nth-last-child(1)', { color: '${INK}' }, { color: '${BLUE}', duration: 0.2 }, 1.2);
    tl.to('#${id}-a .w', { opacity: 0, filter: 'blur(8px)', duration: 0.16, stagger: 0.02 }, 2.2);
    tl.fromTo('#${id}-b .w', { opacity: 0, filter: 'blur(8px)' }, { opacity: 1, filter: 'blur(0px)', duration: 0.14, stagger: 0.08, ease: 'power2.out' }, 2.45);
    tl.fromTo('#${id}-flood', { clipPath: 'inset(100% 0 0 0)' }, { clipPath: 'inset(0% 0 0 0)', duration: 0.42, ease: 'power2.inOut' }, ${r2(dur - 0.42)});`;
  } else if (kind === 'reveal') {
    const size = v ? 230 : fmt === FORMATS.hero ? 190 : 300;
    const letters = 'Ghosttree'.split('').map(c => `<span class="l">${c}</span>`).join('');
    body = `
    <div style="position:absolute;inset:0;background:${BLUE}"></div>
    <h1 id="${id}-name" class="clip line" data-start="0" data-duration="${dur}" data-track-index="2" style="top:${v ? 760 : Math.round(fmt.H * 0.5 - size * 0.62)}px;font-size:${size}px;line-height:1;color:#fff">${letters}</h1>
    <p id="${id}-sub" class="clip sub" data-start="0.5" data-duration="${r2(dur - 0.5)}" data-track-index="3" style="top:${v ? 1010 : Math.round(fmt.H * 0.5 + size * 0.48)}px;color:#fff;font-size:${v ? 46 : S(40)}px">${words('keeps what your project knows.')}</p>`;
    js = `tl.fromTo('#${id}-name .l', { opacity: 0.25, filter: 'blur(10px)', y: 30 }, { opacity: 1, filter: 'blur(0px)', y: 0, duration: 0.4, stagger: 0.045, ease: 'expo.out' }, 0);
    tl.fromTo('#${id}-sub .w', { opacity: 0, filter: 'blur(8px)' }, { opacity: 1, filter: 'blur(0px)', duration: 0.14, stagger: 0.07 }, 0.5);
    tl.to('#${id}-name .l', { opacity: 0, filter: 'blur(8px)', duration: 0.2, stagger: 0.015, ease: 'power2.in' }, ${r2(dur - 0.28)});
    tl.to('#${id}-sub .w', { opacity: 0, duration: 0.15 }, ${r2(dur - 0.28)});`;
  } else if (kind === 'claim') {
    const size = v ? 128 : fmt === FORMATS.hero ? 80 : 124;
    body = `
    <div style="position:absolute;inset:0;background:${BLUE}"></div>
    <h2 id="${id}-c" class="clip line" data-start="0.1" data-duration="${r2(dur - 0.1)}" data-track-index="2" style="top:${v ? 640 : Math.round(fmt.H * 0.5 - size * 1.05)}px;font-size:${size}px;line-height:1.04;color:#fff;padding:0 ${v ? 70 : 0}px">${words('Context for coding agents')}<br>${words('that stays')} <span class="w tru" style="position:relative;display:inline-block;isolation:isolate;color:${INK};padding:0 0.12em"><span id="${id}-mk" style="position:absolute;inset:0.08em 0 0.02em 0;background:${YELLOW};transform-origin:0 50%"></span><span style="position:relative">true.</span></span></h2>`;
    js = `tl.fromTo('#${id}-c .w', { opacity: 0, filter: 'blur(8px)' }, { opacity: 1, filter: 'blur(0px)', duration: 0.14, stagger: 0.08, ease: 'power2.out' }, 0.1);
    tl.fromTo('#${id}-c .tru', { color: '#fff' }, { color: '${INK}', duration: 0.1 }, ${r2(0.1 + 0.08 * 6 + 0.25)});
    tl.fromTo('#${id}-mk', { scaleX: 0 }, { scaleX: 1, duration: 0.35, ease: 'power2.out' }, ${r2(0.1 + 0.08 * 6 + 0.2)});
    tl.to('#${id}-c', { opacity: 0, filter: 'blur(8px)', duration: 0.22, ease: 'power2.in' }, ${r2(dur - 0.26)});`;
  } else if (kind === 'end') {
    const mk = v ? 200 : fmt === FORMATS.hero ? 110 : 168;
    const ws = v ? 150 : fmt === FORMATS.hero ? 100 : 150;
    const cx = fmt.W / 2;
    const top = v ? 560 : Math.round(fmt.H * 0.5 - mk * 0.95);
    body = `
    <div style="position:absolute;inset:0;background:${BLUE}"></div>
    <div id="${id}-lock" style="position:absolute;left:0;right:0;top:${top}px;display:flex;${v ? 'flex-direction:column;' : ''}align-items:center;justify-content:center;gap:${v ? 30 : 38}px">
      <svg id="${id}-mark" viewBox="0 0 32 32" width="${mk}" height="${mk}" aria-hidden="true"><path fill="#fff" d="M6 29V14.5C6 8.7 10.5 4 16 4s10 4.7 10 10.5V29l-3.4-2.6L19.2 29 16 26.4 12.8 29l-3.4-2.6z"/><circle cx="12" cy="14.5" r="2" fill="${BLUE}"/><circle cx="20" cy="14.5" r="2" fill="${BLUE}"/></svg>
      <div id="${id}-word" style="font:900 ${ws}px/1 'Weight',sans-serif;letter-spacing:-0.02em;color:#fff">Ghosttree</div>
    </div>
    <p id="${id}-sub" class="clip sub" data-start="${v ? 2.6 : 2.6}" data-duration="${r2(dur - 2.6)}" data-track-index="3" style="top:${v ? 1060 : top + mk + (fmt === FORMATS.hero ? 40 : 70)}px;color:#fff;padding:0 60px">${words(v ? 'Self-hosted. For Claude Code, Codex and OpenCode.' : 'Self-hosted. Works with Claude Code, Codex and OpenCode.')}</p>
    <div id="${id}-cta" class="clip" data-start="3.0" data-duration="${r2(dur - 3.0)}" data-track-index="4" style="position:absolute;left:0;right:0;top:${v ? 1190 : top + mk + (fmt === FORMATS.hero ? 100 : 160)}px;display:flex;justify-content:center">
      <div id="${id}-ctab" style="background:${YELLOW};color:${INK};font:500 ${v ? 40 : fmt === FORMATS.hero ? 26 : 38}px/1 'Plex',sans-serif;padding:${v ? 26 : 22}px ${v ? 34 : 30}px">github.com/Deadweight-Labs/ghosttree</div>
    </div>`;
    js = `tl.fromTo('#${id}-mark', { y: -${fmt.H}, rotation: -8 }, { y: 0, rotation: 0, duration: 1.0, ease: 'back.out(1.25)' }, 1.5);
    tl.fromTo('#${id}-word', { clipPath: 'inset(0 100% 0 0)', opacity: 1 }, { clipPath: 'inset(0 0% 0 0)', duration: 0.5, ease: 'power3.out' }, 2.15);
    tl.fromTo('#${id}-sub .w', { opacity: 0, filter: 'blur(8px)' }, { opacity: 1, filter: 'blur(0px)', duration: 0.14, stagger: 0.06 }, 2.6);
    tl.fromTo('#${id}-ctab', { scaleX: 0, transformOrigin: '50% 50%' }, { scaleX: 1, duration: 0.4, ease: 'expo.out' }, 3.0);`;
  }
  return `<template>
  <style>${css}</style>
  <div id="root" data-composition-id="${id}" data-width="${fmt.W}" data-height="${fmt.H}">${body}
  </div>
  <script>
    const tl = gsap.timeline({ paused: true });
    ${js}
    window.__timelines["${id}"] = tl;
  </script>
</template>
`;
}

// ---- the launch film ----------------------------------------------------------------------------------------
const FILM = [
  { id: 'hook', rec: 'fail', from: { m: 'click', off: -0.9 }, dur: 5.4,
    cam: [{ t: 0, l: 0, c: [0, 1250], w: 1700 }, { t: 2.9, l: 80, c: [0, 1230], w: 1150, d: 0.9 }],
    hl: [{ t: 3.6, box: { x: 120, y: 1307, w: 440, h: 28 } }],
    titles: [{ t: 0.1, d: 2.6, text: 'Customers got two receipts.' }, { t: 2.8, text: 'An agent reproduced it.' }] },
  { id: 'found', rec: 'found', from: { m: 'start', off: 0.4 }, dur: 6.0,
    cam: [{ t: 0, c: [1000, 600], w: 1800 }, { t: 0.9, c: { m: 'text:Found it: when the provider answers slow' }, w: 1250, d: 0.8 }, { t: { m: 'theo', off: 0 }, c: [1000, 690], w: 1250, d: 0.8 }],
    hl: [{ t: { m: 'theo', off: 0.9 }, m: 'text:the fix must also cover refunds' }],
    titles: [{ t: 0.1, d: 2.7, text: 'It found the cause.' }, { t: 3.0, text: 'Refunds need the same fix.' }] },
  { id: 'zero', type: 'zero', dur: 3.6 },
  { id: 'reveal', type: 'reveal', dur: 2.5 },
  { id: 'proposed', rec: 'approve', from: { m: 'start', off: 0.2 }, dur: 4.5,
    cam: [{ t: 0, c: [1280, 1150], w: 2400 }, { t: 1.0, c: [1000, 1300], w: 1700, d: 0.8 }],
    titles: [{ t: 0.1, text: 'The agent writes down the lesson.' }] },
  { id: 'approved', rec: 'approve', from: { m: 'click-approve', off: -1.3 }, dur: 3.0, enter: 'cut',
    cam: [{ t: 0, c: [1900, 1300], w: 1500 }, { t: 1.6, l: 40, c: [0, 420], w: 2000, d: 0.6 }],
    hl: [{ t: 2.1, box: { x: 112, y: 394, w: 990, h: 36 } }],
    titles: [{ t: 0.1, text: 'A person approves it.' }] },
  { id: 'briefed', rec: 'brief', from: { m: 'start', off: 0 }, dur: 5.0,
    cam: [{ t: 0, c: [1280, 800], w: 2560 }, { t: 1.2, l: 130, c: [0, 900], w: 1900, d: 0.9 }],
    hl: [{ t: { m: 'line', off: 0.2 }, m: 'text:Refunds send their receipt through the s' }],
    titles: [{ t: 0.1, text: 'The next session starts with it.' }] },
  { id: 'beside', rec: 'code', from: { m: 'click', off: -0.8 }, dur: 4.5,
    cam: [{ t: 0, c: [900, 700], w: 2200 }, { t: 1.4, l: 700, c: [0, 650], w: 1700, d: 0.8 }],
    hl: [{ t: { m: 'text:must not send a second one', off: 0.3 }, m: 'text:must not send a second one' }],
    titles: [{ t: 0.1, text: "Next to the file it's about." }] },
  { id: 'payoff', rec: 'payoff', from: { m: 'start', off: 0.3 }, dur: 4.65, exit: 'cut',
    cam: [{ t: 0, c: [1100, 800], w: 2000 }, { t: 0.8, c: [1080, 900], w: 1300, d: 0.8 }],
    hl: [{ t: 1.2, m: 'msg' }, { t: { m: 'text:a repeated callback sends nothing', off: 0.2 }, m: 'text:a repeated callback sends nothing' }],
    titles: [{ t: 0.1, text: 'The refunds agent gets it right.' }] },
  { id: 'traced', rec: 'payoff', from: { m: 'entry', off: 0.05 }, dur: 3.85, enter: 'cut',
    cam: [{ t: 0, l: 0, c: [0, 600], w: 2200 }, { t: 0.4, l: 0, c: [0, 560], w: 1800, d: 0.8 }],
    hl: [{ t: 0.9, m: 'kn-confirmed' }, { t: 1.5, m: 'text:fix/double-receipts' }],
    titles: [{ t: 0.05, text: 'Every line traces back.' }] },
  { id: 'm1', rec: 'workshop', from: { m: 'decide', off: -0.1 }, dur: 2.1, enter: 'cut', exit: 'cut',
    cam: [{ t: 0, c: [1100, 900], w: 1900 }, { t: 0.95, c: [1100, 820], w: 2000, d: 0.4 }],
    titles: [{ t: 0.05, text: 'Decisions keep the rejected options.' }] },
  { id: 'm2', rec: 'checklist', from: { m: 'list', off: 0.4 }, dur: 2.1, enter: 'cut', exit: 'cut',
    cam: [{ t: 0, c: [700, 600], w: 1600 }],
    titles: [{ t: 0.05, text: 'Every criterion with its commit.' }] },
  { id: 'm3', rec: 'gardener', from: { m: 'expanded', off: 0.2 }, dur: 2.1, enter: 'cut', exit: 'cut',
    cam: [{ t: 0, c: [900, 1150], w: 1700 }],
    titles: [{ t: 0.05, text: 'The gardener flags what went stale.' }] },
  { id: 'm4', rec: 'team', from: { m: 'start', off: 4.4 }, dur: 2.1, enter: 'cut',
    cam: [{ t: 0, c: [1300, 1000], w: 1800 }],
    titles: [{ t: 0.05, text: 'One room for every agent.' }] },
  { id: 'claim', type: 'claim', dur: 4.1 },
  { id: 'end', type: 'end', dur: 5.5 },
];

// Tighter crops for 9:16: text must stay at least 1:1 of the 2x recording to be readable on a phone.
const VERT = {
  hook: [{ lv: 0, wv: 1100 }, { lv: 100, wv: 1000 }],
  found: [{ lv: 600, wv: 1300 }, { lv: 680, wv: 1100 }, { lv: 680, wv: 1100 }],
  proposed: [{ lv: 300, wv: 1300 }, { lv: 360, wv: 1100 }],
  approved: [{ wv: 1000 }, { lv: 60, wv: 1100 }],
  briefed: [{ lv: 100, wv: 1400 }, { lv: 1000, wv: 1000 }],
  beside: [{ lv: 500, wv: 1400 }, { lv: 1000, wv: 1300 }],
  payoff: [{ lv: 600, wv: 1300 }, { lv: 680, wv: 1100 }],
  traced: [{ lv: 0, wv: 1300 }, { lv: 20, wv: 1100 }],
  m1: [{ lv: 200, wv: 1300 }, { lv: 200, wv: 1300 }],
  m2: [{ lv: 180, wv: 1100 }],
  m3: [{ lv: 700, wv: 1100 }],
  m4: [{ lv: 600, wv: 1200 }],
};

const HERO = [
  { id: 'h1', rec: 'fail', from: { m: 'click', off: -0.4 }, dur: 3.4, enter: 'cut',
    cam: [{ t: 0, l: 0, c: [0, 1250], w: 1800 }, { t: 1.6, l: 80, c: [0, 1230], w: 1250, d: 0.7 }],
    hl: [{ t: 2.4, box: { x: 120, y: 1307, w: 440, h: 28 } }],
    titles: [{ t: 0.1, text: 'An agent finds a bug.' }] },
  { id: 'h2', rec: 'approve', from: { m: 'click-approve', off: -1.6 }, dur: 3.6,
    cam: [{ t: 0, c: [1700, 1300], w: 1800 }, { t: 1.9, l: 40, c: [0, 420], w: 2000, d: 0.6 }],
    hl: [{ t: 2.4, box: { x: 112, y: 394, w: 990, h: 36 } }],
    titles: [{ t: 0.1, text: 'A person approves its lesson.' }] },
  { id: 'h3', rec: 'brief', from: { m: 'start', off: 0.6 }, dur: 3.8,
    cam: [{ t: 0, c: [1280, 800], w: 2560 }, { t: 0.7, l: 130, c: [0, 900], w: 1900, d: 0.8 }],
    hl: [{ t: { m: 'line', off: -0.4 }, m: 'text:Refunds send their receipt through the s' }],
    titles: [{ t: 0.1, text: 'The next session starts with it.' }] },
  { id: 'h4', rec: 'payoff', from: { m: 'start', off: 0.3 }, dur: 4.2,
    cam: [{ t: 0, c: [1100, 800], w: 2000 }, { t: 0.6, c: [1080, 900], w: 1300, d: 0.7 }],
    hl: [{ t: 1.2, m: 'msg' }, { t: { m: 'text:a repeated callback sends nothing', off: 0.1 }, m: 'text:a repeated callback sends nothing' }],
    titles: [{ t: 0.1, text: 'And gets the refunds right.' }] },
  { id: 'hc', type: 'claim', dur: 2.8 },
];

// Website feature clips: silent loops of one real action each, no titles (the page section names it).
const CLIPS = {
  'clip-briefing': [{ id: 'c', rec: 'brief', from: 0.3, dur: 7.0, enter: 'none', exit: 'none',
    cam: [{ t: 0, c: [1280, 800], w: 2560 }, { t: 1.4, c: [1300, 860], w: 1800, d: 1.0 }],
    hl: [{ t: { m: 'line', off: 0.1 }, m: 'line' }] }],
  'clip-workshop': [{ id: 'c', rec: 'workshop', from: { m: 'start', off: -0.2 }, dur: 11.4, enter: 'none', exit: 'none',
    cam: [{ t: 0, c: [1100, 750], w: 2200 }, { t: { m: 'reason', off: 0.6 }, c: [1100, 900], w: 1900, d: 0.8 }, { t: { m: 'decided', off: -0.1 }, c: [1100, 900], w: 2000, d: 0.6 }] }],
  'clip-checklist': [{ id: 'c', rec: 'checklist', from: { m: 'start', off: -0.3 }, dur: 8.6, enter: 'none', exit: 'none',
    cam: [{ t: 0, c: [1280, 800], w: 2560 }, { t: { m: 'list', off: 0.3 }, c: [800, 600], w: 1800, d: 0.9 }] }],
  'clip-room': [{ id: 'c', rec: 'team', from: { m: 'start', off: -0.2 }, dur: 9.0, enter: 'none', exit: 'none',
    cam: [{ t: 0, c: [1280, 800], w: 2560 }, { t: 1.2, c: [1350, 850], w: 2100, d: 0.9 }] }],
  'clip-gardener': [{ id: 'c', rec: 'gardener', from: { m: 'start', off: -0.2 }, dur: 8.2, enter: 'none', exit: 'none',
    cam: [{ t: 0, c: [1280, 800], w: 2560 }, { t: { m: 'expanded', off: 0.1 }, c: [1000, 1050], w: 1900, d: 0.9 }] }],
  'clip-workflows': [{ id: 'c', rec: 'workflows', from: { m: 'start', off: -0.2 }, dur: 9.0, enter: 'none', exit: 'none',
    cam: [{ t: 0, c: [1280, 800], w: 2560 }, { t: 1.0, c: [1100, 650], w: 2000, d: 0.9 }, { t: { m: 'run', off: 0.1 }, c: [1100, 800], w: 2300, d: 0.7 }] }],
  'clip-agent-memory': [
    { id: 'c1', rec: 'approve', from: { m: 'click-approve', off: -1.8 }, dur: 3.8, enter: 'none', exit: 'none',
      cam: [{ t: 0, c: [1700, 1300], w: 1900 }, { t: 2.1, c: [1100, 380], w: 2100, d: 0.6 }], hl: [{ t: 2.5, box: { x: 112, y: 394, w: 990, h: 36 } }] },
    { id: 'c2', rec: 'payoff', from: { m: 'start', off: 0.3 }, dur: 4.5, enter: 'cut', exit: 'none',
      cam: [{ t: 0, c: [1100, 800], w: 2100 }, { t: 0.6, c: [1080, 900], w: 1400, d: 0.7 }],
      hl: [{ t: 1.2, m: 'msg' }, { t: { m: 'text:a repeated callback sends nothing', off: 0.1 }, m: 'text:a repeated callback sends nothing' }] },
  ],
};

// ---- sound for the film -----------------------------------------------------------------------------------------
function filmAudio(shots) {
  const out = [];
  let t = 0;
  const seams = [];
  for (const s of shots) {
    if (s.rec) {
      const from = typeof s.from === 'number' ? s.from : mark(s.rec, s.from.m).t + (s.from.off || 0);
      for (const m of marks[s.rec]) if (m.name === 'click' && m.t >= from && m.t < from + s.dur - 0.1) out.push({ f: (out.length % 2) ? 'click-soft' : 'click', t: r2(t + m.t - from + 0.03), v: 0.55 });
    }
    t += s.dur;
    seams.push(r2(t));
  }
  return { sfx: out, seams, total: r2(t) };
}

function writeProject(name, fmt, shots, { audio = false } = {}) {
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
    const html = s.type ? typeScene(fmt, s.type, id, s.dur) : uiShot(fmt, s, id, { enter, exit });
    fs.writeFileSync(`${dir}/compositions/${id}.html`, html);
    hosts.push(`<div id="${id}" data-composition-id="${id}" data-composition-src="compositions/${id}.html" data-start="${r2(t)}" data-duration="${r2(s.dur)}" data-track-index="1" data-track-kind="graphics" data-width="${fmt.W}" data-height="${fmt.H}"></div>`);
    t += s.dur;
  });
  const total = r2(t);
  let aud = '';
  if (audio) {
    const { sfx } = filmAudio(shots);
    const whooshAt = [5.4, 17.5, 25.0, 30.0, 34.5, 43.0];
    const w = whooshAt.map((x, i) => ({ f: i % 2 ? 'whoosh-short' : 'whoosh', t: r2(x - 0.12), v: 0.32 }));
    const all = [...sfx, ...w, { f: 'riser', t: 12.9, v: 0.45, ms: 7.9, d: 2.1 }, { f: 'impact-bass-1', t: 57.95, v: 0.5 }];
    aud = `
      <audio id="music-a" class="clip" src="assets/music/Arpent.mp3" data-start="0" data-duration="36.3" data-media-start="113.02" data-volume="0.75" data-fade-in="0.4" data-fade-out="0.3" data-track-index="20"></audio>
      <audio id="music-b" class="clip" src="assets/music/Arpent.mp3" data-start="36" data-duration="${r2(total - 36)}" data-media-start="137.02" data-volume="0.75" data-fade-in="0.3" data-fade-out="1.2" data-track-index="21"></audio>
      ${all.map((a, i) => `<audio id="sfx-${i}" class="clip" src="assets/sfx/${a.f}.mp3" data-start="${a.t}" data-duration="${a.d ?? Math.min(sfxLen[a.f], r2(total - a.t))}"${a.ms ? ` data-media-start="${a.ms}"` : ''} data-volume="${a.v}" data-track-index="${22 + i}"></audio>`).join('\n      ')}`;
  }
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
      ${hosts.join('\n      ')}${aud}
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
if (want('landscape')) writeProject('landscape', FORMATS.landscape, FILM, { audio: true });
if (want('vertical')) writeProject('vertical', FORMATS.vertical, FILM, { audio: true });
if (want('hero')) writeProject('hero', FORMATS.hero, HERO);
for (const [n, shots] of Object.entries(CLIPS)) if (want(n)) writeProject(n, FORMATS.clip, shots);
