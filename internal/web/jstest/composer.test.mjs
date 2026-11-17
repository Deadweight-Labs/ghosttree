// REQ-682 AC-1: the preview of the composer (static/ui-parts.js). It is at least as tall as the text field
// and grows with its content (the script sets no fixed height; the CSS caps it at the window), and the
// toolbar stays where it is while the preview is open, greyed out instead of gone.
import test from "node:test";
import assert from "node:assert/strict";
import { loadPage } from "./dom.mjs";

// The frame of the template "uicomposer" (templates/components.html): tabs, toolbar, field, preview.
const frame = `
<div class="cx"><div class="cx-h"><nav class="utabs-nav"><ul class="utabs">
<li class="sel"><a href="#" data-wtab="w" aria-current="true"><span>Write</span></a></li>
<li><a href="#" data-wtab="p"><span>Preview</span></a></li></ul></nav><span class="sp"></span>
<span class="wtools js-only" role="toolbar"><button type="button" data-tool="bold">B</button><button type="button" data-tool="italic" tabindex="-1">I</button></span></div>
<div class="cx-b"><div class="cx-w"><textarea name="body"></textarea></div><div class="cx-pv prose" data-empty="Nothing to preview" hidden></div></div></div>`;

function page() {
  const p = loadPage(frame, { scripts: [] });
  // A renderer that makes one block per line, like the real one makes blocks of a long text.
  p.window.GTMarkdown = {
    render(text) {
      const frag = p.document.createElement("div");
      for (const line of text.split("\n")) {
        const el = p.document.createElement("p");
        el.textContent = line;
        frag.appendChild(el);
      }
      return frag;
    },
  };
  p.run("ui-parts.js");
  const $ = (s) => p.document.querySelector(s);
  return { ...p, $, tab: (k) => $(`[data-wtab="${k}"]`), ta: $("textarea"), pv: $(".cx-pv"), w: $(".cx-w"), tools: $(".wtools") };
}

test("the preview is as tall as the field at least, and has no fixed height that would cut a long text off", () => {
  const p = page();
  p.w.offsetHeight = 140;
  p.ta.value = Array.from({ length: 80 }, (_, i) => `line ${i}`).join("\n");
  p.tab("p").click();
  assert.equal(p.pv.hidden, false);
  assert.equal(p.w.hidden, true);
  assert.equal(p.pv.style.minHeight, "140px", "never smaller than the field it replaces, so nothing jumps");
  assert.ok(!p.pv.style.height, "no fixed height: the box grows with the content (the CSS caps it at the window)");
  assert.equal(p.pv.children[0].children.length, 80, "the whole text is rendered, a long table or diagram can be checked before it is sent");
});

test("the toolbar stays in its place while the preview is open, greyed out, and comes back for writing", () => {
  const p = page();
  p.tab("p").click();
  assert.equal(p.tools.hidden, false, "the toolbar does not go away: the bar does not jump");
  assert.ok(p.tools.classList.contains("off"), "it is greyed out");
  assert.equal(p.tools.getAttribute("aria-disabled"), "true");
  assert.ok([...p.tools.querySelectorAll("[data-tool]")].every((b) => b.disabled), "and none of its buttons works on a text that is not shown");
  p.tab("w").click();
  assert.equal(p.w.hidden, false);
  assert.equal(p.pv.hidden, true);
  assert.ok(!p.tools.classList.contains("off"));
  assert.equal(p.tools.getAttribute("aria-disabled"), null);
  assert.ok([...p.tools.querySelectorAll("[data-tool]")].every((b) => !b.disabled));
});

test("the preview is rendered again each time the tab is chosen", () => {
  const p = page();
  p.ta.value = "first";
  p.tab("p").click();
  assert.equal(p.pv.children[0].children.length, 1);
  p.tab("w").click();
  p.ta.value = "first\nsecond";
  p.tab("p").click();
  assert.equal(p.pv.children[0].children.length, 2, "the preview shows the text as it is now, not as it was");
});
