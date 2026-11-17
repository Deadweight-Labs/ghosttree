// REQ-677 AC-2: the pill of an @name shows the full name of whom it means, and the
// editor of any page is offered the people and agents of the project (static/ui-mentions.js).
import test from "node:test";
import assert from "node:assert/strict";
import { loadPage } from "./dom.mjs";

const tick = () => new Promise((resolve) => setImmediate(resolve));
const NAME = "Mara Keller (Tech-Lead)";

function page(html, answer, repo = "github.com/x/shell") {
  const calls = [];
  const fetch = (url) => {
    calls.push(url);
    return Promise.resolve({ ok: true, json: () => Promise.resolve(answer(new URL(url, "http://localhost"))) });
  };
  const p = loadPage(html, { url: "http://localhost/ui/x/shell/issues/1", hooks: { fetch } });
  if (repo) p.document.body.setAttribute("data-mention-repo", repo);
  p.run("ui-mentions.js");
  return { ...p, calls };
}

const answerNames = (url) => ({
  names: Object.fromEntries(url.searchParams.getAll("h").filter((h) => h === "mara").map((h) => [h, { name: NAME }])),
  options: url.searchParams.get("options") === "1"
    ? [{ handle: "mara", label: NAME }, { handle: "claude-jonas", label: "Claude · Jonas", provider: "claude", agent: true }]
    : [],
});

test("the pill of a handle shows the full name, as text, with the brackets of the name", async () => {
  const p = page(`<p class="t"><span class="mention">@mara</span> kannst du das ansehen?</p><p class="u"><span class="mention">@nobody</span></p>`, answerNames);
  await p.window.GTMentions.resolve(p.document);
  const pill = p.document.querySelector(".t .mention");
  assert.equal(pill.textContent, "@" + NAME);
  assert.equal(pill.getAttribute("title"), NAME);
  assert.equal(p.document.querySelector(".u .mention").textContent, "@nobody", "a handle that fits nobody stays as written");
  assert.equal(p.calls.length, 1, "one call for all the pills of the page");
  const url = new URL(p.calls[0], "http://localhost");
  assert.equal(url.pathname, "/ui/mentions");
  assert.equal(url.searchParams.get("project"), "github.com/x/shell");
  assert.deepEqual(url.searchParams.getAll("h").sort(), ["mara", "nobody"]);
  await p.window.GTMentions.resolve(p.document);
  assert.equal(p.calls.length, 1, "a pill that was answered is not asked again");
});

test("a name typed out after the handle is not said twice", async () => {
  const p = page(`<p class="t"><span class="mention">@Mara</span> Keller (Tech-Lead) gute Frage</p>`, (url) => ({ names: { Mara: { name: NAME } } }));
  await p.window.GTMentions.resolve(p.document);
  const t = p.document.querySelector(".t");
  assert.equal(t.textContent, "@" + NAME + " gute Frage");
});

test("a page without a project asks nothing and leaves the pills as written", async () => {
  const p = page(`<p><span class="mention">@mara</span></p>`, answerNames, "");
  await p.window.GTMentions.resolve(p.document);
  assert.equal(p.calls.length, 0);
  assert.equal(p.document.querySelector(".mention").textContent, "@mara");
});

test("an editor without a list gets the people and agents of the project when it is focused", async () => {
  const p = page(`<form><div class="cx"><div class="cx-w"><textarea name="body"></textarea></div></div></form>`, answerNames);
  const area = p.document.querySelector("textarea");
  area.focus();
  await tick();
  await tick();
  const items = [...p.document.querySelectorAll(".comp-suggest li")];
  assert.deepEqual(items.map((i) => i.getAttribute("data-handle")), ["mara", "claude-jonas"]);
  assert.equal(items[0].querySelector(".comp-suggest-name").textContent, NAME);
  assert.equal(items[1].querySelector("small").textContent, "claude");
  area.blur();
  area.focus();
  await tick();
  assert.equal(p.document.querySelectorAll(".comp-suggest").length, 1, "the list is written once");
  assert.equal(p.calls.length, 1);
});
