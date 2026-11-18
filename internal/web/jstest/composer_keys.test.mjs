// REQ-687 AC-1: Ctrl+Enter sends a composer (static/ui-parts.js), and the tile of a picture writes its line
// ![description](ATT-n) into the field of the page. The tile is what the template "wsimage" renders
// (fixtures/attach-tile.html, written by TestJSFixturesAreWhatTheTemplatesRender).
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { loadPage } from "./dom.mjs";

const tile = readFileSync(new URL("./fixtures/attach-tile.html", import.meta.url), "utf8");

const composer = (extra = "") => `
<form method="post" action="/ui/x/comment" data-draft>
<div class="cx"${extra}>
<div class="cx-h"><ul class="utabs"><li class="sel"><a href="#" data-wtab="w"><span>Write</span></a></li><li><a href="#" data-wtab="p"><span>Preview</span></a></li></ul></div>
<div class="cx-b"><div class="cx-w"><textarea name="body"></textarea></div><div class="cx-pv prose" hidden></div></div>
<div class="cx-f"><button class="gbtn pri" data-submit>Comment</button></div>
</div></form>`;

// A textarea in the small DOM has neither the editing command nor setRangeText: the test gives it both,
// the way a browser changes the text at the marked place.
function page(html, { lang = "en" } = {}) {
  const p = loadPage(html, { scripts: [] });
  p.document.documentElement.lang = lang;
  const splice = (ta, text) => {
    const a = ta.selectionStart, b = ta.selectionEnd;
    ta.value = ta.value.slice(0, a) + text + ta.value.slice(b);
    ta.selectionStart = ta.selectionEnd = a + text.length;
  };
  p.document.execCommand = (cmd, _ui, text) => {
    if (cmd !== "insertText") return false;
    splice(p.document.activeElement, text);
    return true;
  };
  p.window.GTMarkdown = { render: () => p.document.createElement("div") };
  p.run("ui-parts.js");
  return p;
}

const submits = (p) => {
  const seen = [];
  p.document.querySelector("form").addEventListener("submit", (e) => { e.preventDefault(); seen.push(e); });
  return seen;
};

test("Ctrl+Enter in the field of a composer sends the form, and so does Cmd+Enter", () => {
  const p = page(composer());
  const ta = p.document.querySelector("textarea");
  const seen = submits(p);
  ta.focus();
  const plain = p.key("Enter", {}, ta);
  assert.equal(seen.length, 0, "Enter alone is a line break and sends nothing");
  assert.equal(plain.defaultPrevented, false);
  const ctrl = p.key("Enter", { ctrlKey: true }, ta);
  assert.equal(seen.length, 1, "Ctrl+Enter sends");
  assert.equal(ctrl.defaultPrevented, true, "and the browser does not add a line break");
  p.key("Enter", { metaKey: true }, ta);
  assert.equal(seen.length, 2, "Cmd+Enter sends too");
  p.key("Enter", { ctrlKey: true, shiftKey: true }, ta);
  assert.equal(seen.length, 2, "a chord with Shift is none of ours");
});

test("Ctrl+Enter does nothing while the send is held back, and outside a composer", () => {
  const p = page(composer() + `<textarea name="other"></textarea>`);
  const seen = submits(p);
  p.document.querySelector("[data-submit]").disabled = true;
  p.key("Enter", { ctrlKey: true }, p.document.querySelector("textarea"));
  assert.equal(seen.length, 0, "the button is off (a send is under way): no second send");
  p.key("Enter", { ctrlKey: true }, p.document.querySelector('textarea[name="other"]'));
  assert.equal(seen.length, 0, "a field that is no composer is left alone");
});

test("the button of the composer names the keys", () => {
  const en = page(composer());
  assert.match(en.document.querySelector("[data-submit]").title, /Ctrl\+Enter/);
  const de = page(composer(), { lang: "de" });
  assert.match(de.document.querySelector("[data-submit]").title, /Strg\+Enter/);
});

test("the tile of a picture writes its line into the field, on a line of its own, at the cursor", () => {
  const p = page(`<div class="ws-imgs">${tile}</div>` + composer());
  assert.ok(p.document.documentElement.classList.contains("has-cx"), "the page has a field for a text: the tile may show its button");
  const ta = p.document.querySelector("textarea");
  const button = p.document.querySelector("[data-att-insert]");
  assert.equal(button.dataset.attInsert, "ATT-7");
  ta.value = "See this:";
  ta.selectionStart = ta.selectionEnd = ta.value.length;
  button.click();
  assert.equal(ta.value, "See this:\n![The broken button](ATT-7)\n", "the picture is named in the text, on its own line");
  assert.equal(ta.selectionStart, ta.value.length, "the cursor stands behind it, ready to go on writing");
});

test("a tile without a description writes an empty one, and the line has no brackets from the description", () => {
  const p = page(tile.replace('data-desc="The broken button"', 'data-desc="a [b]\nc"') + composer());
  const ta = p.document.querySelector("textarea");
  p.document.querySelector("[data-att-insert]").click();
  assert.equal(ta.value, "![a b c](ATT-7)\n");
  const bare = page(tile.replace('data-desc="The broken button"', 'data-desc=""') + composer());
  bare.document.querySelector("[data-att-insert]").click();
  assert.equal(bare.document.querySelector("textarea").value, "![](ATT-7)\n");
});

test("with the preview open the tile switches back to writing; without a composer the page keeps the button hidden", () => {
  const p = page(tile + composer());
  const w = p.document.querySelector(".cx-w");
  p.document.querySelector('[data-wtab="p"]').click();
  assert.equal(w.hidden, true);
  p.document.querySelector("[data-att-insert]").click();
  assert.equal(w.hidden, false, "the text the line went into is shown again");
  assert.match(p.document.querySelector("textarea").value, /ATT-7/);

  const none = page(tile);
  assert.ok(!none.document.documentElement.classList.contains("has-cx"), "no field, no button (the CSS shows it only with has-cx)");
});

// REQ-706 (S08): the reply field of a thread and the field of a room send with .comp-send, not with a
// [data-submit] button. The markup is what the template renders (fixtures/coord-composer-*.html).
for (const name of ["thread", "room"]) {
  const html = readFileSync(new URL(`./fixtures/coord-composer-${name}.html`, import.meta.url), "utf8");
  const sends = (p) => {
    const asked = [];
    const form = p.document.querySelector("form");
    form.requestSubmit = (submitter) => { asked.push(submitter); };
    return asked;
  };

  test(`Ctrl+Enter and Cmd+Enter in the ${name} field send with its send button`, () => {
    const p = page(`<form method="post" action="/ui/coord/${name}/post">${html}</form>`);
    const asked = sends(p);
    const ta = p.document.querySelector("textarea");
    ta.value = "Ten minutes are enough.";
    ta.focus();
    p.key("Enter", {}, ta);
    assert.equal(asked.length, 0, "Enter alone is not ours here");
    const ctrl = p.key("Enter", { ctrlKey: true }, ta);
    assert.equal(asked.length, 1, "Ctrl+Enter sends");
    assert.ok(asked[0].classList.contains("comp-send"), "with the send button, not the request button");
    assert.equal(ctrl.defaultPrevented, true);
    p.key("Enter", { metaKey: true }, ta);
    assert.equal(asked.length, 2, "Cmd+Enter sends too");
  });

  test(`the send button of the ${name} field names the keys`, () => {
    const en = page(`<form>${html}</form>`);
    assert.match(en.document.querySelector(".comp-send").title, /Ctrl\+Enter/);
    const de = page(`<form>${html}</form>`, { lang: "de" });
    assert.match(de.document.querySelector(".comp-send").title, /Strg\+Enter/);
  });
}
