// REQ-711 (S20): a row of tabs that is too wide on a desktop folds its last tabs of the second group into a
// "More" menu; the open tab and a tab that wants attention stay; on a phone nothing is folded (the row scrolls).
import test from "node:test";
import assert from "node:assert/strict";
import { loadPage } from "./dom.mjs";

const tab = (key, label, cls = "") => `<li data-tab="${key}" class="${cls}"><a href="/${key}"><svg class="i"></svg><span>${label}</span><span class="counter">2</span></a></li>`;
const page = `<div class="utabs-box"><nav class="utabs-nav" data-fit><ul class="utabs">
${tab("code", "Code")}${tab("issues", "Issues")}<li class="tsep"></li>${tab("rooms", "Rooms", "aux att")}${tab("sessions", "Sessions", "aux")}${tab("tasks", "Tasks", "aux")}${tab("settings", "Settings", "aux sel")}${tab("packages", "Packages", "aux")}
</ul></nav><span class="utabs-fade l" hidden></span><span class="utabs-fade r" hidden><button data-more></button></span></div>`;

// The row is 5 tabs wide; every visible tab takes one unit, the room is `room` units.
function open(room, wide) {
  const p = loadPage(page, { scripts: [] });
  p.window.matchMedia = (q) => ({ matches: wide && /min-width: 768px/.test(q) });
  p.window.Element.prototype.getBoundingClientRect = () => ({ left: 0, right: 0, top: 0, bottom: 0, width: 0, height: 0 });
  const ul = p.document.querySelector(".utabs");
  const lis = () => [...ul.querySelectorAll("li")].filter((li) => !li.classList.contains("tsep"));
  Object.defineProperty(ul, "scrollWidth", { get: () => lis().filter((li) => !li.hidden).length });
  Object.defineProperty(ul, "clientWidth", { get: () => room });
  p.run("ui-tabs.js");
  return { p, ul, lis };
}

test("on a desktop the last tabs of the second group go into More until the row fits", () => {
  const { p, ul, lis } = open(6, true);
  const hidden = lis().filter((li) => li.hidden).map((li) => li.dataset.tab);
  assert.deepEqual(hidden.sort(), ["packages", "tasks"].sort(), "the tabs folded: from the end, one by one, the More entry itself takes room too");
  const menu = p.document.querySelector(".tmore .pop");
  assert.ok(menu, "the menu is there");
  assert.deepEqual([...menu.querySelectorAll("a.opt")].map((a) => a.getAttribute("href")).sort(), ["/packages", "/tasks"]);
});

test("the open tab and a tab with attention stay in the row", () => {
  const { lis } = open(3, true);
  const shown = lis().filter((li) => !li.hidden).map((li) => li.dataset.tab);
  assert.ok(shown.includes("settings") && shown.includes("rooms"), `shown: ${shown}`);
});

test("a row that fits has no menu", () => {
  const { p } = open(20, true);
  assert.equal(p.document.querySelector(".tmore"), null);
});

test("on a phone nothing is folded", () => {
  const { p, lis } = open(3, false);
  assert.equal(p.document.querySelector(".tmore"), null);
  assert.equal(lis().filter((li) => li.hidden).length, 0);
});
