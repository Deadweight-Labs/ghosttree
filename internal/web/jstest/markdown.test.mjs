// REQ-672: what the one Markdown renderer (static/ui-md.js) makes of a note box,
// a section that opens, an image of an attachment and a ```mermaid block, and the
// two scripts behind the diagram: ui-mermaid.js (the page side) and
// mermaid-frame.js (the sandboxed frame side). The library itself is not run
// here; the frame test gives it a stand-in and the page test plays the frame.
import test from "node:test";
import assert from "node:assert/strict";
import { loadPage } from "./dom.mjs";

// What ui-pill.js gives the renderer: an icon element and a pill, named by what they show.
function pillHooks(getPage) {
  return {
    GTPill: {
      icon: (name) => { const e = getPage().document.createElement("span"); e.className = "i"; e.setAttribute("data-icon", name); return e; },
      ref: (key) => { const e = getPage().document.createElement("span"); e.className = "ref"; e.setAttribute("data-ref", key); return e; },
    },
    MutationObserver: class { constructor(fn) { this.fn = fn; } observe() {} },
  };
}

// open makes a page with ui-md.js. The text is rendered the way the composer's preview does it.
function open({ lang = "en", hooks = {} } = {}) {
  let p;
  p = loadPage(`<main></main>`, { scripts: [], hooks: { ...pillHooks(() => p), ...hooks } });
  p.document.documentElement.setAttribute("lang", lang);
  p.run("ui-md.js");
  return p;
}
function render(p, text) {
  const frag = p.window.GTMarkdown.render(text, {});
  const host = p.document.createElement("div");
  host.appendChild(frag);
  p.document.querySelector("main").appendChild(host);
  return host;
}
const all = (host, sel) => host.querySelectorAll(sel);
// every element below a node, by tag name, in order
const tagsOf = (node) => node.childNodes.filter((n) => n.localName).flatMap((n) => [n.localName, ...tagsOf(n)]);

// ---------------------------------------------------------------- note boxes

test("the five kinds of note box are the callout of the page with their own colour, icon and title", () => {
  const p = open();
  const kinds = { NOTE: ["note", "info", "Note"], TIP: ["ok", "idea", "Tip"], IMPORTANT: ["done", "bubble", "Important"], WARNING: ["", "tri", "Warning"], CAUTION: ["is-danger", "stop", "Caution"] };
  for (const [kind, [cls, icon, title]] of Object.entries(kinds)) {
    const host = render(p, `> [!${kind}]\n> Backups run **nightly**.`);
    const box = host.querySelector(".callout");
    assert.ok(box, `${kind}: no box`);
    assert.equal(box.classList.contains("md-alert"), true);
    if (cls) assert.equal(box.classList.contains(cls), true, `${kind}: class ${cls} in "${box.className}"`);
    assert.equal(box.getAttribute("role"), "note");
    assert.equal(box.querySelector(".i").getAttribute("data-icon"), icon);
    assert.equal(box.querySelector(".md-alert-t").textContent, title);
    assert.equal(box.querySelector("strong").textContent, "nightly");
    assert.equal(host.querySelector("blockquote"), null, `${kind}: still a quote`);
  }
});

test("a note box holds blocks, and the marker is read in any case and with blanks", () => {
  const p = open();
  const host = render(p, ">   [!warning]  \n> first\n>\n> - one\n> - two\n>\n> ```\n> code\n> ```");
  const box = host.querySelector(".callout");
  assert.ok(box);
  assert.equal(box.querySelector("p:not(.md-alert-t)").textContent, "first");
  assert.equal(all(box, "li").length, 2);
  assert.equal(box.querySelector("pre code").textContent, "code");
});

test("a quote that only looks like a note box stays a quote", () => {
  const p = open();
  for (const text of ["> [!NOTE] same line", "> [!NOTE]x\n> body", "> [!FOO]\n> body", "> text\n> [!NOTE]", "[!NOTE]\nnot a quote"]) {
    const host = render(p, text);
    assert.equal(host.querySelector(".callout") === null, true, text);
  }
  assert.ok(render(p, "> [!FOO]\n> body").querySelector("blockquote"));
});

test("the title of a note box follows the language of the page", () => {
  const p = open({ lang: "de" });
  assert.equal(render(p, "> [!CAUTION]\n> x").querySelector(".md-alert-t").textContent, "Achtung");
  assert.equal(render(p, "> [!NOTE]\n> x").querySelector(".md-alert-t").textContent, "Hinweis");
});

// ---------------------------------------------------------------- sections

test("details with a summary on its own line is a section that opens and closes", () => {
  const p = open();
  const host = render(p, "before\n\n<details>\n<summary>Why **not**</summary>\n\nBecause `x` is **slow**.\n\n- a\n- b\n\n</details>\n\nafter");
  const d = host.querySelector("details");
  assert.ok(d);
  assert.equal(d.classList.contains("md-details"), true);
  assert.equal(d.hasAttribute("open"), false);
  assert.equal(d.querySelector("summary").textContent, "Why not");
  assert.equal(d.querySelector("summary strong").textContent, "not");
  assert.equal(d.querySelector("p code").textContent, "x");
  assert.equal(all(d, "li").length, 2);
  // what stands before and after is outside of it
  assert.deepEqual(all(host, "p").map((e) => e.textContent).filter((t) => t === "before" || t === "after"), ["before", "after"]);
  assert.equal(all(d, "p").some((e) => e.textContent === "after"), false);
});

test("details: open, a summary on the same line, none at all, and a section inside a section", () => {
  const p = open();
  assert.equal(render(p, "<details open>\n<summary>S</summary>\nx\n</details>").querySelector("details").hasAttribute("open"), true);
  assert.equal(render(p, "<details><summary>Same line</summary>\nx\n</details>").querySelector("summary").textContent, "Same line");
  assert.equal(render(p, "<details>\nbody only\n</details>").querySelector("summary").textContent, "Details");
  const nested = render(p, "<details>\n<summary>outer</summary>\n<details>\n<summary>inner</summary>\ndeep\n</details>\nstill outer\n</details>\nlast");
  const outer = nested.querySelector("details");
  assert.equal(all(outer, "details").length, 1);
  assert.deepEqual(all(outer, "summary").map((e) => e.textContent), ["outer", "inner"]);
  assert.ok(all(outer, "p").some((e) => e.textContent === "still outer"));
  assert.equal(all(outer, "p").some((e) => e.textContent === "last"), false);
});

test("a section that is never closed takes the rest of the text", () => {
  const p = open();
  const host = render(p, "<details>\n<summary>S</summary>\none\n\ntwo");
  assert.deepEqual(all(host.querySelector("details"), "p").map((e) => e.textContent), ["one", "two"]);
});

test("nothing in a section becomes HTML: tags in the summary and the body are text", () => {
  const p = open();
  const host = render(p, "<details>\n<summary><b>bold</b><script>alert(1)</script><img src=x onerror=alert(1)></summary>\n<script>alert(2)</script>\n<a href=\"javascript:alert(3)\">x</a>\n</details>");
  for (const tag of ["script", "img", "b", "a"]) assert.equal(host.querySelector(tag), null, tag);
  assert.match(host.querySelector("summary").textContent, /<b>bold<\/b><script>alert\(1\)<\/script>/);
  assert.match(host.querySelector("details").textContent, /<script>alert\(2\)<\/script>/);
  // only the elements the renderer itself makes are in there
  for (const t of tagsOf(host)) assert.ok(["div", "details", "summary", "p", "br"].includes(t), `unexpected <${t}>`);
});

test("a tag that is no section line stays text, and a section in a code block stays code", () => {
  const p = open();
  assert.equal(render(p, "a <details> in a line").querySelector("details"), null);
  assert.equal(render(p, "```\n<details>\n<summary>x</summary>\n</details>\n```").querySelector("details"), null);
  assert.equal(render(p, "    <details>\nnot indented code?").querySelector("details"), null);
});

test("a section interrupts a paragraph, as on a code host", () => {
  const p = open();
  const host = render(p, "text\n<details>\n<summary>S</summary>\nbody\n</details>");
  assert.ok(host.querySelector("details"));
  assert.equal(host.querySelector("p").textContent, "text");
});

test("many unclosed sections stay linear", () => {
  const p = open();
  const t = Date.now();
  render(p, "<details>\n".repeat(5000) + "x");
  assert.ok(Date.now() - t < 2000, "took " + (Date.now() - t) + " ms");
});

// ---------------------------------------------------------------- images

test("![alt](ATT-n) and ![alt](/ui/attachments/n) are the image of that attachment, in the middle of the text", () => {
  const p = open();
  const host = render(p, "Before ![the graph](ATT-12) and ![](/ui/attachments/7 \"t\") after.");
  const imgs = all(host, "img");
  assert.equal(imgs.length, 2);
  assert.equal(imgs[0].getAttribute("src"), "/ui/attachments/12");
  assert.equal(imgs[0].getAttribute("alt"), "the graph");
  assert.equal(imgs[1].getAttribute("src"), "/ui/attachments/7");
  assert.equal(imgs[0].className, "md-img");
  assert.equal(host.querySelector("p").textContent, "Before  and  after.");
  assert.equal(imgs[0].parentNode, host.querySelector("p"));
});

test("images work in lists, tables and quotes", () => {
  const p = open();
  assert.equal(all(render(p, "- ![a](ATT-1)"), "li img").length, 1);
  assert.equal(all(render(p, "| a |\n|---|\n| ![b](ATT-2) |"), "td img").length, 1);
  assert.equal(all(render(p, "> ![c](ATT-3)"), "blockquote img").length, 1);
});

test("no other address is loaded as an image", () => {
  const p = open();
  for (const src of ["https://tracker.example/pixel.png", "http://x/y.png", "//host/a.png", "data:image/png;base64,AAAA", "javascript:alert(1)", "/ui/attachments/", "/ui/attachments/x", "/ui/attachments/5/../6", "/static/favicon-32.png", "ATT-0", "ATT-", "ATT-5x", "att-5", "REQ-5", "ATT-99999999999999999999"]) {
    const host = render(p, `![alt](${src})`);
    assert.equal(all(host, "img").length, 0, src);
  }
});

test("an image that cannot be loaded is its alt text, whatever the reason", () => {
  const p = open();
  const host = render(p, "x ![a chart](ATT-404) y ![](ATT-9) z");
  const imgs = all(host, "img");
  // The server answers a picture the reader may not see like one that does not exist; both are an error here.
  for (const img of imgs) img.dispatchEvent(new p.window.Event("error"));
  assert.equal(all(host, "img").length, 0);
  assert.equal(host.querySelector("p").textContent, "x a chart y ATT-9 z");
});

test("an escaped bang is no image", () => {
  const p = open();
  assert.equal(all(render(p, "\\![a](ATT-1)"), "img").length, 0);
});

// ---------------------------------------------------------------- diagrams: the box

const FLOW = "flowchart LR\n  A[Start] --> B{Done?}\n  B -->|yes| C[End]";

test("a mermaid block is a box with its source in it, and nothing is loaded for a page without one", () => {
  const p = open();
  render(p, "# Title\n\n```js\nlet a = 1\n```\n\ntext");
  p.frame();
  assert.equal(p.document.head.querySelector("script"), null, "a script was asked for without a diagram");
  assert.equal(p.document.querySelector("iframe"), null);
  const host = render(p, "```mermaid\n" + FLOW + "\n```");
  const box = host.querySelector(".mmd");
  assert.ok(box);
  assert.equal(box.querySelector("pre code").textContent, FLOW);
  assert.equal(box.querySelector("pre").getAttribute("data-lang"), "mermaid");
  // The language is read in any case; other fences are code as before
  assert.ok(render(p, "```Mermaid\ngraph TD\n A-->B\n```").querySelector(".mmd"));
  assert.equal(all(render(p, "```mermaidish\nx\n```"), ".mmd").length, 0);
  assert.equal(all(render(p, "```mermaid\n\n```"), ".mmd").length, 0, "an empty block is just code");
});

test("the script of the diagrams is asked for once, by a script element of this origin, and only then", () => {
  const p = open();
  p.document.body.setAttribute("data-mermaid", "/static/ui-mermaid.js?v=abc123");
  render(p, "```mermaid\n" + FLOW + "\n```");
  assert.equal(p.document.head.querySelector("script"), null, "before the frame");
  p.frame();
  render(p, "```mermaid\ngraph TD\n A-->B\n```");
  p.frame();
  const scripts = all(p.document.head, "script");
  assert.equal(scripts.length, 1);
  assert.equal(scripts[0].getAttribute("src"), "/static/ui-mermaid.js?v=abc123");
  assert.equal(all(p.document, "[data-state=\"wait\"]").length, 2);
});

test("a script that does not load leaves the source and a short note", () => {
  const p = open({ lang: "de" });
  render(p, "```mermaid\n" + FLOW + "\n```");
  p.frame();
  p.document.head.querySelector("script").dispatchEvent(new p.window.Event("error"));
  const box = p.document.querySelector(".mmd");
  assert.equal(box.getAttribute("data-state"), "err");
  assert.equal(box.querySelector("pre code").textContent, FLOW);
  assert.equal(box.querySelector(".mmd-err").textContent, "Das Diagramm konnte nicht gezeichnet werden");
  // and the next text with a diagram does not ask again
  render(p, "```mermaid\ngraph TD\n A-->B\n```");
  p.frame();
  assert.equal(all(p.document.head, "script").length, 1);
  assert.equal(all(p.document, ".mmd-err").length, 2);
});

test("a text over the limit and the plain blocks of the server make no diagram", () => {
  const p = open();
  const host = render(p, "```mermaid\n" + "A-->B\n".repeat(20000) + "```");
  assert.equal(all(host, ".mmd").length, 0);
  assert.equal(host.querySelector("pre").textContent.length > 65536, true);
});

// ---------------------------------------------------------------- diagrams: the page side

// A page with ui-md.js and ui-mermaid.js and a frame that is played by the test.
function diagrams({ lang = "en", dark = false, vars = null } = {}) {
  const timers = [];
  const observers = [];
  let p;
  p = loadPage(`<main></main>`, {
    scripts: [],
    hooks: {
      ...pillHooks(() => p),
      MutationObserver: class { constructor(fn) { this.fn = fn; observers.push(this); } observe() {} },
      setTimeout: (fn, ms) => { timers.push({ fn, ms }); return timers.length; },
      clearTimeout: () => {},
      matchMedia: (q) => ({ matches: dark && /dark/.test(q), addEventListener() {} }),
      getComputedStyle: vars ? () => ({ getPropertyValue: (k) => vars[k] || "" }) : undefined,
    },
  });
  p.document.documentElement.setAttribute("lang", lang);
  p.run("ui-md.js");
  p.document.body.setAttribute("data-mermaid", "/static/ui-mermaid.js");
  const frame = { posted: [] };
  const api = {
    ...p, timers, observers, frame,
    text(md) {
      const frag = p.window.GTMarkdown.render(md, {});
      const host = p.document.createElement("div");
      host.appendChild(frag);
      p.document.querySelector("main").appendChild(host);
      p.frame();
      return host;
    },
    // The script arrives: run it, and tell the page it loaded.
    arrive() {
      p.run("ui-mermaid.js");
      p.document.head.querySelector("script").dispatchEvent(new p.window.Event("load"));
    },
    iframe: () => p.document.querySelector("iframe"),
    // The frame says something (as its own window, or as someone else).
    say(data, source) {
      const fr = p.document.querySelector("iframe");
      for (const fn of [...(p.document.windowListeners.get("message") || [])]) fn({ data, source: source === undefined ? fr.contentWindow : source });
    },
    // The frame has loaded and says so.
    ready() {
      const fr = p.document.querySelector("iframe");
      fr.contentWindow = { postMessage: (m, target) => frame.posted.push({ m, target }) };
      api.say({ t: "ready" });
    },
  };
  return api;
}
const svgOf = (w, h, extra = "") => `<svg xmlns="http://www.w3.org/2000/svg" width="${w}" height="${h}" viewBox="0 0 ${w} ${h}">${extra}<rect width="${w}" height="${h}"/></svg>`;

test("a diagram is drawn by the frame: a sandboxed iframe of /ui/mermaid-frame, created when the script arrives", () => {
  const d = diagrams();
  const host = d.text("```mermaid\n" + FLOW + "\n```");
  assert.equal(d.iframe(), null);
  d.arrive();
  const fr = d.iframe();
  assert.ok(fr, "no frame");
  assert.equal(fr.getAttribute("src"), "/ui/mermaid-frame");
  assert.equal(fr.getAttribute("sandbox"), "allow-scripts", "the frame may run scripts and nothing more: no same origin");
  assert.equal(fr.getAttribute("aria-hidden"), "true");
  assert.equal(fr.className, "mmd-frame");
  assert.equal(fr.parentNode, d.document.body);
  assert.equal(d.frame.posted.length, 0, "nothing is sent before the frame says it is ready");
  d.ready();
  assert.equal(d.frame.posted.length, 1);
  const { m } = d.frame.posted[0];
  assert.equal(m.t, "draw");
  assert.equal(m.src, FLOW);
  assert.equal(m.dark, false);
  assert.equal(typeof m.id, "number");
  assert.equal(host.querySelector(".mmd").getAttribute("data-state"), "wait");
});

test("the SVG that comes back is only the address of an image, never a node of the page", () => {
  const d = diagrams();
  const host = d.text("```mermaid\n" + FLOW + "\n```");
  d.arrive();
  d.ready();
  const svg = svgOf(648, 210, '<a href="javascript:alert(1)"><text>x</text></a><script>alert(2)</script><foreignObject><div onclick="alert(3)">x</div></foreignObject>');
  d.say({ t: "svg", id: d.frame.posted[0].m.id, svg });
  const box = host.querySelector(".mmd");
  assert.equal(box.getAttribute("data-state"), "ok");
  const img = box.querySelector("img.mmd-img");
  assert.ok(img);
  assert.match(img.getAttribute("src"), /^data:image\/svg\+xml;charset=utf-8,%3Csvg/);
  assert.equal(decodeURIComponent(img.getAttribute("src").split(",")[1]), svg);
  assert.equal(img.getAttribute("width"), "648");
  assert.equal(img.getAttribute("height"), "210");
  assert.equal(img.getAttribute("alt"), "Diagram");
  // Nothing of the SVG is in the page: no link, no script, no markup of any kind, only the box, the source and the image.
  for (const tag of ["a", "script", "svg", "foreignobject", "rect", "text"]) assert.equal(p(d).querySelector(tag), null, tag);
  assert.deepEqual(tagsOf(box), ["img", "pre", "code"]);
});
const p = (d) => d.document.querySelector("main");

test("what is not an SVG, and what does not come from the frame, is not shown", () => {
  const d = diagrams();
  const host = d.text("```mermaid\n" + FLOW + "\n```");
  d.arrive();
  d.ready();
  const id = d.frame.posted[0].m.id;
  d.say({ t: "svg", id, svg: svgOf(10, 10) }, { postMessage() {} });         // another window
  d.say({ t: "svg", id, svg: svgOf(10, 10) }, null);                         // no window
  assert.equal(host.querySelector(".mmd").getAttribute("data-state"), "wait");
  d.say({ t: "svg", id: id + 1, svg: svgOf(10, 10) });                       // not what was asked
  assert.equal(host.querySelector(".mmd").getAttribute("data-state"), "wait");
  d.say({ t: "svg", id, svg: '<div onclick="alert(1)">not a diagram</div>' }); // not an SVG
  const box = host.querySelector(".mmd");
  assert.equal(box.getAttribute("data-state"), "err");
  assert.equal(box.querySelector("img"), null);
  assert.equal(box.querySelector("div"), null);
});

test("a diagram that cannot be drawn shows its source and why, in the language of the page", () => {
  const d = diagrams({ lang: "de" });
  const host = d.text("```mermaid\ngraph TD\n A-->\n```");
  d.arrive();
  d.ready();
  d.say({ t: "err", id: d.frame.posted[0].m.id, msg: "Parse error on line 2" });
  const box = host.querySelector(".mmd");
  assert.equal(box.getAttribute("data-state"), "err");
  assert.equal(box.querySelector("pre code").textContent, "graph TD\n A-->");
  assert.equal(box.querySelector(".mmd-err").textContent, "Das Diagramm konnte nicht gezeichnet werden: Parse error on line 2");
  assert.equal(box.querySelector("img"), null);
});

test("a message from the frame holding HTML is shown as text, not as markup", () => {
  const d = diagrams();
  const host = d.text("```mermaid\ngraph TD\n A-->\n```");
  d.arrive();
  d.ready();
  d.say({ t: "err", id: d.frame.posted[0].m.id, msg: '<img src=x onerror=alert(1)>' });
  assert.equal(all(host, "img").length, 0);
  assert.match(host.querySelector(".mmd-err").textContent, /<img src=x onerror=alert\(1\)>$/);
});

test("a diagram that takes too long ends as an error and the next one is drawn", () => {
  const d = diagrams();
  const host = d.text("```mermaid\ngraph TD\n A-->B\n```\n\n```mermaid\ngraph TD\n C-->D\n```");
  d.arrive();
  d.ready();
  assert.equal(d.frame.posted.length, 1, "one at a time");
  d.timers.at(-1).fn();
  const boxes = all(host, ".mmd");
  assert.equal(boxes[0].getAttribute("data-state"), "err");
  assert.equal(d.frame.posted.length, 2);
  d.say({ t: "svg", id: d.frame.posted[1].m.id, svg: svgOf(40, 20) });
  assert.equal(boxes[1].getAttribute("data-state"), "ok");
});

test("a frame that cannot start the library gives every waiting diagram its source", () => {
  const d = diagrams();
  const host = d.text("```mermaid\ngraph TD\n A-->B\n```");
  d.arrive();
  d.iframe().contentWindow = { postMessage() {} };
  d.say({ t: "fail" });
  assert.equal(host.querySelector(".mmd").getAttribute("data-state"), "err");
});

test("a box that left the page is not sent to the frame, and a diagram that was drawn is not asked for again", () => {
  const d = diagrams();
  const a = d.text("```mermaid\ngraph TD\n A-->B\n```");
  d.arrive();
  d.text("```mermaid\ngraph TD\n C-->D\n```");
  d.text("```mermaid\ngraph TD\n E-->F\n```");
  // the first is typed over before the frame is ready
  a.remove();
  d.ready();
  assert.equal(d.frame.posted.length, 1);
  assert.equal(d.frame.posted[0].m.src, "graph TD\n C-->D");
  d.say({ t: "svg", id: d.frame.posted[0].m.id, svg: svgOf(30, 30) });
  assert.equal(d.frame.posted.length, 2);
  assert.equal(d.frame.posted[1].m.src, "graph TD\n E-->F");
  d.say({ t: "svg", id: d.frame.posted[1].m.id, svg: svgOf(30, 30) });
  // the same source again is drawn from what is known, without the frame
  const again = d.text("```mermaid\ngraph TD\n C-->D\n```");
  assert.equal(again.querySelector(".mmd").getAttribute("data-state"), "ok");
  assert.equal(d.frame.posted.length, 2);
});

test("the colour scheme goes along, and a change draws the diagrams again", () => {
  const d = diagrams({ dark: false, vars: {
    "--canvas-default": "#ffffff", "--fg-default": "#1f2328", "--fg-muted": "#59636e", "--border-default": "#d1d9e0",
    "--canvas-subtle": "#f6f8fa", "--accent-fg": "#0969da", "--accent-subtle": "#388bfd1a", "--danger-fg": "#d1242f",
  } });
  const host = d.text("```mermaid\n" + FLOW + "\n```");
  d.arrive();
  d.ready();
  let sent = d.frame.posted[0].m;
  assert.equal(sent.dark, false);
  // The colours are the tokens of the page as opaque rgb, the ones with an alpha laid over the canvas
  assert.equal(sent.vars.fg, "rgb(31,35,40)");
  assert.match(sent.vars.accentSubtle, /^rgb\(\d+,\d+,\d+\)$/);
  d.say({ t: "svg", id: sent.id, svg: svgOf(100, 50) });
  assert.equal(host.querySelector(".mmd").getAttribute("data-state"), "ok");
  d.document.documentElement.setAttribute("data-theme", "dark");
  assert.equal(d.observers.length > 0, true);
  for (const o of d.observers) o.fn([]);
  assert.equal(d.frame.posted.length, 2, "drawn again");
  sent = d.frame.posted[1].m;
  assert.equal(sent.dark, true);
  assert.equal(sent.src, FLOW);
  d.say({ t: "svg", id: sent.id, svg: svgOf(100, 50) });
  assert.equal(host.querySelector(".mmd").getAttribute("data-state"), "ok");
  assert.equal(all(host, "img").length, 1, "the image is replaced, not added");
});

test("the preview types over a diagram: only what is still on the page is drawn", () => {
  const d = diagrams();
  const first = d.text("```mermaid\ngraph TD\n A-->B\n```");
  d.arrive();
  first.remove();
  for (const n of ["C", "D", "E"]) {
    const h = d.text("```mermaid\ngraph TD\n A-->" + n + "\n```");
    if (n !== "E") h.remove();
  }
  d.ready();
  assert.deepEqual(d.frame.posted.map((x) => x.m.src), ["graph TD\n A-->E"]);
});

// ---------------------------------------------------------------- diagrams: the frame side

function frameScript({ render } = {}) {
  const posted = [];
  const calls = { init: [], render: [] };
  const parent = { postMessage: (m, target) => posted.push({ m, target }) };
  const mermaid = {
    initialize: (c) => calls.init.push(c),
    render: async (id, src) => { calls.render.push({ id, src }); if (render) return render(id, src); return { svg: `<svg id="${id}" width="100%" xmlns="http://www.w3.org/2000/svg" style="max-width: 648.25px;" viewBox="4 4 648.25 209.66"><g/></svg>` }; },
  };
  const p = loadPage(`<main></main>`, { scripts: [], hooks: { parent, mermaid, setTimeout, MutationObserver: class {} } });
  p.run("mermaid-frame.js");
  const send = (data, source = parent) => { for (const fn of [...(p.document.windowListeners.get("message") || [])]) fn({ data, source }); };
  const settled = () => new Promise((ok) => setTimeout(ok, 20));
  return { p, posted, calls, send, settled, parent };
}

test("the frame says it is ready, and refuses a library that is not there", () => {
  const f = frameScript();
  assert.deepEqual(JSON.parse(JSON.stringify(f.posted.map((x) => x.m))), [{ t: "ready" }]);
  const p = loadPage(`<main></main>`, { scripts: [], hooks: { parent: { postMessage: (m) => (p.posted ||= []).push(m) }, MutationObserver: class {} } });
  p.run("mermaid-frame.js");
  assert.deepEqual(JSON.parse(JSON.stringify(p.posted)), [{ t: "fail" }]);
});

test("the frame draws with the strict level, without starting by itself, and answers with the SVG at the size of its view box", async () => {
  const f = frameScript();
  f.send({ t: "draw", id: 5, src: "graph TD\n A-->B", dark: false });
  await f.settled();
  const c = f.calls.init[0];
  assert.equal(c.securityLevel, "strict");
  assert.equal(c.startOnLoad, false);
  assert.equal(c.suppressErrorRendering, true);
  assert.equal(c.theme, "default");
  assert.ok(c.maxTextSize <= 50000);
  assert.equal(f.calls.render[0].src, "graph TD\n A-->B");
  const reply = f.posted.at(-1).m;
  assert.equal(reply.t, "svg");
  assert.equal(reply.id, 5);
  assert.match(reply.svg, /^<svg width="649" height="210" id="mmd1" xmlns=/);
  assert.equal(/style=|width="100%"/.test(reply.svg.split(">")[0]), false);
});

test("the frame uses the colours of the page when it gets them, else the theme of the library", async () => {
  const vars = { bg: "rgb(255,255,255)", fg: "rgb(1,2,3)", muted: "rgb(4,5,6)", border: "rgb(7,8,9)", subtle: "rgb(10,11,12)", accent: "rgb(13,14,15)", accentSubtle: "rgb(16,17,18)", danger: "rgb(19,20,21)" };
  const f = frameScript();
  f.send({ t: "draw", id: 1, src: "graph TD\n A-->B", dark: true, vars });
  f.send({ t: "draw", id: 2, src: "graph TD\n A-->B", dark: true });
  f.send({ t: "draw", id: 3, src: "graph TD\n A-->B", dark: false, vars: { ...vars, fg: "x".repeat(100) } });
  await f.settled();
  assert.equal(f.calls.init[0].theme, "base");
  assert.equal(f.calls.init[0].themeVariables.darkMode, true);
  assert.equal(f.calls.init[0].themeVariables.textColor, "rgb(1,2,3)");
  assert.equal(f.calls.init[1].theme, "dark");
  assert.equal(f.calls.init[1].themeVariables, undefined);
  assert.equal(f.calls.init[2].theme, "default", "a colour that is no colour is not passed on");
});

test("the frame answers an error with its first line, one diagram after the other, and only its parent", async () => {
  const f = frameScript({ render: async (_id, src) => { if (src.includes("bad")) throw new Error("Parse error on line 2\nExpecting x\n..."); return { svg: `<svg viewBox="0 0 10 10"></svg>` }; } });
  f.send({ t: "draw", id: 1, src: "bad", dark: false });
  f.send({ t: "draw", id: 2, src: "good", dark: false });
  f.send({ t: "draw", id: 3, src: "x", dark: false }, { postMessage() {} });  // not the parent
  f.send({ t: "draw", id: "4", src: "x" });                                      // wrong shape
  f.send({ t: "draw", id: 5, src: 7 });
  f.send({ t: "nope", id: 6, src: "x" });
  await f.settled();
  const replies = f.posted.slice(1).map((x) => x.m);
  assert.deepEqual(replies.map((r) => [r.t, r.id]), [["err", 1], ["svg", 2]]);
  assert.equal(replies[0].msg, "Parse error on line 2");
  assert.deepEqual(f.calls.render.map((c) => c.src), ["bad", "good"]);
});

test("the frame gives back nothing that is not an SVG", async () => {
  const f = frameScript({ render: async () => ({ svg: "<div onclick=x>hi</div>" }) });
  f.send({ t: "draw", id: 1, src: "x", dark: false });
  await f.settled();
  assert.equal(f.posted.at(-1).m.t, "err");
});
