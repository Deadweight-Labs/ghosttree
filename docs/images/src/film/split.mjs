// node split.mjs [landscape|vertical|hero ...]   (FILM_ROOT: the directory with assets/ and films/; default: here)
// Writes HyperFrames projects in the look "word | detail": a blue field with one word, a white field with one
// element of the real interface (a still from moments.mjs, assets/moments/<name>.png + .json), everything else in
// that still blurred and faded. Landscape: word left, detail right. Vertical: word on top, detail below, inside the
// platform safe area (x 120-900, y 250-1436 on 1080x1920). Hero: the silent README loop.
import fs from 'node:fs';
import path from 'node:path';

const ROOT = process.env.FILM_ROOT || path.dirname(new URL(import.meta.url).pathname);
const BLUE = '#2345c7', YELLOW = '#eed46c', INK = '#000';
const r2 = (x) => Math.round(x * 1000) / 1000;
const M = {};
const mom = (n) => (M[n] ??= JSON.parse(fs.readFileSync(`${ROOT}/assets/moments/${n}.json`, 'utf8')));

// field: the blue rectangle; panel: the white one; focus: where the element's centre goes and how big it may get.
const FORMATS = {
  landscape: { W: 1920, H: 1080, field: [0, 0, 760, 1080], panel: [760, 0, 1160, 1080], word: 150,
    focus: { cx: 1340, cy: 540, w: 960, h: 760, max: 1.25 } },
  vertical: { W: 1080, H: 1920, field: [0, 0, 1080, 900], panel: [0, 900, 1080, 1020], word: 196, wordCy: 560,
    focus: { cx: 510, cy: 1220, w: 780, h: 520, max: 1.0 } },
  hero: { W: 1280, H: 720, field: [0, 0, 500, 720], panel: [500, 0, 780, 720], word: 100,
    focus: { cx: 890, cy: 360, w: 640, h: 520, max: 0.85 } },
};

const fontFaces = `
@font-face { font-family: 'Weight'; src: url('assets/fonts/fira-condensed-heavy.woff2') format('woff2'); font-weight: 900; }
@font-face { font-family: 'Plex'; src: url('assets/fonts/plex-regular.woff2') format('woff2'); font-weight: 400; }
@font-face { font-family: 'Plex'; src: url('assets/fonts/plex-medium.woff2') format('woff2'); font-weight: 500; }`;

const wrap = (fmt, id, inner, css, js) => `<template>
  <style>${fontFaces}
    #root { position: absolute; inset: 0; overflow: hidden; background: #fff; }
    #${id}-field { position: absolute; background: ${BLUE}; }
    .word { position: absolute; margin: 0; white-space: nowrap; font: 900 ${fmt.word}px/1 'Weight', sans-serif; letter-spacing: -0.02em; color: #fff; }
    ${css}
  </style>
  <div id="root" data-composition-id="${id}" data-width="${fmt.W}" data-height="${fmt.H}">${inner}
  </div>
  <script>
    const tl = gsap.timeline({ paused: true });
    ${js.trim().replace(/\n/g, '\n    ')}
    window.__timelines["${id}"] = tl;
  </script>
</template>
`;
const fieldBox = (f) => `left:${f[0]}px;top:${f[1]}px;width:${f[2]}px;height:${f[3]}px`;
const vert = (fmt) => fmt.H > fmt.W;
// The word sits in the middle of the field (vertical: around wordCy, inside the safe area).
function wordPos(fmt) {
  const [x, y, w, h] = fmt.field;
  return vert(fmt) ? { left: 0, width: 1020, top: fmt.wordCy - fmt.word * 0.5 } : { left: x, width: w, top: y + h / 2 - fmt.word * 0.5 };
}

// ---- one shot: word left/top, one element right/below ----------------------------------------------------------
function detailShot(fmt, id, s) {
  const m = mom(s.moment);
  const F = fmt.focus;
  // vfocus: a tighter framing of the same still for the narrow vertical panel (moments.mjs writes it as alt).
  const f = (vert(fmt) && s.vfocus && m.alt?.[s.vfocus]) || m.focus;
  const sc = r2(Math.min(F.w / f.w, F.h / f.h, F.max));
  // Centred horizontally on the element's own box, so a tighter vfocus never pushes the box off the frame.
  const fcx = m.focus.x + m.focus.w / 2, fcy = f.y + f.h / 2;
  const [px, py] = fmt.panel;
  // The still's layer, placed so the focus centre sits on the panel's focus point; it scales around that point.
  const left = r2(F.cx - px - fcx), top = r2(F.cy - py - fcy);
  const pad = s.pad ?? 40;
  // The sharp area is always the element's own box (a tighter vfocus only sizes and centres it).
  const c = m.focus;
  const clip = `inset(${Math.max(0, c.y - pad)}px ${Math.max(0, m.w - c.x - c.w - pad)}px ${Math.max(0, m.h - c.y - c.h - pad)}px ${Math.max(0, c.x - pad)}px round 18px)`;
  const hls = (s.hl != null ? [s.hl].flat() : []).flatMap((i) => m.hl[i].lines.filter((_, k) => !s.hlLines || s.hlLines.includes(k)));
  const wp = wordPos(fmt);
  const inner = `
    <div id="${id}-panel" style="position:absolute;${fieldBox(fmt.panel)};overflow:hidden;background:#fff">
      <div id="${id}-cam" style="position:absolute;left:${left}px;top:${top}px;width:${m.w}px;height:${m.h}px;transform-origin:${fcx}px ${fcy}px">
        <div id="${id}-soft" style="position:absolute;left:0;top:0;width:${m.w}px;height:${m.h}px;background:url('assets/moments/${s.moment}.png') 0 0 / 100% 100% no-repeat;filter:blur(5px);opacity:0.3"></div>
        <div id="${id}-sharp" style="position:absolute;inset:0;clip-path:${clip}">
          <img src="assets/moments/${s.moment}.png" style="position:absolute;left:0;top:0;width:${m.w}px;height:${m.h}px">
          ${hls.map((b, i) => `<div id="${id}-hl${i}" class="hl" style="left:${b.x - 6}px;top:${b.y - 2}px;width:${b.w + 12}px;height:${b.h + 4}px"></div>`).join('\n          ')}
        </div>
      </div>
    </div>
    ${fieldDiv(fmt, id, false)}
    <h2 id="${id}-w" class="word" style="left:${wp.left}px;top:${wp.top}px;width:${wp.width}px;text-align:center">${s.word}</h2>`;
  const css = `.hl { position: absolute; background: ${YELLOW}; mix-blend-mode: multiply; transform-origin: 0 50%; border-radius: 6px; }`;
  const d = s.dur, hlT = s.hlAt ?? 1.1;
  // first: everything is there on frame one (the hook); fromFull: the field shrinks from the whole frame first.
  const wordAt = s.fromFull ? 0.3 : 0.0;
  let js = `
tl.set('#${id}-cam', { scale: ${sc} }, 0);
tl.to('#${id}-cam', { scale: ${r2(sc * 1.035)}, duration: ${d}, ease: 'none' }, 0);`;
  if (!s.first) js += `
tl.fromTo('#${id}-w', { opacity: 0, filter: 'blur(10px)' }, { opacity: 1, filter: 'blur(0px)', duration: 0.3, ease: 'power2.out' }, ${wordAt});
tl.fromTo('#${id}-sharp', { opacity: 0.35, filter: 'blur(6px)' }, { opacity: 1, filter: 'blur(0px)', duration: 0.3, ease: 'power2.out' }, 0);`;
  if (s.fromFull) js += `\n${fieldTween(fmt, id, 'toSplit', 0, 0.45)}`;
  hls.forEach((_, i) => { js += `\ntl.fromTo('#${id}-hl${i}', { scaleX: 0 }, { scaleX: 1, duration: 0.35, ease: 'power2.out' }, ${r2(hlT + i * 0.3)});`; });
  // The word leaves just before the cut; the detail stays until the next one replaces it.
  if (!s.last) js += `\ntl.to('#${id}-w', { opacity: 0, filter: 'blur(10px)', duration: 0.18, ease: 'power2.in' }, ${r2(d - (s.toFull ? 0.5 : 0.2))});`;
  if (s.toFull) js += `\n${fieldTween(fmt, id, 'toFull', d - 0.45, 0.45)}`;
  return wrap(fmt, id, inner, css, js);
}

// The blue field is always the whole frame, scaled from the top left corner down to its split size, so it moves
// on transforms only. toFull grows it to the frame, toSplit shrinks it back.
const splitScale = (fmt) => ({ scaleX: r2(fmt.field[2] / fmt.W), scaleY: r2(fmt.field[3] / fmt.H) });
function fieldTween(fmt, id, dir, at, dur) {
  const split = splitScale(fmt), full = { scaleX: 1, scaleY: 1 };
  const [a, b] = dir === 'toFull' ? [split, full] : [full, split];
  return `tl.fromTo('#${id}-field', ${JSON.stringify(a)}, { ...${JSON.stringify(b)}, duration: ${dur}, ease: 'power3.inOut' }, ${r2(at)});`;
}
const fieldDiv = (fmt, id, full) => {
  const sc = full ? { scaleX: 1, scaleY: 1 } : splitScale(fmt);
  return `<div id="${id}-field" style="left:0;top:0;width:${fmt.W}px;height:${fmt.H}px;transform-origin:0 0;transform:scale(${sc.scaleX},${sc.scaleY})"></div>`;
};

// ---- the name, full blue -------------------------------------------------------------------------------------
function revealShot(fmt, id, s) {
  const size = vert(fmt) ? 180 : fmt === FORMATS.hero ? 150 : 260;
  const top = vert(fmt) ? 840 : Math.round(fmt.H / 2 - size * 0.55);
  const letters = 'Ghosttree'.split('').map((c) => `<span class="l">${c}</span>`).join('');
  const inner = `
    ${fieldDiv(fmt, id, true)}
    <h1 id="${id}-name" class="word" style="left:0;width:${vert(fmt) ? 1020 : fmt.W}px;top:${top}px;text-align:center;font-size:${size}px">${letters}</h1>`;
  const css = `.l { display: inline-block; }`;
  const js = `
tl.fromTo('#${id}-name .l', { opacity: 0.2, filter: 'blur(10px)', y: 24 }, { opacity: 1, filter: 'blur(0px)', y: 0, duration: 0.42, stagger: 0.04, ease: 'expo.out' }, 0.02);
tl.to('#${id}-name .l', { opacity: 0, filter: 'blur(8px)', duration: 0.2, stagger: 0.012, ease: 'power2.in' }, ${r2(s.dur - 0.36)});`;
  return wrap(fmt, id, inner, css, js);
}

// ---- end card: mark and name, the claim, the address -------------------------------------------------------------
function endShot(fmt, id, s) {
  const v = vert(fmt), hero = fmt === FORMATS.hero;
  const mk = v ? 150 : hero ? 84 : 132, ws = v ? 136 : hero ? 84 : 132;
  const claimSize = v ? 120 : hero ? 50 : 76;
  const lockTop = v ? 380 : hero ? 150 : 250;
  const claimTop = v ? 720 : hero ? 300 : 500;
  const urlTop = v ? 1300 : hero ? 470 : 760;
  const claim = v ? `Context for<br>coding agents<br>that stays <span id="${id}-tru" class="tru"><span id="${id}-mk" class="mk"></span><span class="tt">true.</span></span>`
    : `Context for coding agents<br>that stays <span id="${id}-tru" class="tru"><span id="${id}-mk" class="mk"></span><span class="tt">true.</span></span>`;
  const inner = `
    ${fieldDiv(fmt, id, true)}
    <div id="${id}-lock" style="position:absolute;left:0;right:0;top:${lockTop}px;display:flex;align-items:center;justify-content:center;gap:${v ? 28 : 30}px">
      <svg id="${id}-mark" viewBox="0 0 32 32" width="${mk}" height="${mk}" aria-hidden="true"><path fill="#fff" d="M6 29V14.5C6 8.7 10.5 4 16 4s10 4.7 10 10.5V29l-3.4-2.6L19.2 29 16 26.4 12.8 29l-3.4-2.6z"/><circle cx="12" cy="14.5" r="2" fill="${BLUE}"/><circle cx="20" cy="14.5" r="2" fill="${BLUE}"/></svg>
      <div id="${id}-name" style="font:900 ${ws}px/1 'Weight',sans-serif;letter-spacing:-0.02em;color:#fff">Ghosttree</div>
    </div>
    <h2 id="${id}-claim" class="word" style="left:0;width:${fmt.W}px;top:${claimTop}px;text-align:center;white-space:normal;font-size:${claimSize}px;line-height:1.08">${claim}</h2>
    ${s.url === false ? '' : `<div style="position:absolute;left:0;right:0;top:${urlTop}px;display:flex;justify-content:center">
      <div id="${id}-url" style="background:${YELLOW};color:${INK};font:500 ${v ? 48 : hero ? 26 : 38}px/1 'Plex',sans-serif;padding:${v ? 28 : 22}px ${v ? 30 : 30}px">github.com/Deadweight-Labs/ghosttree</div>
    </div>`}`;
  const css = `.tru { position: relative; display: inline-block; padding: 0 0.1em; }
    .mk { position: absolute; inset: 0.1em 0 0.02em 0; background: ${YELLOW}; transform-origin: 0 50%; }
    .tt { position: relative; }`;
  const t = s.times || { lock: 0.05, claim: 0.85, mark: 1.65, url: 2.5 };
  let js = `
tl.fromTo('#${id}-mark', { y: -60, opacity: 0 }, { y: 0, opacity: 1, duration: 0.5, ease: 'back.out(1.6)' }, ${t.lock});
tl.fromTo('#${id}-name', { clipPath: 'inset(0 100% 0 0)' }, { clipPath: 'inset(0 0% 0 0)', duration: 0.45, ease: 'power3.out' }, ${r2(t.lock + 0.2)});
tl.fromTo('#${id}-claim', { opacity: 0, filter: 'blur(10px)' }, { opacity: 1, filter: 'blur(0px)', duration: 0.4, ease: 'power2.out' }, ${t.claim});
tl.fromTo('#${id}-mk', { scaleX: 0 }, { scaleX: 1, duration: 0.35, ease: 'power2.out' }, ${t.mark});
tl.fromTo('#${id}-tru .tt', { color: '#fff' }, { color: '${INK}', duration: 0.12 }, ${r2(t.mark + 0.22)});`;
  if (s.url !== false) js += `\ntl.fromTo('#${id}-url', { scaleX: 0, transformOrigin: '50% 50%' }, { scaleX: 1, duration: 0.4, ease: 'expo.out' }, ${t.url});`;
  if (s.fadeOut) js += `\ntl.to(['#${id}-lock', '#${id}-claim'], { opacity: 0, filter: 'blur(8px)', duration: 0.3, ease: 'power2.in' }, ${r2(s.dur - 0.4)});`;
  return wrap(fmt, id, inner, css, js);
}

// ---- shot lists -------------------------------------------------------------------------------------------------
const FILM = [
  { id: 'twice', moment: 'twice', word: 'Twice.', dur: 5.0, hl: 0, hlAt: 0.9, first: true },
  { id: 'reveal', type: 'reveal', dur: 3.0 },
  { id: 'learned', moment: 'learned', word: 'Learned.', dur: 4.5, hl: 0, fromFull: true, hlAt: 1.3 },
  { id: 'checked', moment: 'checked', word: 'Checked.', dur: 4.5, hl: 0, vfocus: 'text' },
  { id: 'told', moment: 'told', word: 'Told.', dur: 5.0, hl: 0 },
  { id: 'once', moment: 'once', word: 'Once.', dur: 5.5, hl: 0, toFull: true },
  { id: 'end', type: 'end', dur: 6.0 },
];
const HERO = [
  { id: 'h1', moment: 'twice', word: 'Twice.', dur: 3.6, hl: 0, hlAt: 0.8, first: true },
  { id: 'h2', moment: 'checked', word: 'Checked.', dur: 3.4, hl: 0, hlAt: 0.9 },
  { id: 'h3', moment: 'told', word: 'Told.', dur: 3.8, hl: 0, hlAt: 0.9 },
  { id: 'h4', moment: 'once', word: 'Once.', dur: 3.8, hl: 0, hlAt: 0.9, toFull: true },
  { id: 'h5', type: 'end', dur: 3.6, url: false, fadeOut: true, times: { lock: 0.1, claim: 0.7, mark: 1.3 } },
];

// Music for the film: "Arpent" (Kevin MacLeod, CC0), 120 BPM, beats on x.02 / x.52 of the file. The drop (128.02)
// lands on the name at 5.0; a 6 s splice at the cut 22.0 lets the track's last hit (159.03) land on the address at 30.0.
function filmAudio(shots, total) {
  const at = {};
  let t = 0;
  for (const s of shots) { at[s.id] = t; t += s.dur; }
  const sfx = [{ f: 'riser', t: 2.9, v: 0.4, ms: 7.9, d: 2.1 }];
  for (const s of shots) if (s.hl != null) sfx.push({ f: 'click-soft', t: r2(at[s.id] + (s.hlAt ?? 1.1)), v: 0.35 });
  return `
      <audio id="music-a" class="clip" src="assets/music/Arpent.mp3" data-start="0" data-duration="22.3" data-media-start="123.02" data-volume="0.75" data-fade-in="0.6" data-fade-out="0.3" data-track-index="20"></audio>
      <audio id="music-b" class="clip" src="assets/music/Arpent.mp3" data-start="22" data-duration="${r2(Math.min(total - 22, 162.0 - 151.02))}" data-media-start="151.02" data-volume="0.75" data-fade-in="0.3" data-fade-out="1.0" data-track-index="21"></audio>
      ${sfx.map((a, i) => `<audio id="sfx-${i}" class="clip" src="assets/sfx/${a.f}.mp3" data-start="${a.t}" data-duration="${a.d ?? 0.4}"${a.ms ? ` data-media-start="${a.ms}"` : ''} data-volume="${a.v}" data-track-index="${22 + i}"></audio>`).join('\n      ')}`;
}

function writeProject(name, fmt, shots, { audio = false } = {}) {
  const dir = `${ROOT}/films/${name}`;
  fs.rmSync(dir, { recursive: true, force: true });
  fs.mkdirSync(`${dir}/compositions`, { recursive: true });
  fs.symlinkSync('../../assets', `${dir}/assets`);
  fs.writeFileSync(`${dir}/hyperframes.json`, '{"paths":{"assets":"assets"}}\n');
  let t = 0;
  const hosts = [];
  shots.forEach((s, i) => {
    const html = s.type === 'reveal' ? revealShot(fmt, s.id, s) : s.type === 'end' ? endShot(fmt, s.id, s) : detailShot(fmt, s.id, { ...s, last: i === shots.length - 1 });
    fs.writeFileSync(`${dir}/compositions/${s.id}.html`, html);
    hosts.push(`<div id="${s.id}" data-composition-id="${s.id}" data-composition-src="compositions/${s.id}.html" data-start="${r2(t)}" data-duration="${r2(s.dur)}" data-track-index="1" data-track-kind="graphics" data-width="${fmt.W}" data-height="${fmt.H}"></div>`);
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
      html, body { width: ${fmt.W}px; height: ${fmt.H}px; overflow: hidden; background: #fff; }
      #root { position: relative; width: 100%; height: 100%; }
    </style>
  </head>
  <body>
    <div id="root" data-composition-id="main" data-start="0" data-duration="${total}" data-width="${fmt.W}" data-height="${fmt.H}">
      ${hosts.join('\n      ')}${audio ? filmAudio(shots, total) : ''}
    </div>
    <script>
      window.__timelines["main"] = gsap.timeline({ paused: true });
    </script>
  </body>
</html>
`);
  console.log(name, `${fmt.W}x${fmt.H}`, `${total} s`);
}

const only = process.argv.slice(2);
const want = (n) => !only.length || only.includes(n);
if (want('landscape')) writeProject('landscape', FORMATS.landscape, FILM, { audio: true });
if (want('vertical')) writeProject('vertical', FORMATS.vertical, FILM, { audio: true });
if (want('hero')) writeProject('hero', FORMATS.hero, HERO);
