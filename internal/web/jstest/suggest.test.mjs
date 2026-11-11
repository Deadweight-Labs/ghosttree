// REQ-628 AC-1 and REQ-599 AC-10: the list under the search field and the key that
// jumps into it (static/ui-suggest.js). The answers are rows the way
// /ui/search/suggest sends them (templates/search.html, "searchsuggest").
import test from "node:test";
import assert from "node:assert/strict";
import { loadPage, Event } from "./dom.mjs";

const FRAME = `
<form class="hsf" role="search" method="get" action="/ui/search"><label class="gfield hsearch"><input id="shell-search" type="search" name="q" aria-label="Search everything" data-suggest="/ui/search/suggest" data-suggest-project="github.com/acme/shell"></label></form>
<textarea class="comp-in"></textarea>`;

const hit = `<li role="option" class="sg"><a class="sg-a" href="/ui/acme/shell/issues/7" tabindex="-1"><span class="sg-t">Fix the deploy</span></a></li>`;
const qual = (insert) => `<li role="option" class="sg sg-q" data-insert="${insert}"><span class="sg-k">${insert}</span></li>`;
const all = (q) => `<li role="option" class="sg sg-all"><a class="sg-a" href="/ui/search?q=${q}" tabindex="-1"><span class="sg-t">Search for ${q}</span></a></li>`;

const tick = () => new Promise((resolve) => setImmediate(resolve));

// page loads the script with a fetch that answers from answer(url) and a timer
// that runs at once, so that the wait before a request does not slow the test.
function page(answer = () => "") {
  const calls = [];
  const fetch = (url) => {
    calls.push(url);
    return Promise.resolve({ ok: true, text: () => Promise.resolve(answer(url)) });
  };
  const hooks = { fetch, setTimeout: (fn) => { fn(); return 1; }, clearTimeout: () => {} };
  const p = loadPage(FRAME, { url: "http://localhost/ui/acme/shell", scripts: ["ui-suggest.js"], hooks });
  const input = p.document.getElementById("shell-search");
  const list = p.document.querySelector("ul.sg-list");
  const rows = () => [...list.querySelectorAll("li.sg")];
  const type = async (text) => {
    input.focus();
    input.value = text;
    input.dispatchEvent(new Event("input", { bubbles: true }));
    await tick();
  };
  return { ...p, calls, input, list, rows, type };
}

test("Ctrl+K and Cmd+K jump into the field of the header, and the page's own editors keep their key", () => {
  const p = page();
  const ev = p.key("k", { ctrlKey: true });
  assert.equal(ev.defaultPrevented, true, "the browser's own Ctrl+K (the address bar) is not run");
  assert.equal(p.document.activeElement, p.input);
  p.input.blur();
  p.key("K", { metaKey: true });
  assert.equal(p.document.activeElement, p.input, "Cmd+K is the same key on a Mac");
  p.input.blur();

  const plain = p.key("k");
  assert.equal(plain.defaultPrevented, false, "k alone is typing");
  p.key("k", { ctrlKey: true, shiftKey: true });
  p.key("k", { ctrlKey: true, altKey: true });
  assert.notEqual(p.document.activeElement, p.input, "Ctrl+Shift+K and Ctrl+Alt+K are other keys");

  // The link key of a composer has already taken Ctrl+K: it is not taken twice.
  p.document.addEventListener("keydown", (e) => e.preventDefault(), true);
  p.key("k", { ctrlKey: true }, p.document.querySelector("textarea"));
  assert.notEqual(p.document.activeElement, p.input);
});

test("what is typed asks for suggestions with the repository of the page and shows the rows", async () => {
  const p = page(() => hit + all("deploy"));
  await p.type("deploy");
  assert.equal(p.calls.length, 1);
  const url = new URL(p.calls[0], "http://localhost");
  assert.equal(url.pathname, "/ui/search/suggest");
  assert.deepEqual([url.searchParams.get("q"), url.searchParams.get("project")], ["deploy", "github.com/acme/shell"]);
  assert.equal(p.list.hidden, false);
  assert.equal(p.rows().length, 2);
  assert.equal(p.input.getAttribute("aria-expanded"), "true");
  assert.equal(p.input.getAttribute("role"), "combobox");
  assert.equal(p.input.getAttribute("aria-controls"), p.list.id);
  assert.equal(p.list.getAttribute("aria-label"), "Search everything");
  assert.deepEqual(p.rows().map((r) => r.id), [`${p.list.id}-0`, `${p.list.id}-1`], "every row has an address for the pointer of the field");
});

test("nothing typed asks nothing, and an answer without rows closes the list", async () => {
  const p = page((url) => (url.includes("q=x") ? "" : hit));
  await p.type("   ");
  assert.equal(p.calls.length, 0);
  assert.equal(p.list.hidden, true);
  await p.type("seven");
  assert.equal(p.list.hidden, false);
  await p.type("x");
  assert.equal(p.list.hidden, true, "no rows, no list");
  assert.equal(p.input.getAttribute("aria-expanded"), "false");
});

test("the arrows move over the rows, Enter takes the row, and without a row Enter searches as always", async () => {
  const p = page(() => hit + qual("is:open") + all("is:o"));
  await p.type("is:o");
  const down = p.key("ArrowDown", {}, p.input);
  assert.equal(down.defaultPrevented, true);
  assert.equal(p.rows()[0].getAttribute("aria-selected"), "true");
  assert.equal(p.input.getAttribute("aria-activedescendant"), p.rows()[0].id);
  p.key("ArrowDown", {}, p.input);
  assert.equal(p.rows()[1].getAttribute("aria-selected"), "true");
  assert.equal(p.rows()[0].getAttribute("aria-selected"), "false");
  p.key("ArrowUp", {}, p.input);
  p.key("ArrowUp", {}, p.input);
  assert.equal(p.rows()[2].getAttribute("aria-selected"), "true", "from the top it goes round to the last row");

  // Enter on a row of an object follows its link.
  p.key("ArrowDown", {}, p.input);
  assert.equal(p.rows()[0].getAttribute("aria-selected"), "true");
  let followed = null;
  p.rows()[0].querySelector("a").addEventListener("click", (e) => { followed = e.target.getAttribute("href"); });
  const enter = p.key("Enter", {}, p.input);
  assert.equal(followed, "/ui/acme/shell/issues/7");
  assert.equal(enter.defaultPrevented, true, "the form is not sent as well");

  // Without a chosen row the form is sent: Enter is left alone.
  const fresh = page(() => qual("is:open"));
  await fresh.type("is:o");
  assert.equal(fresh.key("Enter", {}, fresh.input).defaultPrevented, false);
});

test("a qualifier row writes itself over the word being typed: a key keeps the colon open, a value ends the word", async () => {
  const p = page((url) => (new URL(url, "http://localhost").searchParams.get("q").endsWith(":") ? qual("is:open") + qual("is:done") : qual("is:")));
  await p.type("deploy is");
  p.key("ArrowDown", {}, p.input);
  p.key("Enter", {}, p.input);
  assert.equal(p.input.value, "deploy is:", "the word is replaced, the text before it stays, and the colon stays open for the value");
  assert.equal(p.document.activeElement, p.input);
  await tick();
  assert.equal(new URL(p.calls.at(-1), "http://localhost").searchParams.get("q"), "deploy is:", "the values are asked for at once");
  assert.deepEqual(p.rows().map((r) => r.dataset.insert), ["is:open", "is:done"]);

  p.rows()[1].click();
  assert.equal(p.input.value, "deploy is:done ", "a value ends the word, the next one can be typed");
  assert.equal(p.input.selectionStart, p.input.value.length, "the caret is at the end");
});

test("a click on a row of a qualifier is taken, a click on a link is not; pressing a row keeps the focus in the field", async () => {
  const p = page(() => hit + qual("kind:pain"));
  await p.type("pa");
  const press = new Event("mousedown", { bubbles: true, cancelable: true });
  p.rows()[1].dispatchEvent(press);
  assert.equal(press.defaultPrevented, true, "the field does not lose the focus");
  const link = new Event("click", { bubbles: true, cancelable: true });
  p.rows()[0].querySelector("a").dispatchEvent(link);
  assert.equal(link.defaultPrevented, false, "a link is a link");
  p.rows()[1].click();
  assert.equal(p.input.value, "kind:pain ");
});

test("Escape closes the list, leaving the field closes it, and an answer that comes late is dropped", async () => {
  const waiting = [];
  const calls = [];
  const fetch = (url) => {
    calls.push(url);
    return new Promise((resolve) => waiting.push((html) => resolve({ ok: true, text: () => Promise.resolve(html) })));
  };
  const p = loadPage(FRAME, { url: "http://localhost/ui/", scripts: ["ui-suggest.js"], hooks: { fetch, setTimeout: (fn) => { fn(); return 1; }, clearTimeout: () => {} } });
  const input = p.document.getElementById("shell-search");
  const list = p.document.querySelector("ul.sg-list");
  input.focus();
  input.value = "de";
  input.dispatchEvent(new Event("input", { bubbles: true }));
  input.value = "dep";
  input.dispatchEvent(new Event("input", { bubbles: true }));
  assert.equal(calls.length, 2);
  // The answer of the second request comes first, the first one after it.
  waiting[1](hit);
  await tick();
  assert.equal(list.querySelectorAll("li.sg").length, 1);
  waiting[0](qual("a") + qual("b"));
  await tick();
  assert.equal(list.querySelectorAll("li.sg").length, 1, "the older answer does not replace the newer one");

  const esc = p.key("Escape", {}, input);
  assert.equal(esc.defaultPrevented, true);
  assert.equal(list.hidden, true);

  // Leaving the field closes the list, and what is still on its way is not shown.
  input.value = "dep2";
  input.dispatchEvent(new Event("input", { bubbles: true }));
  input.blur();
  input.dispatchEvent(new Event("blur"));
  waiting[2](hit);
  await tick();
  assert.equal(list.hidden, true, "an answer after the field was left does not open the list");
});

test("without fetch the shortcut still works and the field is left as it is", () => {
  const p = loadPage(FRAME, { url: "http://localhost/ui/", scripts: ["ui-suggest.js"], hooks: { fetch: undefined } });
  p.key("k", { ctrlKey: true });
  assert.equal(p.document.activeElement, p.document.getElementById("shell-search"));
  assert.equal(p.document.querySelector("ul.sg-list"), null);
});

test("a page without a field that asks for suggestions does nothing", () => {
  const p = loadPage(`<form><input name="q"></form>`, { url: "http://localhost/ui/", scripts: ["ui-suggest.js"] });
  const ev = p.key("k", { ctrlKey: true });
  assert.equal(ev.defaultPrevented, false);
});

test("a field that has the focus from the start (the search page) does not open the list before anything is typed", async () => {
  const p = page(() => hit);
  p.input.value = "deploy";
  p.input.focus();
  p.input.dispatchEvent(new Event("focus"));
  await tick();
  assert.equal(p.calls.length, 0);
  assert.equal(p.list.hidden, true);
  p.key("ArrowDown", {}, p.input);
  await tick();
  assert.equal(p.calls.length, 1, "the arrow asks for the list");
  assert.equal(p.list.hidden, false);
});
