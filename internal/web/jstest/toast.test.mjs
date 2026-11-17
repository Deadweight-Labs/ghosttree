// REQ-686 AC-4: the outcome of an action (.flash.fly) goes when the person moves on, and the
// query values that asked for it leave the address (static/ui-toast.js); the entry of the
// theme in the account menu names the theme that is on (static/shell.js).
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

test("the theme entry names the theme that is on and follows a click", () => {
  const menu = `<button data-theme-toggle><span data-theme-is="dark" hidden>Theme: Dark</span><span data-theme-is="light" hidden>Theme: Light</span></button>`;
  const shown = (p) => [...p.document.querySelectorAll("[data-theme-is]")].filter((el) => !el.hidden).map((el) => el.textContent);

  const light = loadPage(menu, { scripts: ["shell.js"] });
  assert.deepEqual(shown(light), ["Theme: Light"], "the system is light: the entry says so");
  light.document.querySelector("[data-theme-toggle]").dispatchEvent(new Event("click", { bubbles: true }));
  assert.deepEqual(shown(light), ["Theme: Dark"], "after the click the other theme is on");
  assert.equal(light.document.documentElement.dataset.theme, "dark");

  const dark = loadPage(menu, { scripts: ["shell.js"], hooks: { matchMedia: () => ({ matches: true }) } });
  assert.deepEqual(shown(dark), ["Theme: Dark"], "the system is dark: the entry says so");
});
