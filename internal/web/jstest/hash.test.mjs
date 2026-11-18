// REQ-684 AC-2: a "#n" in a message of a room is the issue or pull request n of the repository of
// the room, and a knowledge entry only where it is written KN-n. ui-md.js marks the number
// (data-pr from the repository of the page, no data-maybe where the page is strict), ui-refs.js asks
// /ui/refs for pr:n and issue:n of that repository and leaves what neither knows as the text it was.
import test from "node:test";
import assert from "node:assert/strict";
import { loadPage } from "./dom.mjs";

function setup({ repo = "github.com/x/shell", strict = true, answer }) {
  const calls = [];
  const fetch = (url) => {
    calls.push(url);
    const refs = new URL(url, "http://localhost").searchParams.getAll("ref");
    return Promise.resolve({ ok: true, json: () => Promise.resolve({ refs: refs.map((r) => answer(r)) }) });
  };
  let p;
  const make = (spec) => {
    const e = p.document.createElement(spec.href ? "a" : "span");
    e.className = "ref " + (spec.cls || "");
    if (spec.href) e.setAttribute("href", spec.href);
    if (spec.ref) e.setAttribute("data-ref", spec.ref);
    e.textContent = spec.text;
    return e;
  };
  const GTPill = {
    icon: () => p.document.createElement("span"),
    kind: () => ({ cls: "k-x", icon: "x" }),
    make,
    ref: (key, o) => make({ cls: "k-x", ref: key, href: o && o.href, text: key.startsWith("REQ-") ? "#" + key.slice(4) : key }),
    setTitle: () => {},
  };
  p = loadPage(`<main></main>`, { scripts: [], hooks: { fetch, GTPill, MutationObserver: class { observe() {} } } });
  if (repo) p.document.body.setAttribute("data-repo", repo);
  if (strict) p.document.body.setAttribute("data-hash", "strict");
  p.run("ui-md.js");
  p.run("ui-refs.js");
  return { ...p, calls };
}

function render(p, text) {
  const host = p.document.createElement("div");
  host.appendChild(p.window.GTMarkdown.render(text, {}));
  p.document.querySelector("main").appendChild(host);
  return host;
}

const resolve = (p, host) => new Promise((done) => p.window.GTRefs.resolve(host, done));

// The server answers issue:2 (it lies in the repository of the page) and nothing else.
const issue2 = (ref) => (ref === "issue:2"
  ? { kind: "request", gefunden: true, ref: "REQ-2", nummer: 2, titel: "Deploy hook signature", link: "/ui/x/shell/issues/2" }
  : { gefunden: false, ref });

test("a #n in a room is the issue of the repository of the room, not a knowledge entry", async () => {
  const p = setup({ answer: issue2 });
  const host = render(p, "Issue #2 habe ich kommentiert, #3 gibt es nicht.");
  await resolve(p, host);
  const pill = host.querySelector("a.ref");
  assert.ok(pill, "the #2 became a pill");
  assert.equal(pill.getAttribute("data-ref"), "REQ-2");
  assert.equal(pill.getAttribute("href"), "/ui/x/shell/issues/2");
  assert.equal(pill.textContent, "#2");
  assert.match(host.textContent, /#3 gibt es nicht/, "a number nothing knows stays the text it was");
  const asked = p.calls.flatMap((u) => new URL(u, "http://localhost").searchParams.getAll("ref"));
  assert.deepEqual(asked.filter((r) => /^kn-|^KN-/.test(r)), [], "no knowledge entry is asked for in a room");
  assert.ok(asked.includes("pr:2") && asked.includes("issue:2"), `asked ${asked}`);
  assert.equal(new URL(p.calls[0], "http://localhost").searchParams.get("project"), "github.com/x/shell");
});

test("a pull request of the repository wins over an issue of the same number", async () => {
  const p = setup({
    answer: (ref) => (ref === "pr:5" ? { kind: "pull", gefunden: true, ref: "#5", nummer: 5, titel: "Fix", zustand: "open", link: "/ui/x/shell/pulls/5" }
      : ref === "issue:5" ? { kind: "request", gefunden: true, ref: "REQ-5", nummer: 5, titel: "Other", link: "/ui/x/shell/issues/5" } : { gefunden: false, ref }),
  });
  const host = render(p, "see #5");
  await resolve(p, host);
  assert.equal(host.querySelector("a.ref").getAttribute("href"), "/ui/x/shell/pulls/5");
});

test("a room without a repository leaves #n as written and asks nothing", async () => {
  const p = setup({ repo: "", answer: issue2 });
  const host = render(p, "Issue #2 habe ich kommentiert");
  await resolve(p, host);
  assert.equal(host.querySelector(".ref"), null);
  assert.match(host.textContent, /Issue #2 habe ich/);
  assert.equal(p.calls.length, 0);
});

test("KN-n written out is a knowledge entry, in a room as anywhere", async () => {
  const p = setup({ answer: issue2 });
  const host = render(p, "siehe KN-17");
  const pill = host.querySelector(".ref");
  assert.equal(pill.getAttribute("data-ref"), "KN-17");
});

test("outside a room the old #1970 of a knowledge entry is still found when there is neither pull request nor issue", async () => {
  const p = setup({
    strict: false,
    answer: (ref) => (ref === "KN-1970" ? { kind: "knowledge", gefunden: true, ref: "KN-1970", nummer: 1970, titel: "An old entry", link: "/ui/x/shell/knowledge/1970" } : { gefunden: false, ref }),
  });
  const host = render(p, "alt: #1970");
  await resolve(p, host);
  assert.equal(host.querySelector("a.ref").getAttribute("data-ref"), "KN-1970");
});
