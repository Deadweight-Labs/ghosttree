// REQ-671 AC-2: Ctrl+V with a picture on the clipboard and dropping a picture file add it on
// the idea, issue and pain page (static/ui-attach.js), through the form of the page. Pasting
// text is left alone. The form is what the template renders: the fixture is written by
// TestJSFixturesAreWhatTheTemplatesRender (jstest_fixtures_test.go).
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { loadPage, Event } from "./dom.mjs";

const fixture = (name) => readFileSync(new URL("./fixtures/" + name, import.meta.url), "utf8");
const FRAME = fixture("attach-box.html") + `<textarea id="comment" class="comp-in"></textarea><input id="alt2"><p id="text">text</p>`;
const HERE = "http://localhost/ui/x/shell/pains/7";
const DONE = HERE + "?done=image#images";

const tick = () => new Promise((resolve) => setImmediate(resolve));

// The parts of FormData the script uses: the fields of the form, and set().
class FakeFormData {
  constructor(form) {
    this.fields = new Map();
    for (const input of form.querySelectorAll("input[name]")) this.fields.set(input.getAttribute("name"), input.value ?? input.getAttribute("value") ?? "");
  }
  set(name, value, filename) { this.fields.set(name, value); if (filename !== undefined) this.names = { ...this.names, [name]: filename }; }
  get(name) { return this.fields.get(name); }
}

// page loads the script with a fetch that answers like the server (a redirect to the page)
// and a location the test can read.
function page({ html = FRAME, answer = () => ({ ok: true, redirected: true, url: DONE }) } = {}) {
  const calls = [];
  const pending = [];
  const fetch = (url, init) => {
    calls.push({ url, init });
    const result = answer(url, init);
    return result instanceof Promise ? result : Promise.resolve(result);
  };
  const reloads = [];
  const location = { href: HERE, reload: () => reloads.push(1) };
  const p = loadPage(html, { url: HERE, scripts: ["ui-attach.js"], hooks: { fetch, FormData: FakeFormData, location } });
  return { ...p, calls, location, reloads, pending };
}

const image = (extra = {}) => ({ type: "image/png", size: 2048, name: "image.png", ...extra });

// clip is a clipboard with the given file and text, as the browser hands it to a paste.
function clip({ file, text = false } = {}) {
  const items = [];
  if (file) items.push({ kind: "file", type: file.type, getAsFile: () => file });
  if (text) items.push({ kind: "string", type: "text/plain" });
  return { files: file ? [file] : [], items };
}

function paste(p, clipboardData, target) {
  const ev = new Event("paste", { bubbles: true, cancelable: true, clipboardData });
  (target || p.document.getElementById("text")).dispatchEvent(ev);
  return ev;
}

function drop(p, files, { types = ["Files"], target } = {}) {
  const dataTransfer = { files, types };
  const over = new Event("dragover", { bubbles: true, cancelable: true, dataTransfer });
  const el = target || p.document.getElementById("text");
  el.dispatchEvent(over);
  const dropped = new Event("drop", { bubbles: true, cancelable: true, dataTransfer });
  el.dispatchEvent(dropped);
  return { over, dropped };
}

test("a picture pasted on the page goes through the form of the page, and the page is loaded again", async () => {
  const p = page();
  p.document.querySelector('input[name="alt"]').value = "The broken button";
  const file = image();
  const ev = paste(p, clip({ file }));
  assert.equal(ev.defaultPrevented, true, "the paste is taken: nothing is inserted into the page");
  await tick();
  assert.equal(p.calls.length, 1, "one upload");
  const { url, init } = p.calls[0];
  assert.equal(url, "/ui/pains/7/attach", "the action of the form");
  assert.equal(init.method, "POST");
  assert.equal(init.credentials, "same-origin");
  assert.equal(init.body.get("file"), file, "the pasted picture is the file of the form");
  assert.equal(init.body.get("csrf_token"), "tok-1", "the token of the form goes along");
  assert.equal(init.body.get("owner"), "pain", "what the picture belongs to");
  assert.equal(init.body.get("alt"), "The broken button", "the description typed into the form goes along");
  assert.equal(p.location.href, DONE, "the page the server answered with is shown: done=image or fail=img_*");
});

test("the page is reloaded when the answer is the page already shown", async () => {
  const p = page({ answer: () => ({ ok: true, redirected: true, url: HERE }) });
  paste(p, clip({ file: image() }));
  await tick();
  assert.equal(p.reloads.length, 1);
});

test("pasting text is never touched, in a field or outside one", async () => {
  const p = page();
  const area = p.document.getElementById("comment");
  for (const target of [area, p.document.getElementById("alt2"), p.document.getElementById("text")]) {
    const ev = paste(p, clip({ text: true }), target);
    assert.equal(ev.defaultPrevented, false, "the browser pastes the text");
  }
  assert.equal(paste(p, { files: [], items: [] }, area).defaultPrevented, false, "an empty clipboard");
  assert.equal(paste(p, undefined, area).defaultPrevented, false, "no clipboard data at all");
  assert.equal(paste(p, clip({ file: { type: "application/pdf", size: 9, name: "a.pdf" } }), area).defaultPrevented, false, "a file that is no picture");
  assert.equal(paste(p, clip({ file: { type: "image/svg+xml", size: 9, name: "a.svg" } }), area).defaultPrevented, false, "SVG is not taken, the server would refuse it");
  await tick();
  assert.equal(p.calls.length, 0, "nothing was uploaded");
});

test("a screenshot pasted into a field is taken, a picture that comes with text is left to the field", async () => {
  const p = page();
  const area = p.document.getElementById("comment");
  assert.equal(paste(p, clip({ file: image() }), area).defaultPrevented, true, "a screenshot has no text: it goes up instead of nothing");
  await tick();
  assert.equal(p.calls.length, 1);
  // A cell copied from a spreadsheet carries a picture and its text: in a field the text is what is wanted.
  assert.equal(paste(p, clip({ file: image(), text: true }), area).defaultPrevented, false);
  await tick();
  assert.equal(p.calls.length, 1, "the picture with text was not uploaded from the field");
  // Outside a field the same clipboard is a picture.
  assert.equal(paste(p, clip({ file: image(), text: true })).defaultPrevented, true);
  await tick();
  assert.equal(p.calls.length, 2);
});

test("a picture file dropped on the page is uploaded, and only a picture file", async () => {
  const p = page();
  const file = image({ name: "shot.png" });
  const { over, dropped } = drop(p, [file]);
  assert.equal(over.defaultPrevented, true, "the page accepts a dragged file, so the browser does not open it");
  assert.equal(dropped.defaultPrevented, true);
  await tick();
  assert.equal(p.calls.length, 1);
  assert.equal(p.calls[0].init.body.get("file"), file);
  assert.equal(p.location.href, DONE);
  assert.equal(p.document.body.classList.contains("ws-dropping"), false, "the mark of the dragging is gone after the drop");

  const q = page();
  const text = drop(q, [{ type: "application/zip", size: 5, name: "a.zip" }]);
  assert.equal(text.dropped.defaultPrevented, false, "a file that is no picture is left to the browser");
  const dragText = drop(q, [], { types: ["text/plain"] });
  assert.equal(dragText.over.defaultPrevented, false, "dragging text over the page is not touched");
  assert.equal(dragText.dropped.defaultPrevented, false);
  await tick();
  assert.equal(q.calls.length, 0);
});

test("a file dropped on the file field of the form is left to that field", async () => {
  const p = page();
  const field = p.document.querySelector('input[type="file"]');
  const { over, dropped } = drop(p, [image()], { target: field });
  assert.equal(over.defaultPrevented, false);
  assert.equal(dropped.defaultPrevented, false);
  await tick();
  assert.equal(p.calls.length, 0, "the field takes it itself");
});

test("a page without the form (guest, reader, closed idea) does nothing at all", async () => {
  const p = page({ html: `<textarea id="comment"></textarea><p id="text">x</p>` });
  assert.equal(paste(p, clip({ file: image() })).defaultPrevented, false, "the browser keeps its behaviour");
  const { over, dropped } = drop(p, [image()]);
  assert.equal(over.defaultPrevented || dropped.defaultPrevented, false);
  await tick();
  assert.equal(p.calls.length, 0);
});

test("a picture over the limit is not sent, and the page says so", async () => {
  const p = page();
  paste(p, clip({ file: image({ size: 5 * 1024 * 1024 + 1 }) }));
  await tick();
  assert.equal(p.calls.length, 0);
  const note = p.document.querySelector(".ws-up-note");
  assert.ok(note, "a note over the form");
  assert.equal(note.getAttribute("role"), "alert");
  assert.match(note.textContent, /too large/);
  // A second one replaces the note instead of piling up.
  paste(p, clip({ file: image({ size: 6 * 1024 * 1024 }) }));
  assert.equal(p.document.querySelectorAll(".ws-up-note").length, 1);
});

test("an answer that is no redirect, or no answer, is told on the page and the page stays", async () => {
  const refused = page({ answer: () => ({ ok: false, redirected: false, url: "http://localhost/ui/pains/7/attach" }) });
  paste(refused, clip({ file: image() }));
  await tick();
  assert.equal(refused.location.href, HERE, "the page is not left");
  assert.match(refused.document.querySelector(".ws-up-note").textContent, /could not be uploaded/);

  const down = page({ answer: () => Promise.reject(new Error("offline")) });
  paste(down, clip({ file: image() }));
  await tick();
  assert.equal(down.location.href, HERE);
  assert.match(down.document.querySelector(".ws-up-note").textContent, /could not be uploaded/);
});

test("a second picture while one is on its way is not sent twice", async () => {
  let release;
  const p = page({ answer: () => new Promise((resolve) => { release = () => resolve({ ok: true, redirected: true, url: DONE }); }) });
  paste(p, clip({ file: image() }));
  paste(p, clip({ file: image({ name: "second.png" }) }));
  await tick();
  assert.equal(p.calls.length, 1);
  assert.equal(p.document.querySelector("form[data-attach]").getAttribute("aria-busy"), "true");
  release();
  await tick();
  await tick();
  assert.equal(p.document.querySelector("form[data-attach]").getAttribute("aria-busy"), null);
  paste(p, clip({ file: image({ name: "third.png" }) }));
  await tick();
  assert.equal(p.calls.length, 2, "after the first one is done the next goes");
});
