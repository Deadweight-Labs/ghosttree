// The test DOM itself (jstest/dom.mjs): what it does has to be what a browser
// does, or the tests of the scripts prove nothing.
import test from "node:test";
import assert from "node:assert/strict";
import { loadPage, manualTimers, Event } from "./dom.mjs";

const page = (body = `<input id="a"><input id="b"><div id="box"></div>`, hooks = {}) => loadPage(body, { hooks });

test("focus and blur fire their events in the order of a browser, and do not bubble", () => {
  const p = page();
  const a = p.document.getElementById("a");
  const b = p.document.getElementById("b");
  const seen = [];
  for (const [name, el] of [["a", a], ["b", b]]) {
    for (const type of ["focus", "blur", "focusin", "focusout"]) el.addEventListener(type, (e) => seen.push(`${name}:${type}${e.bubbles ? "+" : ""}`));
  }
  p.document.body.addEventListener("focus", () => seen.push("body:focus"));
  p.document.body.addEventListener("focusin", () => seen.push("body:focusin"));
  a.focus();
  b.focus();
  b.blur();
  assert.deepEqual(seen, ["a:focus", "a:focusin+", "body:focusin", "a:blur", "a:focusout+", "b:focus", "b:focusin+", "body:focusin", "b:blur", "b:focusout+"]);
  assert.equal(p.document.activeElement, p.document.body);
  a.focus();
  a.focus();
  assert.equal(seen.filter((s) => s === "a:focus").length, 2, "focusing the focused element does nothing");
  a.blur();
  a.blur();
  assert.equal(seen.filter((s) => s === "a:blur").length, 2, "a blur of an element without the focus does nothing");
});

test("removeEventListener removes the listener it is given and no other", () => {
  const p = page();
  const box = p.document.getElementById("box");
  const calls = [];
  const one = () => calls.push("one");
  const two = () => calls.push("two");
  box.addEventListener("click", one);
  box.addEventListener("click", two);
  box.removeEventListener("click", () => {});
  box.removeEventListener("click", one);
  box.click();
  assert.deepEqual(calls, ["two"], "a listener that was never added removed nothing, and the one that was is gone");

  const seen = [];
  const onKey = () => seen.push("key");
  p.document.addEventListener("keydown", onKey);
  p.document.addEventListener("keydown", onKey, true);
  p.document.removeEventListener("keydown", onKey, true);
  p.key("a", {}, box);
  assert.deepEqual(seen, ["key"], "the capture one is gone, the other stays");
  p.document.removeEventListener("keydown", onKey);
  p.key("a", {}, box);
  assert.equal(seen.length, 1);

  const win = [];
  const onPop = () => win.push("pop");
  p.window.addEventListener("popstate", onPop);
  p.window.history.pushState({}, "", "/x");
  p.window.history.back();
  p.window.removeEventListener("popstate", onPop);
  p.window.history.forward();
  p.window.history.back();
  assert.equal(win.length, 1);
});

test("a listener that removes itself while the event runs does not skip the next one", () => {
  const p = page();
  const box = p.document.getElementById("box");
  const calls = [];
  const first = () => { calls.push("first"); box.removeEventListener("click", first); };
  box.addEventListener("click", first);
  box.addEventListener("click", () => calls.push("second"));
  box.click();
  box.click();
  assert.deepEqual(calls, ["first", "second", "second"]);
});

test("the entities the Go templates write are read: &#34; &#39; &#43; &amp; and any number", () => {
  const p = page(`<a id="l" href="/x?q=a&#43;b&amp;c=1" title="say &#34;hi&#34; &#39;you&#39;" data-x="&#x26;&#169;">t&#34;&lt;&#43;</a>`);
  const a = p.document.getElementById("l");
  assert.equal(a.getAttribute("href"), "/x?q=a+b&c=1");
  assert.equal(a.getAttribute("title"), `say "hi" 'you'`);
  assert.equal(a.dataset.x, "&©");
  assert.equal(a.textContent, 't"<+');
});

test("what the page may use and the test DOM does not have fails loudly, and a test can bring its own", () => {
  const p = page();
  for (const name of ["MutationObserver", "IntersectionObserver", "ResizeObserver"]) {
    assert.equal(typeof p.eval(name), "function", name + " can be asked for, so that a script finds it");
    assert.throws(() => new p.window[name](() => {}), new RegExp(name + " is not in the test DOM"), name);
    assert.throws(() => p.eval(`new ${name}(() => {})`), new RegExp(name + " is not in the test DOM"), name + " from a script");
  }
  const own = page(undefined, { MutationObserver: class { observe() {} } });
  assert.equal(typeof own.window.MutationObserver, "function");
});

test("the animation frame is the test's: asked for, run when frame() is called, cancellable; AbortController is the real one", () => {
  const p = page();
  const ran = [];
  p.window.requestAnimationFrame(() => ran.push("a"));
  const id = p.window.requestAnimationFrame(() => ran.push("b"));
  p.window.cancelAnimationFrame(id);
  assert.deepEqual(ran, []);
  p.frame();
  assert.deepEqual(ran, ["a"]);
  p.frame();
  assert.deepEqual(ran, ["a"], "a frame runs once");
  const ctl = new p.window.AbortController();
  ctl.abort();
  assert.equal(ctl.signal.aborted, true);
});

test("a script that keeps changing the page ends with an error instead of never", () => {
  const p = page();
  const box = p.document.getElementById("box");
  assert.throws(() => { for (;;) box.append("x"); }, /endless mutation/);
  const q = page();
  const other = q.document.getElementById("box");
  assert.throws(() => { for (;;) other.innerHTML = "<i></i>"; }, /endless mutation/);
  // A page of its own starts counting again.
  const fresh = page();
  fresh.document.getElementById("box").append("fine");
});

test("the clock of a test runs timers in the order of their time and only when it is turned", () => {
  const clock = manualTimers();
  const ran = [];
  clock.setTimeout(() => ran.push("late"), 100);
  clock.setTimeout(() => ran.push("early"), 10);
  const gone = clock.setTimeout(() => ran.push("gone"), 20);
  clock.clearTimeout(gone);
  clock.advance(9);
  assert.deepEqual(ran, []);
  clock.advance(1);
  assert.deepEqual(ran, ["early"]);
  clock.setTimeout(() => ran.push("again"), 50);
  clock.advance(1000);
  assert.deepEqual(ran, ["early", "again", "late"]);
  assert.equal(clock.pending(), 0);
});
