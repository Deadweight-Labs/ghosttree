// REQ-689 AC-4 (P17, N13): a question answered with a proposal leaves "Waiting for you" as it does after a
// reload, the numbers of the box and the filters go down with it, and a line says what it was answered with.
import test from "node:test";
import assert from "node:assert/strict";
import { loadPage } from "./dom.mjs";

const PAGE = `<main class="ib"><div class="wrap">
<div class="ntools"><nav class="ifilter"><a data-f="alle">All<span class="counter">2</span></a><a data-f="fragen">Questions<span class="counter">1</span></a><a data-f="aufgaben">Tasks<span class="counter">1</span></a></nav></div>
<section class="box" id="waiting"><div class="box-h"><h2 class="t">Waiting for you</h2><span class="counter">2</span></div>
<div class="row nrow" data-r="q" data-cat="fragen "><div class="nb"><div class="t">Wie lange soll das Timeout sein?</div>
<div class="opts" data-answered="Answered:"><form class="orow" method="post" action="/ui/inbox/answer" data-quick><button class="gbtn" data-text="Nur Zeitfenster">1</button></form></div></div></div>
<div class="row nrow" data-r="t" data-cat="aufgaben "><div class="nb"><div class="t">Secret ablegen</div></div></div>
</section></div></main>`;

async function answer() {
  const sent = [];
  const hooks = {
    fetch: async (...args) => { sent.push(args); return { ok: true, json: async () => ({ option: { text: "Nur Zeitfenster (5 Min)" } }) }; },
    FormData: class { constructor() {} [Symbol.iterator]() { return [["option", "A"], ["next", "1"]][Symbol.iterator](); } },
  };
  const p = loadPage(PAGE, { hooks });
  p.run("ui-inbox.js");
  const form = p.document.querySelector("form[data-quick]");
  form.dispatchEvent(new p.window.Event("submit", { bubbles: true, cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 10));
  return { p, sent };
}

test("an answered question leaves the list and the numbers follow the list", async () => {
  const { p, sent } = await answer();
  assert.equal(sent.length, 1, "the answer went to the server in the background");
  assert.equal(p.document.querySelector('[data-r="q"]'), null, "the answered question is gone from the list");
  assert.ok(p.document.querySelector('[data-r="t"]'), "the other row stays");
  assert.equal(p.document.querySelector(".box-h .counter").textContent, "1");
  assert.equal(p.document.querySelector('[data-f="alle"] .counter').textContent, "1");
  assert.equal(p.document.querySelector('[data-f="fragen"] .counter').textContent, "0");
  assert.equal(p.document.querySelector('[data-f="aufgaben"] .counter').textContent, "1", "a tab the question was not in keeps its number");
});

test("the line after the answer says what it was answered with", async () => {
  const { p } = await answer();
  const note = p.document.querySelector("[data-answered-note]");
  assert.ok(note, "there is a line");
  assert.equal(note.textContent.trim(), "Answered: Nur Zeitfenster (5 Min)");
  assert.equal(note.getAttribute("role"), "status");
});
