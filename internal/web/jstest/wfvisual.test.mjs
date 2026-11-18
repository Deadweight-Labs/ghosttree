// REQ-697: the block view of a workflow file. The layout is a pure function of
// what the server read from the file (no stored positions), the palette offers
// what the file does not have yet, and an edit goes to the server, whose text
// lands in the form's textarea, so saving stays the save of the text form.
import test from "node:test";
import assert from "node:assert/strict";
import { loadPage, manualTimers } from "./dom.mjs";

// Values made in the page's own context compare by their JSON.
const plain = (x) => JSON.parse(JSON.stringify(x));

const catalog = {
  events: [{ name: "pr.merged", label: "Merged" }, { name: "schedule", label: "Schedule" }],
  inputs: [
    { name: "pr.text", label: "PR text", events: ["pr.merged"] },
    { name: "pr.diff", label: "Diff", events: ["pr.merged"] },
    { name: "schedule.slot", label: "Period", events: ["schedule"] },
    { name: "requests.open", label: "Open issues" },
  ],
  blocks: [
    { name: "ghosttree/pr-wishes", label: "Wishes", version: "v1", events: ["pr.merged"], results: ["issue"], model: true, instruction: true },
    { name: "ghosttree/echo", label: "Echo", version: "v1" },
  ],
  kinds: [{ name: "knowledge", label: "Knowledge" }, { name: "issue", label: "Issue" }],
  aliases: ["klein", "mittel"],
  default_alias: "mittel",
};

const view = {
  ok: true,
  keys: [
    { key: "on", line: 2, scalar: "pr.merged" },
    { key: "branches", line: 3, list: ["release/*"] },
    { key: "inputs", line: 4, list: ["pr.text"] },
    { key: "uses", line: 5, scalar: "ghosttree/pr-wishes@v1" },
    { key: "results", line: 6, pairs: [{ key: "issue", value: "5" }, { key: "knowledge", value: "3" }] },
    { key: "model", line: 10, scalar: "gruendlich" },
  ],
  blocks: [{ id: 6, kind: "comment", text: "# bis Release 2.0", line: 9, owner: "model" }, { id: 8, kind: "raw", text: "kein Schlüssel", line: 11 }],
  body: "Zitiere wörtlich.",
  body_line: 12,
  stage: 2,
  errors: [{ key: "results", item: "knowledge", line: 6, message: "ghosttree/pr-wishes cannot create knowledge" }, { line: 11, message: 'expected "key: value"' }],
  warnings: [],
};

function load() {
  const p = loadPage(`<main></main>`, { scripts: [], hooks: { ResizeObserver: class { observe() {} } } });
  p.run("ui-wfvisual.js");
  return p;
}

test("the layout follows the order of a workflow, and the same file gives the same picture", () => {
  const p = load();
  const g = p.window.GTWfv.graph(view, catalog, "");
  assert.deepEqual(plain(g.cols.map((c) => c.name)), ["trigger", "inputs", "task", "results"]);
  assert.deepEqual(plain(g.cols[0].map((n) => n.id)), ["on", "branches"]);
  assert.deepEqual(plain(g.cols[2].map((n) => n.id)), ["body", "uses"]);
  assert.deepEqual(plain(g.subs.map((n) => n.id)), ["model"]);
  assert.deepEqual(plain(g.subNotes.map((n) => n.id)), ["block:6"], "the comment above model stands beside the model");
  assert.deepEqual(plain(g.rest.map((n) => n.id)), ["block:8"], "a line without a key that no key follows stays a text block at the end");
  assert.deepEqual(plain(g.cols[3].map((n) => n.id)), ["results:issue", "results:knowledge"]);
  assert.equal(g.nodes["results:knowledge"].errors.length, 1, "the check's error stands at the kind it is about");
  assert.equal(g.nodes["results:issue"].errors.length, 0);
  assert.equal(g.nodes["block:8"].errors.length, 1, "an error on a line goes to the text block of that line");
  const edges = g.edges.map((e) => e.from + ">" + e.to);
  for (const e of ["on>branches", "branches>inputs", "inputs>uses", "body>uses", "uses>results:issue", "model>uses", "block:6>model"]) assert.ok(edges.includes(e), e);
  assert.ok(g.edges.find((e) => e.to === "results:knowledge").err, "the edge into a node with an error is marked");
  assert.deepEqual(JSON.stringify(p.window.GTWfv.graph(view, catalog, "")), JSON.stringify(g));
});

test("a new file shows the trigger and the block as nodes still to choose", () => {
  const p = load();
  const g = p.window.GTWfv.graph({ ok: true, keys: [{ key: "description", line: 2, scalar: "" }], blocks: [], body: "", errors: [], warnings: [] }, catalog, "");
  assert.ok(g.nodes.on.ghost && g.nodes.uses.ghost && g.nodes.description.ghost);
  const P = p.window.GTWfv.palette({ ok: true, keys: [], blocks: [], body: "" }, catalog);
  assert.deepEqual(plain(P.more.slice(0, 2).map((i) => i.key)), ["on", "uses"]);
});

test("the palette offers only what fits and what the file does not have", () => {
  const p = load();
  const P = p.window.GTWfv.palette(view, catalog);
  assert.deepEqual(plain(P.inputs.map((i) => i.name)), ["pr.diff", "requests.open"], "inputs of the event, not those already read");
  assert.deepEqual(plain(P.inputs[0].edit), { op: "set", key: "inputs", list: ["pr.text", "pr.diff"] });
  assert.deepEqual(plain(P.results.map((i) => i.name)), [], "pr-wishes creates issues only, and issue is there");
  const more = P.more.map((i) => i.key);
  assert.ok(!more.includes("branches") && !more.includes("model") && !more.includes("body"));
  assert.ok(more.includes("budget") && more.includes("chain") && more.includes("description"));
  assert.equal(P.more.find((i) => i.key === "description").pending, "description", "a text block of its own starts empty and is written once typed");
});

test("an edit goes to the server and its text lands in the textarea of the form", async () => {
  const calls = [];
  const next = Object.assign({}, view, { keys: view.keys.concat({ key: "budget", line: 12, pairs: [{ key: "run_usd", value: "0.20" }] }) });
  const fetch = (url, opts) => {
    calls.push({ url, body: new URLSearchParams(String(opts.body)) });
    return Promise.resolve({ ok: true, json: () => Promise.resolve({ text: "NEW TEXT", view: next }) });
  };
  const p = loadPage(`<form class="wfform"><input type="hidden" name="csrf_token" value="tok"><input type="hidden" name="project" value="github.com/x/shell"><div class="wfv" data-wfv data-edit><label class="wfv-text"><textarea name="text">OLD</textarea></label></div></form>`,
    { scripts: [], hooks: { fetch, ResizeObserver: class { observe() {} } } });
  const root = p.document.querySelector("[data-wfv]");
  root.setAttribute("data-model", JSON.stringify({ text: "OLD", view }));
  root.setAttribute("data-catalog", JSON.stringify(catalog));
  root.setAttribute("data-l10n", JSON.stringify({ visual: "Visual", text: "Text" }));
  p.run("ui-wfvisual.js");
  assert.ok(p.document.querySelector('[data-node="results:knowledge"]'), "the nodes are drawn");
  assert.ok(p.document.querySelector("label.wfv-text").hidden, "the text is hidden in the visual view");
  const chip = p.document.querySelectorAll(".wfv-chip").find((b) => b.textContent.includes("budget"));
  assert.ok(chip, "the palette offers the budget");
  chip.click();
  await new Promise((r) => setTimeout(r, 0));
  await new Promise((r) => setTimeout(r, 0));
  assert.equal(calls.length, 1);
  assert.equal(calls[0].url, "/ui/workflows/visual");
  assert.equal(calls[0].body.get("csrf_token"), "tok");
  assert.equal(calls[0].body.get("text"), "OLD");
  assert.deepEqual(JSON.parse(calls[0].body.get("edit")), { op: "set", key: "budget", pairs: [{ key: "run_usd", value: "0.20" }] });
  assert.equal(p.document.querySelector("textarea").value, "NEW TEXT");
  assert.ok(p.document.querySelector('[data-node="budget"]'), "the new node is drawn");
  assert.ok(!p.document.querySelector(".wfv-insp").hidden, "and its settings are open");
  // The text tab shows the textarea with the same text.
  p.document.querySelector('button[data-mode="text"]').click();
  assert.ok(!p.document.querySelector("label.wfv-text").hidden);
});

// REQ-708: a problem of the instruction (a misspelt placeholder) stands at the
// instruction node, which shows the line it starts on like every other node.
test("a problem of the instruction stands at the instruction node", () => {
  const p = load();
  const v = Object.assign({}, view, {
    errors: [{ key: "body", item: "idea.titel", line: 13, message: "unknown placeholder {{idea.titel}}: did you mean {{idea.title}}?" }],
    warnings: [{ key: "body", item: "pr.diff", line: 12, message: "placeholder {{pr.diff}}: the workflow does not read it" }],
  });
  const g = p.window.GTWfv.graph(v, catalog, "");
  assert.equal(g.nodes.body.errors.length, 1);
  assert.equal(g.nodes.body.warnings.length, 1);
  assert.equal(g.nodes.body.line, 12);
  assert.equal(g.loose.length, 0, "the problem is not left loose");
});

// The block that cannot create a kind does not offer it in the palette.
test("the palette of the echo block offers none of the kinds it cannot create", () => {
  const p = load();
  const echo = { ok: true, keys: [{ key: "on", line: 2, scalar: "pr.merged" }, { key: "uses", line: 3, scalar: "ghosttree/echo@v1" }], blocks: [], body: "", errors: [], warnings: [] };
  const P = p.window.GTWfv.palette(echo, Object.assign({}, catalog, { blocks: [{ name: "ghosttree/echo", label: "Echo", version: "v1" }] }));
  assert.deepEqual(plain(P.results), []);
});

function editor(extra = {}) {
  const calls = [];
  const clock = manualTimers();
  const fetch = (url, opts) => {
    calls.push({ url, body: new URLSearchParams(String(opts.body)) });
    const answer = url === "/ui/workflows/visual" ? { text: "NEW", view } : extra.answer || { ok: true, stage: 1, errors: [], warnings: [] };
    return Promise.resolve({ ok: true, json: () => Promise.resolve(answer) });
  };
  const p = loadPage(`<form class="wfform"><input type="hidden" name="csrf_token" value="tok"><input type="hidden" name="project" value="github.com/x/shell"><div class="wfv" data-wfv data-edit><label class="wfv-text"><textarea name="text">OLD</textarea></label></div></form>`,
    { scripts: [], hooks: { fetch, setTimeout: clock.setTimeout, clearTimeout: clock.clearTimeout, ResizeObserver: class { observe() {} } } });
  const root = p.document.querySelector("[data-wfv]");
  root.setAttribute("data-model", JSON.stringify({ text: "OLD", view }));
  root.setAttribute("data-catalog", JSON.stringify(catalog));
  root.setAttribute("data-l10n", JSON.stringify({ visual: "Visual", text: "Text", errors: "%d errors", errors_one: "1 error", checked: "Checked", line: "line %d", checking: "Checking", add: "Add", added: "Added: %s", stage_2: "Stage %d: instruction" }));
  p.run("ui-wfvisual.js");
  return { p, calls, clock };
}
const settle = async () => { for (let i = 0; i < 4; i++) await new Promise((r) => setTimeout(r, 0)); };

// AC-708.2: typing in the text mode checks the file after a pause, without saving.
test("typing in the text mode asks the server for a check after a pause and stores nothing", async () => {
  const { p, calls, clock } = editor({ answer: { ok: false, stage: 2, errors: [{ line: 8, key: "body", message: "unknown placeholder {{idea.titel}}: did you mean {{idea.title}}?" }], warnings: [] } });
  p.document.querySelector('button[data-mode="text"]').click();
  clock.advance(0);
  await settle();
  assert.equal(calls.length, 1, "entering the text mode checks the text once");
  const ta = p.document.querySelector("textarea");
  ta.value = "---\non: idea.created\n---\nNenne {{idea.titel}}";
  ta.dispatchEvent(new p.window.Event("input"));
  ta.value += "x";
  ta.dispatchEvent(new p.window.Event("input"));
  clock.advance(300);
  await settle();
  assert.equal(calls.length, 1, "no request while the person is still typing");
  clock.advance(400);
  await settle();
  assert.equal(calls.length, 2, "one request after the pause, not one per keystroke");
  assert.equal(calls[1].url, "/ui/workflows/check", "the check does not go to the edit or save routes");
  assert.equal(calls[1].body.get("csrf_token"), "tok");
  assert.equal(calls[1].body.get("text"), ta.value);
  const box = p.document.querySelector(".wfv-tcheck");
  assert.ok(box && !box.hidden, "the result stands under the text");
  assert.match(box.textContent, /1 error/);
  assert.match(box.textContent, /line 8/);
  assert.match(box.textContent, /did you mean \{\{idea\.title\}\}\?/);
  assert.equal(ta.value.endsWith("x"), true, "the text stays as typed");
});

// AC-708.3: a click on the add button opens the palette so that it can be seen,
// and a click on a chip says what it added.
test("the add button shows the palette, and a chip says what it added", async () => {
  const { p, calls } = editor();
  const plus = p.document.querySelector(".wfv-plus");
  assert.ok(plus, "the button is there");
  assert.match(plus.textContent, /Add/, "and it says what it does");
  const pal = p.document.querySelector(".wfv-pal");
  assert.ok(pal.hidden, "the palette is closed (a phone) until the button opens it");
  plus.click();
  assert.ok(!pal.hidden && pal.classList.contains("open"), "the palette is open after the click");
  assert.equal(plus.getAttribute("aria-expanded"), "true");
  assert.ok(pal.querySelectorAll(".wfv-chip").length > 0, "with chips in it");
  plus.click();
  assert.ok(pal.hidden, "a second click closes it");
  plus.click();
  const chip = pal.querySelectorAll(".wfv-chip").find((b) => b.textContent.includes("budget"));
  chip.click();
  await settle();
  assert.equal(calls.length, 1);
  const note = p.document.querySelector(".wfv-note");
  assert.ok(!note.hidden, "the click shows what it added");
  assert.match(note.textContent, /Added: .*budget.*Stage 2: instruction/);
});
