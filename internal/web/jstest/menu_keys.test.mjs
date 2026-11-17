// REQ-690 AC-3 (R04): a menu (div.dd with a button and a list of entries, as ui-select.js builds it
// from a select) works by keyboard: arrows open it and move between the entries, Home and End jump,
// the page does not scroll along, and the entry in focus is chosen with Enter (it is a button).
import test from "node:test";
import assert from "node:assert/strict";
import { loadPage } from "./dom.mjs";

const menu = (n, chosen = -1) => {
  const opts = [];
  for (let i = 0; i < n; i++) {
    opts.push(`<button type="button" class="opt" role="option" data-val="l${i}" aria-selected="${i === chosen}">label ${i}</button>`);
  }
  return `<form class="lbadd"><div class="dd selx"><button type="button" class="gbtn" data-dd aria-expanded="false" aria-haspopup="listbox">Choose a label …</button>
<div class="pop" role="listbox">${opts.join("")}</div></div><button class="gbtn" data-needs-pick>Add</button></form>`;
};

function page(html) {
  const p = loadPage(html, { scripts: [] });
  // The small DOM has no layout: a menu that needs no moving, and prepend for the filter field.
  const proto = p.window.Element.prototype;
  proto.getBoundingClientRect = () => ({ left: 0, right: 10, top: 0, bottom: 10 });
  proto.prepend = function prepend(node) { this.insertBefore(node, this.childNodes[0] || null); };
  p.window.GTMarkdown = { render: () => p.document.createElement("div") };
  p.run("ui-parts.js");
  return p;
}

test("the arrow keys open a closed menu and put the focus on its first entry", () => {
  const p = page(menu(3));
  const button = p.document.querySelector("[data-dd]");
  button.focus();
  const ev = p.key("ArrowDown");
  assert.equal(ev.defaultPrevented, true, "the page must not scroll");
  assert.ok(p.document.querySelector(".dd").classList.contains("open"));
  assert.equal(p.document.activeElement.dataset.val, "l0");
});

test("a menu with a chosen entry opens on that entry", () => {
  const p = page(menu(4, 2));
  p.document.querySelector("[data-dd]").focus();
  p.key("ArrowUp");
  assert.equal(p.document.activeElement.dataset.val, "l2");
});

test("arrows, Home and End move between the entries and stop at the ends", () => {
  const p = page(menu(4));
  p.document.querySelector("[data-dd]").focus();
  p.key("ArrowDown");
  const at = () => p.document.activeElement.dataset.val;
  p.key("ArrowDown");
  p.key("ArrowDown");
  assert.equal(at(), "l2");
  p.key("End");
  assert.equal(at(), "l3");
  assert.equal(p.key("ArrowDown").defaultPrevented, true, "at the end the page still does not scroll");
  assert.equal(at(), "l3");
  p.key("ArrowUp");
  assert.equal(at(), "l2");
  p.key("Home");
  assert.equal(at(), "l0");
  p.key("ArrowUp");
  assert.equal(at(), "l0");
});

test("Home and End on a closed menu are left to the page", () => {
  const p = page(menu(3));
  p.document.querySelector("[data-dd]").focus();
  assert.equal(p.key("Home").defaultPrevented, false);
  assert.equal(p.key("End").defaultPrevented, false);
  assert.ok(!p.document.querySelector(".dd").classList.contains("open"));
});

test("a long menu starts in its filter field and the arrows leave it for the entries", () => {
  const p = page(menu(12));
  p.document.querySelector("[data-dd]").focus();
  p.key("ArrowDown");
  const find = p.document.querySelector(".pf");
  assert.ok(find, "a menu of more than ten entries has a filter field");
  assert.equal(p.document.activeElement, find);
  assert.equal(p.key("Home", {}, find).defaultPrevented, false, "Home moves the cursor in the text");
  p.key("ArrowDown", {}, find);
  assert.equal(p.document.activeElement.dataset.val, "l0");
});

test("Escape closes the menu and gives the focus back to its button", () => {
  const p = page(menu(3));
  const button = p.document.querySelector("[data-dd]");
  button.focus();
  p.key("ArrowDown");
  p.key("Escape");
  assert.ok(!p.document.querySelector(".dd").classList.contains("open"));
  assert.equal(p.document.activeElement, button);
});

test("a chord with a modifier key is none of the menu's", () => {
  const p = page(menu(3));
  p.document.querySelector("[data-dd]").focus();
  const ev = p.key("ArrowDown", { altKey: true });
  assert.equal(ev.defaultPrevented, false);
  assert.ok(!p.document.querySelector(".dd").classList.contains("open"));
});
