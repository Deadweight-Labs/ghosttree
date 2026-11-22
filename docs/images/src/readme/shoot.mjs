// node shoot.mjs <base> <login-url> <outdir> [names...]
// Takes the README pictures from a throwaway demo instance: light, 1280 px wide.
import fs from 'node:fs';

// PLAYWRIGHT may point at a playwright index.mjs outside this repository.
const { chromium } = await import(process.env.PLAYWRIGHT || 'playwright');

const [base, loginURL, outdir, ...only] = process.argv.slice(2);
const P = '/ui/northwind/storefront';
const screens = {
  'ui-code':      { path: P, height: 700 },
  'ui-issue':     { path: P + '/issues/1', height: 900 },
  'ui-pr':        { path: P + '/pulls/17?tab=checklist', height: 820 },
  'ui-idea':      { path: P + '/ideas/1', el: '#workshop', height: 1600 },
  'ui-room':      { path: '/ui/northwind/payments/rooms', height: 1000 },
  'ui-inbox':     { path: '/ui/inbox', height: 820 },
  'ui-workflows': { path: P + '/workflows', height: 520 },
  'ui-context':   { path: '/ui/context?project=github.com%2Fnorthwind%2Fstorefront', height: 900 },
  'ui-knowledge': { path: P + '/knowledge', height: 820 },
  'ui-runbook':   { path: P + '/runbooks', height: 700 },
};
const scheme = process.env.SCHEME || 'light';
const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 }, deviceScaleFactor: Number(process.env.DPR || 1), colorScheme: scheme, locale: 'en-US', timezoneId: 'Europe/Berlin' });
const page = await ctx.newPage();
await page.goto(loginURL);
await page.locator('main form button').first().click();
await page.waitForLoadState('networkidle');
fs.mkdirSync(outdir, { recursive: true });
for (const [name, s] of Object.entries(screens)) {
  if (only.length && !only.includes(name)) continue;
  await page.setViewportSize({ width: 1280, height: s.height });
  await page.goto(base + s.path, { waitUntil: 'networkidle' });
  // Mask the address of the throwaway instance wherever it shows.
  await page.evaluate(() => {
    const w = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    for (let n; (n = w.nextNode());) n.nodeValue = n.nodeValue.replace(/https?:\/\/127\.0\.0\.1:\d+/g, 'https://ghosttree.northwind.example');
  });
  if (s.scrollTo) await page.evaluate((sel) => { const e = document.querySelector(sel); window.scrollTo(0, e.getBoundingClientRect().bottom + window.scrollY - 700); }, s.scrollTo);
  await page.waitForTimeout(400);
  const file = `${outdir}/${name}.png`;
  if (s.el) await page.locator(s.el).screenshot({ path: file });
  else await page.screenshot({ path: file });
  console.log(file);
}
await browser.close();
