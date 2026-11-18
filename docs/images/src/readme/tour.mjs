// node tour.mjs <base> <login-url> <outdir>
// Records the README tour as a video (webm) with a drawn mouse pointer: the
// decision in the idea workshop, the issue it leads to, the checklist of the
// pull request, the briefing of the next session, and the code tree.
import fs from 'node:fs';

// PLAYWRIGHT may point at a playwright index.mjs outside this repository.
const { chromium } = await import(process.env.PLAYWRIGHT || 'playwright');

const [base, loginURL, outdir] = process.argv.slice(2);
const P = '/ui/northwind/storefront';
const W = 1280, H = 720;
fs.mkdirSync(outdir, { recursive: true });

const browser = await chromium.launch();
// Sign in first in a context that does not record.
const login = await browser.newContext({ viewport: { width: W, height: H }, colorScheme: 'light', locale: 'en-US' });
const lp = await login.newPage();
await lp.goto(loginURL);
await lp.locator('main form button').first().click();
await lp.waitForLoadState('networkidle');
const state = await login.storageState();
await login.close();

const ctx = await browser.newContext({ viewport: { width: W, height: H }, colorScheme: 'light', locale: 'en-US', storageState: state,
  recordVideo: { dir: outdir, size: { width: W, height: H } } });
// A pointer drawn into the page, because headless Chromium draws none.
await ctx.addInitScript(() => {
  addEventListener('DOMContentLoaded', () => {
    const c = document.createElement('div');
    c.id = 'tour-cursor';
    c.innerHTML = '<svg width="22" height="22" viewBox="0 0 22 22"><path d="M3 2l14 8-6 1.6L8 18z" fill="#1f2328" stroke="#fff" stroke-width="1.5"/></svg>';
    Object.assign(c.style, { position: 'fixed', left: '0', top: '0', zIndex: 99999, pointerEvents: 'none', transform: `translate(${window.__cx || 640}px,${window.__cy || 360}px)` });
    document.body.appendChild(c);
    addEventListener('mousemove', (e) => { c.style.transform = `translate(${e.clientX}px,${e.clientY}px)`; });
  });
});
const page = await ctx.newPage();
let cx = 640, cy = 360;
const pause = (ms) => page.waitForTimeout(ms);
async function mask() {
  await page.evaluate(() => {
    const w = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    for (let n; (n = w.nextNode());) n.nodeValue = n.nodeValue.replace(/https?:\/\/127\.0\.0\.1:\d+/g, 'https://ghosttree.northwind.example');
  });
}
async function go(path) {
  await page.goto(base + path, { waitUntil: 'networkidle' });
  await mask();
  await page.mouse.move(cx, cy);
}
async function glide(x, y, steps = 30) {
  await page.mouse.move(x, y, { steps });
  cx = x; cy = y;
}
async function to(locator, steps = 30) {
  const b = await locator.first().boundingBox();
  if (b) await glide(Math.round(b.x + Math.min(b.width / 2, 60)), Math.round(b.y + b.height / 2), steps);
}
async function scroll(dy, ms = 1200) {
  const n = Math.max(1, Math.round(ms / 40));
  for (let i = 0; i < n; i++) { await page.mouse.wheel(0, dy / n); await pause(40); }
}

// 1. The decision in the workshop.
await go(P + '/ideas/1');
await pause(900);
await page.evaluate(() => { const e = document.querySelector('#workshop'); window.scrollTo({ top: e.getBoundingClientRect().top + scrollY - 70 }); });
await pause(1200);
await scroll(560, 1600);
await pause(500);
await to(page.locator('#workshop').getByText('7 days'));
await pause(700);
await to(page.locator('#workshop').getByText('30 days'));
await pause(500);
await to(page.locator('#workshop').getByText('Only until the link is used'));
await pause(900);

// 2. The issue with its criteria and evidence.
await go(P + '/issues/1');
await pause(900);
await to(page.getByText('Shoppers can pay without an account'));
await pause(700);
await scroll(260, 900);
await to(page.getByText('A confirmation email arrives within a minute'));
await pause(1100);

// 3. The checklist of the pull request.
await go(P + '/pulls/17');
await pause(700);
await to(page.getByRole('link', { name: /Checklist/ }).first());
await pause(300);
await page.getByRole('link', { name: /Checklist/ }).first().click();
await page.waitForLoadState('networkidle');
await mask();
await pause(1000);
await to(page.getByText('Choose the shipping price tiers'));
await pause(1300);

// 4. The briefing of the next session.
await go('/ui/context?project=github.com%2Fnorthwind%2Fstorefront');
await pause(900);
await to(page.getByText('A payment retry must not send a second receipt').first());
await pause(1400);
await scroll(420, 1400);
await pause(800);

// 5. The code tree with file descriptions.
await go(P);
await pause(800);
await to(page.getByRole('link', { name: 'checkout', exact: false }).first());
await pause(300);
await page.getByRole('link', { name: 'checkout', exact: false }).first().click();
await page.waitForLoadState('networkidle');
await mask();
await pause(800);
await to(page.getByText('receipt.go').first());
await pause(1800);

const video = page.video();
await ctx.close();
await browser.close();
fs.renameSync(await video.path(), `${outdir}/tour.webm`);
console.log(`${outdir}/tour.webm`);
