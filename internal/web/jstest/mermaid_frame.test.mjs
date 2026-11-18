// The page side of the Mermaid frame (static/ui-mermaid.js): a diagram that takes too long
// does not keep the frame busy for the ones after it, and what the frame sends back is
// checked before it becomes the address of an image.
import test from "node:test";
import assert from "node:assert/strict";
import { loadPage, manualTimers } from "./dom.mjs";

function setup() {
  const clock = manualTimers();
  const p = loadPage(`<main><div class="mmd" id="a"><pre><code>graph TD; A--&gt;B</code></pre></div><div class="mmd" id="b"><pre><code>graph TD; C--&gt;D</code></pre></div></main>`, {
    hooks: { setTimeout: clock.setTimeout, clearTimeout: clock.clearTimeout, GTMarkdown: { word: (k) => k }, MutationObserver: class { observe() {} } },
  });
  p.run("ui-mermaid.js");
  const frames = [];
  const posted = [];
  // Every iframe the script makes gets a window of its own that records what is sent to it.
  const create = p.document.createElement.bind(p.document);
  p.document.createElement = (tag) => {
    const el = create(tag);
    if (tag === "iframe") {
      const w = { postMessage: (m) => posted.push({ frame: w, m }) };
      el.contentWindow = w;
      frames.push({ el, w });
    }
    return el;
  };
  const fromFrame = (w, data) => {
    for (const fn of p.document.windowListeners.get("message") || []) fn({ source: w, data });
  };
  return { p, clock, frames, posted, fromFrame, box: (id) => p.document.getElementById(id) };
}

test("a diagram that times out takes the frame with it, and the next one gets a fresh frame", () => {
  const s = setup();
  s.p.window.GTMermaid.draw(s.box("a"));
  s.p.window.GTMermaid.draw(s.box("b"));
  assert.equal(s.frames.length, 1, "one frame for the page");
  s.fromFrame(s.frames[0].w, { t: "ready" });
  assert.equal(s.posted.length, 1, "the first diagram is sent");
  s.clock.advance(31000);
  assert.equal(s.box("a").getAttribute("data-state"), "err", "the diagram that took too long shows its source");
  assert.equal(s.frames.length, 2, "the frame that kept computing is replaced");
  assert.equal(s.frames[0].el.parentNode, null, "the old frame is gone from the page");
  s.fromFrame(s.frames[1].w, { t: "ready" });
  assert.equal(s.posted.length, 2, "the next diagram goes to the new frame");
  assert.equal(s.posted[1].frame, s.frames[1].w);
});

test("an image that the frame sends is only taken as an svg of a sane size without script", () => {
  const s = setup();
  s.p.window.GTMermaid.draw(s.box("a"));
  s.fromFrame(s.frames[0].w, { t: "ready" });
  const id = s.posted[0].m.id;
  s.fromFrame(s.frames[0].w, { t: "svg", id, svg: '<svg width="10" height="10"><script>alert(1)</script></svg>' });
  assert.equal(s.box("a").getAttribute("data-state"), "err", "a script in the svg is refused");
});

test("a source over the limit is not sent to the frame at all", () => {
  const s = setup();
  s.box("a").querySelector("code").textContent = "graph TD; " + "A-->B;".repeat(5000);
  s.p.window.GTMermaid.draw(s.box("a"));
  assert.equal(s.box("a").getAttribute("data-state"), "err");
  assert.equal(s.posted.length, 0);
});
