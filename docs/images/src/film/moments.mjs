// node moments.mjs [<moment> ...]   (BASE, WORK, OUT from film/make.sh; PLAYWRIGHT if Node does not find it)
// Stills of single moments for the split films (gen.mjs, look "word | detail"): the real interface at phone width
// (360 CSS px) and 4x device pixels, without a pointer. Each moment writes <name>.png (a band of the page around the
// element) and <name>.json: the band's size, the focus box and the highlight boxes, all in image pixels.
// Order matters: "checked" approves KN-37, which "told", "once" and "traced" then show as confirmed.
import fs from 'node:fs';
import { chromium, loginLink, base, SF, PAY } from './lib.mjs';

const W = 360, DPR = 4;
const out = process.env.OUT || 'moments';
fs.mkdirSync(out, { recursive: true });

const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: W, height: 800 }, deviceScaleFactor: DPR, locale: 'en-US', timezoneId: 'Europe/Berlin' });
const page = await ctx.newPage();
await page.goto(loginLink('Mia'));
await Promise.all([page.waitForURL(u => !u.href.includes('/login'), { timeout: 15000 }), page.locator('main form button').first().click()]);

async function go(path) {
  await page.goto(base + path, { waitUntil: 'load' });
  await page.waitForTimeout(600);
  await page.evaluate(() => {
    const tw = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    for (let n; (n = tw.nextNode());) n.nodeValue = n.nodeValue.replace(/https?:\/\/127\.0\.0\.1:\d+/g, 'https://ghosttree.northwind.example');
  });
}

// Page box (CSS px, document coordinates) of the first occurrence of a text; lines = one rect per line.
async function textBox(str, nth = 0) {
  return page.evaluate(([s, k]) => {
    const tw = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    let all = '', nodes = [];
    for (let n; (n = tw.nextNode());) { nodes.push([n, all.length]); all += n.nodeValue; }
    const at = (i) => { let j = nodes.length - 1; while (nodes[j][1] > i) j--; return [nodes[j][0], i - nodes[j][1]]; };
    let seen = 0;
    for (let i = all.indexOf(s); i >= 0; i = all.indexOf(s, i + 1)) {
      const r = document.createRange();
      r.setStart(...at(i)); r.setEnd(...at(i + s.length));
      const rects = [...r.getClientRects()].filter(q => q.width > 0 && q.height > 0);
      if (!rects.length || seen++ < k) continue;
      const sy = window.scrollY, sx = window.scrollX;
      const lines = [];
      for (const q of rects) {
        const last = lines.at(-1);
        if (last && Math.abs(last.y - (q.y + sy)) < 4) { last.w = q.x + sx + q.width - last.x; last.h = Math.max(last.h, q.height); }
        else lines.push({ x: q.x + sx, y: q.y + sy, w: q.width, h: q.height });
      }
      const x = Math.min(...lines.map(l => l.x)), y = lines[0].y;
      return { x, y, w: Math.max(...lines.map(l => l.x + l.w)) - x, h: lines.at(-1).y + lines.at(-1).h - y, lines };
    }
    return null;
  }, [str, nth]);
}
async function elBox(locator) {
  return locator.first().evaluate(e => { const r = e.getBoundingClientRect(); return { x: r.x + scrollX, y: r.y + scrollY, w: r.width, h: r.height }; });
}

// Write the band [top, top+height] of the page (CSS px) and the boxes relative to it, in image pixels.
async function shoot(name, { focus, hl = [], alt = {}, above = 420, below = 900 }) {
  const doc = await page.evaluate(() => document.documentElement.scrollHeight);
  const top = Math.max(0, Math.floor(focus.y - above));
  const height = Math.min(doc - top, Math.ceil(focus.y + focus.h + below) - top);
  await page.screenshot({ path: `${out}/${name}.png`, fullPage: true, clip: { x: 0, y: top, width: W, height } });
  const px = (b) => ({ x: Math.round(b.x * DPR), y: Math.round((b.y - top) * DPR), w: Math.round(b.w * DPR), h: Math.round(b.h * DPR) });
  // alt: other framings of the same still (gen picks one per format), e.g. only the text inside a box.
  const meta = { w: W * DPR, h: Math.round(height * DPR), dpr: DPR, focus: px(focus), alt: Object.fromEntries(Object.entries(alt).map(([k, b]) => [k, px(b)])),
    hl: hl.map(b => ({ ...px(b), lines: (b.lines || [b]).map(px) })) };
  fs.writeFileSync(`${out}/${name}.json`, JSON.stringify(meta, null, 1));
  console.log(name, meta.w + 'x' + meta.h);
}
const union = (...bs) => {
  const x = Math.min(...bs.map(b => b.x)), y = Math.min(...bs.map(b => b.y));
  return { x, y, w: Math.max(...bs.map(b => b.x + b.w)) - x, h: Math.max(...bs.map(b => b.y + b.h)) - y };
};

const moments = {
  // The failing test inside the session that found the bug.
  async twice() {
    await go(`${PAY}/sessions`);
    const href = await page.getByRole('link', { name: 'Duplicate receipts after a retry' }).first().getAttribute('href');
    await go(href + '?view=raw');
    await page.getByText('npm test -- receipts').first().click();
    await page.waitForTimeout(700);
    const line = await textBox('Expected 1 receipt, received 2');
    const hit = await textBox('received 2');
    await shoot('twice', { focus: line, hl: [hit] });
  },
  // The agent's lesson, written down and waiting for a person.
  async learned() {
    await go(`${PAY}/knowledge/37`);
    const title = await elBox(page.locator('h1'));
    const waiting = await textBox('Waiting for review');
    const lesson = await textBox('Refunds send their receipt through the same path as payments');
    await shoot('learned', { focus: union({ ...title, w: lesson.w + lesson.x - title.x }, waiting), hl: [lesson] });
  },
  // Mia approves it.
  async checked() {
    await go(`${PAY}/knowledge/37`);
    await Promise.all([page.waitForLoadState('load'), page.getByRole('button', { name: 'Approve', exact: true }).first().click()]);
    await page.waitForTimeout(900);
    // The notice that confirms the approval, with its own green box around the text.
    const text = await textBox('Approved: Refunds send their receipt');
    const box = await page.evaluate(([x, y]) => {
      let e = document.elementFromPoint(x - scrollX + 2, y - scrollY + 2);
      while (e && getComputedStyle(e).backgroundColor.match(/rgba\(0, 0, 0, 0\)|transparent/)) e = e.parentElement;
      const r = e.getBoundingClientRect();
      return { x: r.x + scrollX, y: r.y + scrollY, w: r.width, h: r.height };
    }, [text.x, text.y]);
    const word = await textBox('Approved');
    const full = await textBox('Approved: Refunds send their receipt through the same path as payments') || text;
    await shoot('checked', { focus: box, hl: [word], alt: { text: full } });
  },
  // The next session on payments starts with it.
  async told() {
    await go('/ui/context?project=github.com%2Fnorthwind%2Fpayments');
    const line = await textBox('Refunds send their receipt through the same path as payments');
    const head = await textBox('### pitfall');
    await shoot('told', { focus: union(head, line), hl: [line] });
  },
  // The refunds agent does it once and says why.
  async once() {
    await go(`${PAY}/rooms`);
    const msg = await textBox('Partial refunds work.');
    const card = await page.evaluate(([x, y]) => {
      let e = document.elementFromPoint(x - scrollX + 4, y - scrollY + 4);
      // Up to the message with its author line (avatar, name, time).
      while (e && !(e.textContent.includes('Claude') && e.getBoundingClientRect().height < 700)) e = e.parentElement;
      const r = e.getBoundingClientRect();
      return { x: r.x + scrollX, y: r.y + scrollY, w: r.width, h: r.height };
    }, [msg.x, msg.y]);
    const once = await textBox('goes out once');
    // The avatar sits left of the message box.
    const avatar = await page.evaluate(([x, y]) => {
      for (let dx = 8; dx < 80; dx += 4) {
        const e = document.elementFromPoint(x - scrollX - dx, y - scrollY + 16);
        if (e && (e.tagName === 'IMG' || e.tagName === 'svg' || e.closest('svg, img, .avatar'))) {
          const r = (e.closest('.avatar') || e).getBoundingClientRect();
          return { x: r.x + scrollX, y: r.y + scrollY, w: r.width, h: r.height };
        }
      }
      return null;
    }, [card.x, card.y]);
    await shoot('once', { focus: avatar ? union(card, avatar) : card, hl: [once] });
  },
  // Where the lesson came from: commit, branch, the person who confirmed it.
  async traced() {
    await go(`${PAY}/knowledge/37`);
    const title = await elBox(page.locator('h1'));
    const branch = await textBox('fix/double-receipts');
    await shoot('traced', { focus: union(title, branch), hl: [branch] });
  },
  // Beside the code: the description of receipt.go.
  async beside() {
    await go(`${SF}/tree/main/checkout`);
    await page.getByRole('link', { name: 'receipt.go' }).first().click();
    await page.waitForLoadState('load');
    await page.waitForTimeout(700);
    const t = await textBox('must not send a second one');
    await shoot('beside', { focus: t, hl: t ? [t] : [] });
  },
};

const names = process.argv.slice(2).length ? process.argv.slice(2) : Object.keys(moments);
for (const n of names) {
  try { await moments[n](); } catch (e) { console.error(n, e.message); process.exitCode = 1; }
}
await browser.close();
