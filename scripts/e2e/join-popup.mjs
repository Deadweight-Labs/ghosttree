// Browser half of the join popup steps in join-order.sh (REQ-434).
//   node join-popup.mjs MODE SERVER_URL JAR FEEDFILE
// MODE: popup (second window reaches the installer, closes itself),
//       blocked (window.open refused: the code stays, it is pasted),
//       unreachable (the loopback cannot be reached: the code tab is unchanged).
// Prints one "ok: ..." or "FAIL: ..." line per check; exit 1 on any failure.
import { createRequire } from "node:module";
import { readFileSync, writeFileSync } from "node:fs";

const [mode, server, jar, feed] = process.argv.slice(2);
const { chromium } = createRequire(process.env.PW_FROM || import.meta.url)("playwright");

let failed = 0;
const check = (cond, what) => { console.log(`${cond ? "ok" : "FAIL"}: ${what}`); if (!cond) failed++; };

// curl cookie jar (Netscape format) -> Playwright cookies
const cookies = readFileSync(jar, "utf8").split("\n").filter((l) => l && (!l.startsWith("#") || l.startsWith("#HttpOnly_"))).map((l) => {
  const f = l.replace(/^#HttpOnly_/, "").split("\t");
  return { name: f[5], value: f[6], domain: f[0], path: f[2], httpOnly: l.startsWith("#HttpOnly_"), secure: f[3] === "TRUE" };
});

const browser = await chromium.launch({ executablePath: process.env.CHROME || undefined });
const ctx = await browser.newContext();
await ctx.addCookies(cookies);
const page = await ctx.newPage();
if (mode === "blocked") await page.addInitScript(() => { window.open = () => null; });
const port = new URL(server).port;
if (mode === "unreachable") await ctx.route((u) => u.hostname === "127.0.0.1" && u.port !== port, (r) => r.abort());
const navigations = [];
page.on("framenavigated", (f) => { if (f === page.mainFrame()) navigations.push(f.url()); });

await page.goto(server + "/join/pair");
await page.waitForSelector("text=wants to connect", { timeout: 30000 });
const before = page.url();
await page.check("input[name=confirm_account]");
const popupP = mode === "blocked" ? null : page.waitForEvent("popup", { timeout: 10000 }).catch(() => null);
await page.click("button[value=approve]");
const popup = popupP ? await popupP : null;
check(mode === "blocked" ? popup === null : popup !== null, mode === "blocked" ? "no second window when window.open is refused" : "a second window opened inside the click");

const codeEl = page.locator("#join-paste-code");
// A fast installer may finish before the page was looked at: then the code tab
// already says "connected" and the code was never needed.
await Promise.race([codeEl.waitFor({ timeout: 15000 }), page.waitForSelector("text=Continue in your terminal", { timeout: 15000 })]);
const code = (await codeEl.count()) ? (await codeEl.textContent()).trim() : "";
if (mode !== "popup") check(/^[A-Z0-9]{4}(-[A-Z0-9]{4}){4}$/.test(code), "the code tab shows the code");

if (mode === "popup") {
  const closed = popup.isClosed() || await popup.waitForEvent("close", { timeout: 20000 }).then(() => true, () => false);
  check(closed, "the second window closed itself after the installer finished" + (closed ? "" : " (stuck at " + popup.url() + ": " + (await popup.evaluate(() => document.body.innerText).catch((e) => String(e))) + ")"));
} else if (mode === "unreachable") {
  await new Promise((r) => setTimeout(r, 2500));
  check(popup && !popup.isClosed() && /^http:\/\/127\.0\.0\.1:\d+\/callback/.test(popup.url() === "about:blank" ? "" : popup.url()) || (popup && popup.url().startsWith("chrome-error")), "the second window shows the browser error, not the code tab");
}
if (mode !== "popup") {
  await new Promise((r) => setTimeout(r, 2500));
  writeFileSync(feed, code + "\n"); // what the person pastes into the terminal
}
await page.waitForSelector("text=Continue in your terminal", { timeout: 40000 }).then(() => check(true, "the code tab says to continue in the terminal"), () => check(false, "the code tab says to continue in the terminal"));
check(!(await page.locator("#join-paste-code").count()), "the code is gone from the page");
check(page.url() === before && navigations.every((u) => u.startsWith(server)), `the code tab never left ${new URL(before).pathname}`);
await browser.close();
process.exit(failed ? 1 : 0);
