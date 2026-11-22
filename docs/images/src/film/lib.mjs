// Shared helpers for takes.mjs: sign in to the throwaway demo instance and record a scene at 2x with a
// drawn pointer. BASE, WORK (the directory with ctx and gt.db) and PLAYWRIGHT come from film/make.sh.
import fs from 'node:fs';
import { execFileSync } from 'node:child_process';
// PLAYWRIGHT may point at a playwright index.mjs outside this repository.
export const { chromium } = await import(process.env.PLAYWRIGHT || 'playwright');
export const base = process.env.BASE;
export const SF = '/ui/northwind/storefront', PAY = '/ui/northwind/payments';
const work = process.env.WORK;

export function loginLink(who = 'Mia') {
  return execFileSync('env', ['-i', 'PATH=/usr/bin:/bin', `HOME=${work}/home`, `XDG_CONFIG_HOME=${work}/home/.config`, 'TZ=UTC',
    `${work}/ctx`, 'account', 'login-link', who, '--db', `${work}/gt.db`, '--url', base], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] })
    .match(/http\S+/g).pop();
}

export async function storage(browser, scheme = 'light', who = 'Mia') {
  const c = await browser.newContext({ viewport: { width: 1280, height: 720 }, colorScheme: scheme, locale: 'en-US' });
  const p = await c.newPage();
  await p.goto(loginLink(who));
  // The page after sign-in keeps a live connection open, so wait for the address, not for an idle network.
  await Promise.all([p.waitForURL(u => !u.href.includes('/login'), { timeout: 15000 }), p.locator('main form button').first().click()]);
  const s = await c.storageState();
  await c.close();
  return s;
}

// A scene: a recording context with a pointer drawn into the page (headless Chromium draws none).
export async function scene(browser, state, { w = 1280, h = 720, dpr = 2, scheme = 'light', dir }) {
  fs.mkdirSync(dir, { recursive: true });
  const ctx = await browser.newContext({ viewport: { width: w, height: h }, deviceScaleFactor: dpr, colorScheme: scheme, locale: 'en-US',
    timezoneId: 'Europe/Berlin', storageState: state });
  await ctx.addInitScript(() => {
    addEventListener('DOMContentLoaded', () => {
      const c = document.createElement('div');
      c.id = 'mk-cursor';
      c.innerHTML = '<svg width="30" height="30" viewBox="0 0 22 22"><path d="M3 2l14 8-6 1.6L8 18z" fill="#11130f" stroke="#fff" stroke-width="1.6" stroke-linejoin="round"/></svg>';
      Object.assign(c.style, { position: 'fixed', left: '-4px', top: '-3px', zIndex: 2147483647, pointerEvents: 'none', transition: 'scale .12s', transform: `translate(${window.__cx ?? 640}px,${window.__cy ?? 360}px)` });
      document.body.appendChild(c);
      // The pointer shows only on the way to a click (window.__ptr, set by click and clickNav) and fades out
      // when it rests, so it never sits on or wanders over what is read.
      c.style.transition = 'scale .12s, opacity .25s';
      c.style.opacity = '0';
      let idle;
      const wake = () => { if (!window.__ptr) return; c.style.opacity = '1'; clearTimeout(idle); idle = setTimeout(() => { c.style.opacity = '0'; window.__ptr = false; }, 450); };
      addEventListener('mousemove', (e) => { c.style.transform = `translate(${e.clientX}px,${e.clientY}px)`; wake(); });
      addEventListener('mousedown', wake);
      addEventListener('mousedown', () => { c.style.scale = '.82'; });
      addEventListener('mouseup', () => { c.style.scale = '1'; });
    });
  });
  const page = await ctx.newPage();
  // Screencast over CDP at device pixels; Playwright's own recorder stays at 1x.
  const cdp = await ctx.newCDPSession(page);
  const frames = [];
  const fdir = `${dir}/frames-${Date.now()}`;
  fs.mkdirSync(fdir, { recursive: true });
  cdp.on('Page.screencastFrame', async (f) => {
    const file = `${fdir}/${String(frames.length).padStart(5, '0')}.jpg`;
    frames.push({ file, t: Date.now() / 1000 });
    fs.writeFileSync(file, Buffer.from(f.data, 'base64'));
    try { await cdp.send('Page.screencastFrameAck', { sessionId: f.sessionId }); } catch {}
  });
  await cdp.send('Page.startScreencast', { format: 'jpeg', quality: 95, maxWidth: w * dpr, maxHeight: h * dpr, everyNthFrame: 1 });
  let cx = w / 2, cy = h / 2;
  const t0 = Date.now();
  const marks = [];
  // Boxes in recording pixels (CSS px times dpr), so the film can frame them exactly.
  const scaleBox = (b) => ({ x: Math.round(b.x * dpr), y: Math.round(b.y * dpr), w: Math.round(b.width * dpr), h: Math.round(b.height * dpr),
    ...(b.all ? { lines: b.all.map(q => ({ x: Math.round(q.x * dpr), y: Math.round(q.y * dpr), w: Math.round(q.width * dpr), h: Math.round(q.height * dpr) })) } : {}) });
  const api = {
    page,
    pause: (ms) => page.waitForTimeout(ms),
    mark: (name, box) => { marks.push({ name, t: (Date.now() - t0) / 1000, ...(box ? { box: scaleBox(box) } : {}) }); },
    async mask() {
      await page.evaluate(() => {
        const tw = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
        for (let n; (n = tw.nextNode());) n.nodeValue = n.nodeValue.replace(/https?:\/\/127\.0\.0\.1:\d+/g, 'https://ghosttree.northwind.example');
      });
    },
    async go(path) {
      // Pages with a room keep a live connection open, so wait for load and a moment, not for an idle network.
      await page.goto(base + path, { waitUntil: 'load' });
      await page.waitForTimeout(500);
      await api.mask();
      await page.evaluate(([x, y]) => { window.__cx = x; window.__cy = y; }, [cx, cy]);
      await page.mouse.move(cx, cy);
    },
    async glide(x, y, ms = 700) {
      const steps = Math.max(8, Math.round(ms / 16));
      const sx = cx, sy = cy;
      for (let i = 1; i <= steps; i++) {
        const k = i / steps, e = k < .5 ? 4 * k * k * k : 1 - Math.pow(-2 * k + 2, 3) / 2;
        await page.mouse.move(sx + (x - sx) * e, sy + (y - sy) * e);
        await page.waitForTimeout(ms / steps);
      }
      cx = x; cy = y;
    },
    async to(locator, ms = 700, dx = null) {
      const b = await locator.first().boundingBox();
      if (!b) throw new Error('no box for ' + locator);
      await api.glide(Math.round(b.x + (dx ?? Math.min(b.width / 2, 60))), Math.round(b.y + b.height / 2), ms);
      api.mark('to', b);
      return b;
    },
    // Box of the first occurrence of a text, even inside highlighted code.
    async textBox(str) {
      return page.evaluate((s) => {
        const tw = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
        let all = '', nodes = [];
        for (let n; (n = tw.nextNode());) { nodes.push([n, all.length]); all += n.nodeValue; }
        const at = (k) => { let j = nodes.length - 1; while (nodes[j][1] > k) j--; return [nodes[j][0], k - nodes[j][1]]; };
        for (let i = all.indexOf(s); i >= 0; i = all.indexOf(s, i + 1)) {
          const r = document.createRange();
          r.setStart(...at(i)); r.setEnd(...at(i + s.length));
          const rects = [...r.getClientRects()].filter(q => q.width > 0 && q.height > 0);
          if (!rects.length) continue;
          const f = rects[0];
          return { x: f.x, y: f.y, width: f.width, height: f.height, all: rects.map(q => ({ x: q.x, y: q.y, width: q.width, height: q.height })) };
        }
        return null;
      }, str);
    },
    // Point at a text without covering it: the pointer rests just below the line it points at.
    async toText(str, ms = 700, frac = 0.5) {
      const b = await api.textBox(str);
      if (!b) throw new Error('text not found: ' + str);
      const last = b.all ? b.all[b.all.length - 1] : b;
      await api.glide(Math.round(b.x + Math.min(b.width * frac, 200)), Math.round(last.y + last.height + 10), ms);
      api.mark('text:' + str.slice(0, 40), b);
      return b;
    },
    // Record where a text or element is right now, without moving the pointer.
    async note(name, what) {
      const b = typeof what === 'string' ? await api.textBox(what) : await what.first().boundingBox();
      if (b) api.mark(name, b);
      return b;
    },
    async click(locator, ms = 700) {
      await page.evaluate(() => { window.__ptr = true; });
      await api.to(locator, ms);
      await page.waitForTimeout(150);
      api.mark('click');
      await page.mouse.down(); await page.waitForTimeout(90); await page.mouse.up();
    },
    // Click something that loads another page and wait until it is there.
    async clickNav(locator, ms = 700) {
      const old = page.url();
      await page.evaluate(() => { window.__ptr = true; });
      await api.to(locator, ms);
      await page.waitForTimeout(150);
      api.mark('click');
      await page.mouse.down(); await page.waitForTimeout(90);
      await Promise.all([page.waitForURL(u => u.href !== old, { timeout: 10000 }), page.mouse.up()]);
      await page.waitForLoadState('load');
      await page.waitForTimeout(500);
      await api.mask();
    },
    async scroll(dy, ms = 900) {
      const n = Math.max(1, Math.round(ms / 30));
      for (let i = 0; i < n; i++) { await page.mouse.wheel(0, dy / n); await page.waitForTimeout(30); }
    },
    async type(text, delay = 45) { await page.keyboard.type(text, { delay }); },
    async done(name) {
      await page.waitForTimeout(200);
      const tEnd = Date.now() / 1000;
      await cdp.send('Page.stopScreencast');
      await ctx.close();
      // Frames only arrive when something changes: hold each until the next one, 30 fps out.
      const tStart = frames[0]?.t ?? 0;
      const list = frames.map((f, i) => `file '${f.file}'\nduration ${((frames[i + 1]?.t ?? tEnd) - f.t).toFixed(4)}`).join('\n') + `\nfile '${frames.at(-1).file}'\n`;
      fs.writeFileSync(`${fdir}/list.txt`, list);
      const out = `${dir}/${name}.mp4`;
      execFileSync('ffmpeg', ['-loglevel', 'error', '-y', '-f', 'concat', '-safe', '0', '-i', `${fdir}/list.txt`, '-vf', `fps=30,scale=${w * dpr}:${h * dpr}:flags=lanczos,format=yuv420p`,
        '-c:v', 'libx264', '-crf', '14', '-preset', 'medium', '-g', '15', '-movflags', '+faststart', out]);
      // Mark times are counted from t0; the video starts at the first frame.
      const shift = t0 / 1000 - tStart;
      fs.writeFileSync(`${dir}/${name}.marks.json`, JSON.stringify(marks.map(m => ({ ...m, t: Math.round((m.t + shift) * 100) / 100 })), null, 1));
      fs.rmSync(fdir, { recursive: true, force: true });
      console.log(out);
      return out;
    },
  };
  return api;
}
