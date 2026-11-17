// REQ-686 AC-4: the outcome of an action (.flash.fly) goes when the person moves on, and the
// query values that asked for it leave the address (static/ui-toast.js); the entry of the
// theme in the account menu is a choice of light, dark and system (static/shell.js).
import test from "node:test";
import assert from "node:assert/strict";
import { loadPage, Event } from "./dom.mjs";

const html = `<div class="flash fly" role="status"><div>Project created.</div></div>
<nav><a id="tab" href="#members">Members</a><a id="out" href="/ui/inbox">Inbox</a></nav>`;
const open = (url = "http://localhost/ui/orgs?org=alpha&notice=project_created&pick=7#projects") => loadPage(html, { url, scripts: ["ui-toast.js"] });
const toasts = (p) => p.document.querySelectorAll(".flash.fly").length;
const click = (p, id) => p.document.getElementById(id).dispatchEvent(new Event("click", { bubbles: true, cancelable: true }));

test("the address no longer asks for the toast once it is shown, and keeps the rest", () => {
  const p = open();
  assert.equal(toasts(p), 1, "the toast is there");
  assert.equal(p.path(), "/ui/orgs?org=alpha&pick=7");
});

test("a page without a toast keeps its address as it is", () => {
  const p = loadPage(`<a href="/x">x</a>`, { url: "http://localhost/ui/orgs?notice=project_created", scripts: ["ui-toast.js"] });
  assert.equal(p.path(), "/ui/orgs?notice=project_created", "the query belongs to whoever reads it");
});

test("moving to another tab of the page takes the toast away", () => {
  const p = open();
  click(p, "tab");
  assert.equal(toasts(p), 0);
});

test("a link to another page takes the toast away", () => {
  const p = open();
  click(p, "out");
  assert.equal(toasts(p), 0);
});

test("a changed #address takes the toast away", () => {
  const p = open();
  for (const fn of p.document.windowListeners.get("hashchange") || []) fn({ type: "hashchange" });
  assert.equal(toasts(p), 0);
});

test("coming back from the history does not bring the toast back", () => {
  const p = open();
  const restore = new Event("pageshow");
  restore.persisted = true;
  for (const fn of p.document.windowListeners.get("pageshow") || []) fn(restore);
  assert.equal(toasts(p), 0);
});

test("a click inside the toast keeps it", () => {
  const p = loadPage(`<div class="flash fly"><a id="in" href="/ui/inbox">Open</a></div>`, { scripts: ["ui-toast.js"] });
  click(p, "in");
  assert.equal(toasts(p), 1);
});

// REQ-690 AC-4 (N24): the theme is a choice of light, dark and system, not a switch that names the state.
test("the theme is a choice of three: the one that is on is marked, a click sets it, system lets go of it", () => {
  const menu = `<div role="radiogroup"><button role="radio" data-theme-set="light">Light</button><button role="radio" data-theme-set="dark">Dark</button><button role="radio" data-theme-set="system">System</button></div>`;
  const checked = (p) => [...p.document.querySelectorAll("[data-theme-set]")].filter((b) => b.getAttribute("aria-checked") === "true").map((b) => b.dataset.themeSet);
  const click = (p, which) => p.document.querySelector(`[data-theme-set="${which}"]`).dispatchEvent(new Event("click", { bubbles: true }));

  const p = loadPage(menu, { scripts: ["shell.js"] });
  assert.deepEqual(checked(p), ["system"], "without a choice the page follows the system");
  click(p, "dark");
  assert.deepEqual(checked(p), ["dark"]);
  assert.equal(p.document.documentElement.dataset.theme, "dark");
  assert.equal(p.window.localStorage.getItem("gt-theme"), "dark");
  click(p, "light");
  assert.deepEqual(checked(p), ["light"]);
  assert.equal(p.document.documentElement.dataset.theme, "light");
  click(p, "system");
  assert.deepEqual(checked(p), ["system"]);
  assert.equal(p.document.documentElement.dataset.theme, undefined, "system leaves the choice to the browser");
  assert.equal(p.window.localStorage.getItem("gt-theme"), null);
});

test("a stored choice is marked when the page opens", () => {
  const menu = `<button data-theme-set="light">Light</button><button data-theme-set="dark">Dark</button><button data-theme-set="system">System</button>`;
  const p = loadPage(menu, { scripts: [], hooks: {} });
  p.window.localStorage.setItem("gt-theme", "dark");
  p.run("shell.js");
  const on = [...p.document.querySelectorAll("[data-theme-set]")].filter((b) => b.getAttribute("aria-checked") === "true").map((b) => b.dataset.themeSet);
  assert.deepEqual(on, ["dark"]);
  assert.equal(p.document.documentElement.dataset.theme, "dark");
});

// REQ-690 AC-4 (N27): the toast floats under the header and the tab band, not over the tabs.
test("the toast is placed under the header and the tab band of the page", () => {
  const page = `<header class="gh-header">head</header><div class="rband">tabs</div><div class="flash fly" role="status">Saved.</div>`;
  const bottoms = { "gh-header": 56, rband: 112 };
  const p = loadPage(page, { scripts: [] });
  p.window.Element.prototype.getBoundingClientRect = function rect() {
    for (const [cls, bottom] of Object.entries(bottoms)) if (this.classList.contains(cls)) return { bottom };
    return { bottom: 0 };
  };
  p.run("ui-toast.js");
  assert.equal(p.document.querySelector(".flash.fly").style.top, "120px", "8 px under the band");
});

test("a page without a tab band puts the toast under the header", () => {
  const p = loadPage(`<header class="gh-header">head</header><div class="flash fly">Saved.</div>`, { scripts: [] });
  p.window.Element.prototype.getBoundingClientRect = function rect() { return { bottom: this.classList.contains("gh-header") ? 56 : 0 }; };
  p.run("ui-toast.js");
  assert.equal(p.document.querySelector(".flash.fly").style.top, "64px");
});
