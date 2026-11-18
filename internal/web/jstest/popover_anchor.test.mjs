// REQ-711 (P41): a confirmation popover (details.cfm .cfp) is placed like the menus: it opens above its button when
// there is no room below, and a caret points at the middle of the button wherever the box ended up.
import test from "node:test";
import assert from "node:assert/strict";
import { loadPage, Event } from "./dom.mjs";

const html = `<div class="row"><details class="cfm"><summary class="gbtn">Remove</summary><div class="cfp"><p>Remove Gast?</p></div></details></div>`;

function open(rects, innerHeight) {
  const p = loadPage(html, { scripts: [] });
  p.window.innerHeight = innerHeight;
  p.window.Element.prototype.getBoundingClientRect = function rect() {
    const key = this.tagName === "SUMMARY" || this.tagName === "DETAILS" ? "button" : this.classList.contains("cfp") ? "pop" : "none";
    return rects[key] || { left: 0, right: 0, top: 0, bottom: 0, width: 0, height: 0 };
  };
  p.window.Element.prototype.prepend = function prepend(node) { this.insertBefore(node, this.childNodes[0] || null); };
  p.run("ui-parts.js");
  const details = p.document.querySelector("details.cfm");
  details.open = true;
  details.dispatchEvent(new Event("toggle", { bubbles: true }));
  return { p, pop: p.document.querySelector(".cfp") };
}

test("with no room below the popover opens above its button and says so", () => {
  const { pop } = open({ button: { left: 340, right: 400, top: 280, bottom: 310, width: 60 }, pop: { left: 100, right: 380, top: 314, bottom: 440, width: 280 } }, 400);
  assert.equal(pop.getAttribute("data-side"), "up");
});

test("the caret points at the middle of the button, inside the box", () => {
  const { pop } = open({ button: { left: 340, right: 400, top: 100, bottom: 130, width: 60 }, pop: { left: 120, right: 400, top: 134, bottom: 250, width: 280 } }, 900);
  assert.equal(pop.style["--caret-x"], "250px", "370 - 120");
  assert.equal(pop.getAttribute("data-side"), null, "room below: it stays below");
});
