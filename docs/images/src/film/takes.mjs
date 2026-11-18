// node takes.mjs <take> [<take> ...] [--dark] [--w=1280 --h=800]   (BASE, WORK, OUT from film/make.sh)
// Records each take of the films from the Northwind demo instance (2x, real clicks). Each take writes
// <take>.mp4 and <take>.marks.json (times and boxes in recording pixels) for gen.mjs.
// Order matters: "approve" changes the data that "brief" and "payoff" show, "workshop" decides DEC-1.
import { chromium, storage, scene, SF, PAY } from './lib.mjs';

const args = process.argv.slice(2);
const names = args.filter(a => !a.startsWith('--'));
const opt = Object.fromEntries(args.filter(a => a.startsWith('--')).map(a => { const [k, v] = a.slice(2).split('='); return [k, v ?? true]; }));
const scheme = opt.dark ? 'dark' : 'light';
const W = Number(opt.w || 1280), H = Number(opt.h || 720);
const dir = process.env.OUT || `rec/${scheme}-${W}x${H}`;

async function openSession(s, project, title) {
  await s.go(`${project}/sessions`);
  const href = await s.page.getByRole('link', { name: title }).first().getAttribute('href');
  return href;
}
// Scroll the element under the pointer (room timelines scroll inside their own box).
async function wheelAt(s, x, y, dy, ms) { await s.glide(x, y, 500); await s.scroll(dy, ms); }

const takes = {
  // The real failure inside the session that found the bug.
  async fail(s) {
    const href = await openSession(s, PAY, 'Duplicate receipts after a retry');
    await s.go(href + '?view=raw');
    await s.pause(500);
    await s.page.evaluate(() => window.scrollTo(0, 210));
    await s.pause(500);
    s.mark('start');
    await s.click(s.page.getByText('npm test -- receipts').first(), 900);
    s.mark('click-test');
    await s.pause(700);
    const b = await s.textBox('Expected 1 receipt, received 2');
    if (b && b.y > H - 120) { await s.scroll(b.y - H + 220, 700); }
    await s.pause(300);
    await s.toText('received 2', 800, 0.9);
    s.mark('on-failure');
    await s.note('fail-line', 'sends one receipt when callback and retry run at once');
    await s.note('output', 'FAIL  src/payments/receipts.test.ts');
    await s.note('prompt', 'Customers sometimes get two receipts after paying');
    await s.pause(2200);
  },
  // The agent says what it found; Theo widens it to refunds.
  async found(s) {
    await s.go(`${PAY}/rooms`);
    await s.pause(400);
    const b = await s.textBox('Found it: when the provider');
    if (b) await wheelAt(s, 700, 420, b.y - 220, 700);
    await s.pause(300);
    s.mark('start');
    await s.toText('Found it: when the provider answers slowly', 900, 0.3);
    await s.pause(1600);
    s.mark('theo');
    await s.toText('the fix must also cover refunds', 800, 0.6);
    await s.pause(2000);
  },
  // The agent's lesson waits for a person; Mia approves it.
  async approve(s) {
    await s.go(`${PAY}/review`);
    await s.pause(400);
    const row = s.page.locator('li, .row, article, div').filter({ hasText: 'Refunds send their receipt through the same path as payments' }).last();
    await row.scrollIntoViewIfNeeded();
    await s.page.evaluate(() => window.scrollBy(0, 160));
    await s.pause(500);
    s.mark('start');
    await s.toText('Refunds send their receipt through the same path as payments', 900, 0.4);
    await s.pause(900);
    // Open the card to show the full text.
    const t = await s.textBox('Refunds send their receipt through the same path as payments');
    const chevron = { x: 45, y: t.y + 20 };
    await s.glide(chevron.x, chevron.y, 600);
    await s.page.mouse.down(); await s.pause(80); await s.page.mouse.up();
    s.mark('expanded');
    await s.pause(1600);
    const t2 = await s.textBox('Refunds send their receipt through the same path as payments');
    const approve = s.page.getByRole('button', { name: 'Approve', exact: true });
    const boxes = await approve.evaluateAll(els => els.map(e => { const r = e.getBoundingClientRect(); return { x: r.x, y: r.y, w: r.width, h: r.height }; }));
    const near = boxes.filter(q => q.h > 0).sort((a, b) => Math.abs(a.y - t2.y) - Math.abs(b.y - t2.y))[0];
    await s.glide(near.x + near.w / 2, near.y + near.h / 2, 800);
    await s.pause(250);
    s.mark('click-approve');
    await s.page.mouse.down(); await s.pause(90); await s.page.mouse.up();
    await s.page.waitForLoadState('networkidle');
    await s.mask();
    await s.note('banner', 'Approved: Refunds send their receipt');
    await s.pause(1800);
    s.mark('end');
  },
  // The next session on payments starts with the lesson.
  async brief(s) {
    await s.go(`/ui/context?project=github.com%2Fnorthwind%2Fpayments`);
    await s.pause(600);
    s.mark('start');
    await s.toText('### pitfall', 800, 0.5);
    await s.pause(500);
    await s.toText('Refunds send their receipt through the same path as payments', 1000, 0.15);
    s.mark('on-line');
    await s.note('line', 'Refunds send their receipt through the same path as payments — A refund confirmation can arrive');
    await s.note('header', 'Session start');
    await s.pause(2600);
  },
  // Beside the code: the file description of receipt.go.
  async code(s) {
    await s.go(`${SF}/tree/main/checkout`);
    await s.pause(500);
    s.mark('start');
    await s.toText('Sends the receipt once', 900, 0.3);
    await s.pause(1000);
    await s.clickNav(s.page.getByRole('link', { name: 'receipt.go' }).first(), 700);
    s.mark('file');
    await s.pause(800);
    const b = await s.textBox('must not send a second one');
    if (b) await s.toText('must not send a second one', 900, 0.5);
    await s.pause(2000);
  },
  // Payoff: the refunds agent followed the lesson; its link opens the entry and where it came from.
  async payoff(s) {
    await s.go(`${PAY}/rooms`);
    await s.pause(600);
    s.mark('start');
    await s.toText('Partial refunds work', 900, 0.2);
    await s.note('msg', 'Partial refunds work. The refund receipt goes out once');
    await s.pause(1200);
    await s.toText('a repeated callback sends nothing', 800, 0.5);
    await s.pause(900);
    s.mark('to-link');
    await s.click(s.page.locator('span.ref[data-ref="KN-37"]').last(), 800);
    s.mark('card');
    await s.pause(2200);
    await s.go(`${PAY}/knowledge/37`);
    s.mark('entry');
    await s.note('kn-title', 'Refunds send their receipt through the same path as');
    await s.note('kn-confirmed', 'confirmed by Mia');
    await s.note('kn-session', 'Open session');
    await s.pause(1000);
    await s.toText('fix/double-receipts', 900, 0.5);
    await s.pause(1500);
  },
  // The pull request checklist: criteria with the commit that meets each.
  async checklist(s) {
    await s.go(`${SF}/pulls/17`);
    await s.pause(600);
    s.mark('start');
    await s.clickNav(s.page.getByRole('link', { name: /Checklist/ }).first(), 800);
    s.mark('list');
    await s.pause(700);
    await s.toText('Shoppers can pay without an account', 800, 0.4);
    await s.pause(700);
    await s.toText('A confirmation email arrives within a minute', 700, 0.4);
    await s.pause(700);
    await s.toText('Choose the shipping price tiers', 800, 0.4);
    await s.pause(1800);
  },
  // Several agents, two people, one room.
  async team(s) {
    await s.go(`${SF}/rooms`);
    await s.pause(600);
    s.mark('start');
    await wheelAt(s, 700, 400, -900, 1600);
    await s.pause(400);
    await wheelAt(s, 700, 400, 520, 1600);
    await s.pause(600);
    const tk = s.page.getByRole('button', { name: 'Take over' });
    if (await tk.count()) { await s.to(tk.first(), 900); }
    await s.pause(1800);
  },
  // The gardener re-read an entry against the code and says it no longer holds.
  async gardener(s) {
    await s.go(`${SF}/review`);
    await s.pause(500);
    s.mark('start');
    await s.clickNav(s.page.getByText('From the gardener').first(), 800);
    await s.pause(700);
    const t = await s.textBox('Deploys start when a version tag is pushed');
    await s.glide(45, t.y + 12, 800);
    await s.page.mouse.down(); await s.pause(80); await s.page.mouse.up();
    s.mark('expanded');
    await s.pause(800);
    const r = await s.textBox('the manual step is gone');
    if (r) await s.toText('the manual step is gone', 900, 0.5);
    await s.pause(2200);
  },
  // The workshop: an open decision with variants, pros and cons.
  async workshop(s) {
    await s.go(`${SF}/ideas/1`);
    await s.pause(500);
    await s.page.locator('text=Longer helps people who come back').first().evaluate(e => window.scrollTo(0, e.getBoundingClientRect().top + window.scrollY - 150));
    await s.pause(600);
    s.mark('start');
    await s.to(s.page.locator('text=Covers the weekend gap').first(), 900);
    await s.pause(500);
    await s.to(s.page.locator('text=Catches most returning shoppers').first(), 700);
    await s.pause(500);
    await s.to(s.page.locator('text=A second device cannot restore it').first(), 700);
    await s.pause(700);
    s.mark('reason');
    await s.click(s.page.getByPlaceholder('Why this one? (optional)').first(), 900);
    await s.pause(200);
    await s.type('Most shoppers come back within three days. A week covers it with the least data.', 32);
    await s.pause(400);
    s.mark('decide');
    await s.click(s.page.getByRole('button', { name: 'Decide' }).first(), 800);
    await s.page.waitForLoadState('networkidle');
    await s.mask();
    await s.pause(600);
    const d = s.page.locator('text=How long does a guest cart live on the server?').last();
    await d.evaluate(e => window.scrollTo(0, e.getBoundingClientRect().top + window.scrollY - 200)).catch(() => {});
    s.mark('decided');
    await s.note('saved', 'Decision saved');
    await s.note('chosen', 'Chosen');
    await s.pause(2500);
  },
  // A workflow reacted to a new idea and left a comment, within its budget.
  async workflows(s) {
    await s.go(`${SF}/workflows`);
    await s.pause(600);
    s.mark('start');
    await s.toText('Compares a new idea with the open ideas', 900, 0.4);
    await s.pause(800);
    await s.toText('both need a server-side list per shopper', 900, 0.4);
    await s.pause(900);
    await s.clickNav(s.page.getByRole('link', { name: /idea-match/ }).last(), 800);
    s.mark('run');
    await s.pause(2200);
  },
};

const browser = await chromium.launch();
const state = await storage(browser, scheme);
for (const name of names) {
  if (!takes[name]) { console.error('unknown take', name); continue; }
  const s = await scene(browser, state, { w: W, h: H, scheme, dir });
  try { await takes[name](s); } catch (e) { console.error(name, e.message); }
  await s.done(name);
}
await browser.close();
