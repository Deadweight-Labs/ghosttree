// REQ-628 AC-5 and AC-6: the client script of the search (static/ui-search.js).
// The page is the live frame of templates/search.html, the groups come the way
// /ui/search/stream sends them.
import test from "node:test";
import assert from "node:assert/strict";
import { loadPage } from "./dom.mjs";

const BASE = "http://localhost/ui/search?q=deploy&live=1";

const frameOf = (kind = "") => `
<form class="sform" method="get" action="/ui/search"><input type="search" name="q" value="deploy"><button>Search</button></form>
<form class="other" method="get" action="/ui/elsewhere"><input name="q"></form>
<div class="sprog" data-search-live="/ui/search/stream?q=deploy" data-sync="/ui/search?q=deploy" data-total="3" data-kind="${kind}" data-per="5" data-page="20" data-step="20" data-cap="100" data-more-hits="More hits"><span class="bar"><span></span></span><span class="tx" data-fmt="{done} of {total} kinds"></span><button type="button" data-search-stop>Stop</button></div>
<div class="slayout"><aside><section class="box facets" data-search-kinds hidden>
<a class="frow on" href="/ui/search?q=deploy" data-kind=""><span class="nm">All</span><span class="counter" data-search-all></span></a>
<template data-kind-side="request"></template><template data-kind-side="knowledge"></template><template data-kind-side="pain"></template></section></aside>
<div><div class="box hits" data-search-hits hidden><template data-kind-body="request"></template><template data-kind-body="knowledge"></template><template data-kind-body="pain"></template>
<template data-kind-retry="pain"><div class="hg" data-group="pain" data-retry><div class="box-h"><h2 class="t">Pains</h2></div><div class="hit retry"><button type="button" data-search-more="pain">Search again</button></div></div></template></div>
<div class="box" data-search-none hidden></div></div></div>`;

const frame = frameOf();
const hit = (kind, n) => `<div class="hit"><span class="hm"></span><div><div class="hl"><a class="ht rl" href="/ui/x/shell/${kind}/${n}">${kind} ${n}</a></div></div></div>`;
const rows = (kind, from, to) => Array.from({ length: to - from + 1 }, (_, i) => hit(kind, from + i)).join("\n");
const group = (key, total, from, to, extra = "") =>
  `<div class="hg" data-group="${key}" data-total="${total}" data-narrow="/ui/search?q=deploy&kind=${key}" data-full="/ui/x/shell/${key}?q=deploy"${extra}><div class="box-h"><h2 class="t">${key}</h2><span class="sp"></span><span class="muted" data-count>${total} hits</span></div>\n${rows(key, from, to)}</div>`;
const side = (key, total) => `<a class="frow" href="/ui/search?q=deploy&kind=${key}" data-kind="${key}"><span class="nm">${key}</span><span class="counter">${total}</span></a>`;
const send = (es, key, total, from, to) => es.emit("group", { key, total, side: side(key, total), body: group(key, total, from, to) });

// page opens the live frame with every kind answered: 12 issues of which 5
// are loaded, 2 knowledge entries and 1 pain, each with all of its hits.
function page(opts = {}) {
  const p = loadPage(frame, { url: opts.url || BASE, scripts: ["ui-search.js"] });
  const es = p.EventSource.instances.at(-1);
  send(es, "request", 12, 1, 5);
  send(es, "knowledge", 2, 1, 2);
  send(es, "pain", 1, 1, 1);
  es.emit("done", "");
  return { ...p, es };
}

const shown = (p, key) => [...p.document.querySelectorAll(`.hg[data-group="${key}"] .hit`)].filter((r) => !r.hidden).length;
const visible = (p) => [...p.document.querySelectorAll(".hg[data-group]")].filter((g) => !g.hidden).map((g) => g.dataset.group);
const facet = (p, key) => p.document.querySelector(`[data-search-kinds] a.frow[data-kind="${key}"]`);

test("the live page shows every kind, five hits of each, and the facets with their totals", () => {
  const p = page();
  assert.deepEqual(visible(p), ["request", "knowledge", "pain"]);
  assert.equal(p.document.querySelector("[data-search-hits]").hidden, false);
  assert.equal(p.document.querySelector("[data-search-kinds]").hidden, false);
  assert.equal(p.document.querySelector("[data-search-all]").textContent, "15");
  assert.equal(p.document.querySelector("[data-search-live]").hidden, true, "the progress bar goes when all kinds are in");
  // 12 issues were found but only 5 are loaded: the group links to its own kind.
  const more = p.document.querySelector('.hg[data-group="request"] > a.morelink');
  assert.equal(more.dataset.more, "kind");
  assert.match(more.href, /kind=request/);
  assert.equal(p.document.querySelector('.hg[data-group="knowledge"] > a.morelink'), null, "a kind with all its hits has no link");
});

test("a kind is chosen without loading the document again and without a request when all its hits are loaded", () => {
  const p = page();
  const before = p.EventSource.instances.length;
  const click = facet(p, "knowledge");
  const ev = click.click();
  assert.equal(ev, false, "the click is taken over: the browser does not follow the link");
  assert.deepEqual(visible(p), ["knowledge"]);
  assert.equal(shown(p, "knowledge"), 2);
  assert.equal(p.EventSource.instances.length, before, "no stream was opened");
  assert.equal(facet(p, "knowledge").getAttribute("aria-current"), "true");
  assert.equal(facet(p, "").classList.contains("on"), false);
  assert.equal(p.document.querySelector("[data-search-hits]").hidden, false);
});

test("a kind with more hits than are loaded asks for them once and keeps the rows that are there", () => {
  const p = page();
  const first = [...p.document.querySelectorAll('.hg[data-group="request"] .hit')];
  const before = p.EventSource.instances.length;
  facet(p, "request").click();
  assert.equal(p.EventSource.instances.length, before + 1, "one stream for the kind");
  const stream = p.EventSource.instances.at(-1);
  const url = new URL(stream.url, "http://localhost");
  assert.equal(url.pathname, "/ui/search/stream");
  assert.deepEqual([url.searchParams.get("kind"), url.searchParams.get("only"), url.searchParams.get("n")], ["request", "request", "20"]);
  assert.equal(p.document.querySelector('.hg[data-group="request"] > a.morelink').getAttribute("aria-busy"), "true", "the link shows that it works");

  // The answer has the first five again and seven more.
  send(stream, "request", 12, 1, 12);
  const now = [...p.document.querySelectorAll('.hg[data-group="request"] .hit')];
  assert.equal(now.length, 12);
  first.forEach((row, i) => assert.equal(now[i], row, `row ${i + 1} is the same element, not a copy (no flicker)`));
  assert.deepEqual(now.map((r) => r.querySelector("a.ht").textContent), Array.from({ length: 12 }, (_, i) => `request ${i + 1}`), "the new rows come in order");
  assert.equal(shown(p, "request"), 12);
  assert.equal(p.document.querySelector('.hg[data-group="request"] > a.morelink').hidden, true, "nothing is left to load");
  stream.emit("done", "");
  assert.equal(p.document.querySelector('.hg[data-group="request"] > a.morelink').getAttribute("aria-busy"), null);
  // Going back to the kind that has all its hits needs no stream either.
  const count = p.EventSource.instances.length;
  facet(p, "knowledge").click();
  facet(p, "request").click();
  assert.equal(p.EventSource.instances.length, count, "the kind is loaded: switching asks for nothing");
});

test("a kind that is cut at the cap links to the list of the repository", () => {
  const p = loadPage(frameOf("request"), { url: "http://localhost/ui/search?q=deploy&live=1&kind=request&n=100", scripts: ["ui-search.js"] });
  const es = p.EventSource.instances.at(-1);
  es.emit("group", { key: "request", total: 400, capped: true, side: side("request", 400), body: group("request", 400, 1, 100, " data-capped") });
  es.emit("done", "");
  const more = p.document.querySelector('.hg[data-group="request"] > a.morelink');
  assert.equal(more.dataset.more, "full");
  assert.equal(more.getAttribute("href"), "/ui/x/shell/request?q=deploy");
  assert.equal(more.click(), true, "the link to the list is a plain link");
});

test("the address carries the kind, back returns to the kind before, and a longer list replaces the step", () => {
  const p = page();
  assert.equal(p.path(), "/ui/search?q=deploy&live=1");
  const steps = p.window.history.length;
  facet(p, "knowledge").click();
  assert.equal(p.path(), "/ui/search?q=deploy&live=1&kind=knowledge");
  assert.equal(p.window.history.length, steps + 1, "a new kind is a step of the history");
  p.window.history.back();
  assert.equal(p.path(), "/ui/search?q=deploy&live=1");
  assert.deepEqual(visible(p), ["request", "knowledge", "pain"], "back shows every kind again");
  assert.equal(shown(p, "request"), 5, "and again five hits of each");
  assert.equal(facet(p, "").getAttribute("aria-current"), "true");
  p.window.history.forward();
  assert.deepEqual(visible(p), ["knowledge"]);
});

test("asking for a longer list of the same kind replaces the step of the history", () => {
  const p = loadPage(frame, { url: BASE, scripts: ["ui-search.js"] });
  const first = p.EventSource.instances.at(-1);
  send(first, "request", 50, 1, 5);
  first.emit("done", "");
  facet(p, "request").click();
  const asked = p.EventSource.instances.at(-1);
  assert.equal(new URL(asked.url, "http://localhost").searchParams.get("n"), "20");
  send(asked, "request", 50, 1, 20);
  asked.emit("done", "");
  const steps = p.window.history.length;
  const more = p.document.querySelector('.hg[data-group="request"] > a.morelink');
  assert.equal(more.dataset.more, "step");
  assert.equal(more.dataset.n, "40");
  assert.equal(more.click(), false, "the step is taken over");
  assert.equal(p.window.history.length, steps, "a longer list is not a new step");
  assert.equal(p.path(), "/ui/search?q=deploy&live=1&kind=request&n=40", "but the address says how long it is");
  assert.equal(new URL(p.EventSource.instances.at(-1).url, "http://localhost").searchParams.get("n"), "40");
  p.window.history.back();
  assert.deepEqual(visible(p), ["request"], "back leaves the list: the step before had the same kind");
});

test("the plain links stay what they are: a modified click and the kind link of the page without the script", () => {
  const p = page();
  const link = facet(p, "knowledge");
  assert.equal(link.getAttribute("href"), "/ui/search?q=deploy&kind=knowledge", "the link leads to the page of the kind");
  assert.equal(link.click({ ctrlKey: true }), true, "ctrl-click opens a new tab as always");
  assert.equal(link.click({ metaKey: true }), true);
  assert.equal(link.click({ button: 1 }), true);
  assert.deepEqual(visible(p), ["request", "knowledge", "pain"], "none of them narrowed this page");

  // Without EventSource the page of the server (searched before it was sent) takes over.
  const bare = loadPage(frame, { url: BASE, scripts: ["ui-search.js"], hooks: { EventSource: undefined } });
  assert.equal(bare.path(), "/ui/search?q=deploy", "the script goes to the synchronous page");
});

test("searching from a form asks for the live page, and only a search does", () => {
  const p = page();
  const search = p.document.querySelector("form.sform");
  search.requestSubmit();
  assert.equal(search.querySelector('input[name="live"]').value, "1");
  search.requestSubmit();
  assert.equal(search.querySelectorAll('input[name="live"]').length, 1, "the field is not added twice");
  const other = p.document.querySelector("form.other");
  other.requestSubmit();
  assert.equal(other.querySelector('input[name="live"]'), null, "a form to another page is left alone");
});

test("a kind that ran out of time offers its button again and keeps nothing it does not have", () => {
  const p = loadPage(frame, { url: BASE, scripts: ["ui-search.js"] });
  const es = p.EventSource.instances.at(-1);
  send(es, "request", 2, 1, 2);
  es.emit("group", { key: "pain", slow: true });
  es.emit("done", "");
  assert.deepEqual(visible(p), ["request", "pain"]);
  assert.ok(p.document.querySelector('.hg[data-group="pain"] [data-search-more="pain"]'));
  p.document.querySelector('[data-search-more="pain"]').click();
  const retry = p.EventSource.instances.at(-1);
  assert.match(retry.url, /only=pain/);
  send(retry, "pain", 1, 1, 1);
  assert.equal(p.document.querySelector("[data-search-more]"), null, "the retry button is gone once the kind answered");
  assert.equal(shown(p, "pain"), 1);
});
