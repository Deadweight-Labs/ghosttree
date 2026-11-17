// REQ-688 AC-4 (N06): "Send as an ask" needs somebody to ask. A name written with @ in the words is
// somebody, so the form goes to the server, which resolves it; with neither a choice under "To" nor a
// name, the choice itself is marked and the form is not sent.
import test from "node:test";
import assert from "node:assert/strict";
import { loadPage } from "./dom.mjs";

const PAGE = `<div class="coord-workspace" data-coord-texts='{"need_to":"A question needs someone to answer it."}'>
<form class="coord-composer" method="post" action="/ui/coord/send"><textarea name="body"></textarea>
<p data-coord-action-status hidden></p>
<div class="cx-f"><div class="dd" data-pick><button type="button" class="gbtn sm" data-dd aria-expanded="false">To</button>
<div class="pop"><button type="button" class="opt" data-val="" data-label="Everyone">Everyone</button></div><input type="hidden" name="mentions" value="" data-pick-input></div>
<button class="gbtn rm-ask" name="intent" value="question" data-needs-to>Send as an ask</button></div></form></div>`;

async function load(words) {
  const sent = [];
  // The form is not posted for real: what matters is whether the script lets it go.
  const hooks = { fetch: (...args) => { sent.push(args); return new Promise(() => {}); }, FormData: class { constructor() { this.fields = []; } append() {} get() { return null; } entries() { return []; } [Symbol.iterator]() { return [][Symbol.iterator](); } } };
  const p = loadPage(PAGE, { hooks });
  p.window.HTMLTextAreaElement = p.document.querySelector("textarea").constructor;
  p.window.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  p.run("ui-coord.js");
  const form = p.document.querySelector("form");
  form.querySelector("textarea").value = words;
  const submit = new p.window.Event("submit", { bubbles: true, cancelable: true });
  submit.submitter = form.querySelector("[data-needs-to]");
  form.dispatchEvent(submit);
  await new Promise((resolve) => setTimeout(resolve, 5));
  return { p, form, submit, status: form.querySelector("[data-coord-action-status]"), pick: form.querySelector("[data-pick]"), sent };
}

test("an ask without anyone chosen and without a name marks the choice and says what is missing", async () => {
  const { submit, status, pick, sent } = await load("Wie lange soll das Timeout sein?");
  assert.equal(sent.length, 0, "nothing was sent");
  assert.equal(submit.defaultPrevented, true);
  assert.equal(pick.classList.contains("invalid"), true, "the To menu is marked");
  assert.match(status.textContent, /needs someone/);
});

test("an ask with an @name in the words is not held back: the server reads the name", async () => {
  const { pick, status, sent } = await load("@Mara wie lange soll das Timeout sein?");
  assert.equal(pick.classList.contains("invalid"), false);
  assert.doesNotMatch(status.textContent, /needs someone/);
});
