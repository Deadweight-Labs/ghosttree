// Mermaid diagrams in a text (REQ-672). ui-md.js writes a box .mmd with the
// source in it for every ```mermaid block and loads this file, only then, with a
// script element of this origin; it is not part of the base bundle. The library
// itself (vendor/mermaid.min.js, 5.5 MB) is not loaded by this page at all: it
// lives in a sandboxed frame of its own (/ui/mermaid-frame, mermaid_frame.go),
// which is created when the first diagram is asked for. The reason is the CSP:
// the library writes a stylesheet into every diagram and measures its text with
// inline styles, which style-src 'self' blocks (the layout comes out wrong), and
// the CSP of the pages is not loosened for it. The frame draws, the answer is the
// SVG as text, and the page shows it as an image with a data: address, where it
// cannot run a script, load a thing or reach the page.
//
//   GTMermaid.draw(box)   draws the source in box (.mmd > pre > code) into it
//
// A box is in the state "wait" while it is drawn, "ok" when the image is in, "err"
// when the source is shown with the reason. A colour scheme change draws the
// diagrams again, with the colours of the other scheme.
(function () {
  "use strict";
  var FRAME = "/ui/mermaid-frame", TIMEOUT = 30000, CACHE_MAX = 64;
  // The most a source may be (the limit of the frame, maxTextSize) and an answer may be.
  var MAX_SOURCE = 20000, MAX_SVG = 4 << 20;
  // The words are those of ui-md.js, which has loaded this file.
  function word(key) { return (window.GTMarkdown && window.GTMarkdown.word(key)) || ""; }

  var frame = null, ready = false, failed = false;
  var queue = [];        // requests not sent yet: {id, key, src, dark, vars, waiters}
  var sent = null;       // the one the frame draws now
  var byKey = {};        // key -> request in the queue or at the frame
  var cache = new Map(); // key -> {url, w, h} of a drawn diagram, oldest first
  var boxes = new Set(); // the boxes that were drawn, for a change of scheme
  var nextId = 0, timer = 0;

  function dark() {
    var t = document.documentElement.getAttribute("data-theme");
    if (t) return t === "dark";
    return !!(window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches);
  }

  // #rgb, #rrggbb or #rrggbbaa as [r, g, b, a], or null.
  function hex(s) {
    var m = /^#([0-9a-f]{3,4}|[0-9a-f]{6}|[0-9a-f]{8})$/i.exec((s || "").trim());
    if (!m) return null;
    var h = m[1];
    if (h.length <= 4) h = h.replace(/./g, "$&$&");
    return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16), h.length === 8 ? parseInt(h.slice(6), 16) / 255 : 1];
  }
  function rgb(c) { return "rgb(" + c[0] + "," + c[1] + "," + c[2] + ")"; }
  function over(top, base) {
    var a = top[3], out = [];
    for (var k = 0; k < 3; k++) out.push(Math.round(top[k] * a + base[k] * (1 - a)));
    return out;
  }
  // The colours of the page for the frame, as opaque rgb(): the tokens of ui-tokens.css, the ones
  // with an alpha laid over the canvas. Without them (no style, a token that is no colour) the frame
  // draws with the colours of the library.
  function palette() {
    try {
      var cs = getComputedStyle(document.documentElement), c = {};
      var names = { bg: "--canvas-default", fg: "--fg-default", muted: "--fg-muted", border: "--border-default", subtle: "--canvas-subtle", accent: "--accent-fg", accentSubtle: "--accent-subtle", danger: "--danger-fg" };
      for (var k in names) if (!(c[k] = hex(cs.getPropertyValue(names[k])))) return null;
      var out = {};
      for (var n in c) out[n] = rgb(over(c[n], c.bg));
      return out;
    } catch (e) { return null; }
  }

  function remember(key, v) {
    cache.delete(key);
    cache.set(key, v);
    if (cache.size > CACHE_MAX) cache.delete(cache.keys().next().value);
  }

  function ensureFrame() {
    if (frame || failed) return;
    frame = document.createElement("iframe");
    frame.className = "mmd-frame";
    frame.setAttribute("sandbox", "allow-scripts");
    frame.setAttribute("aria-hidden", "true");
    frame.setAttribute("tabindex", "-1");
    frame.setAttribute("title", "");
    frame.setAttribute("referrerpolicy", "no-referrer");
    frame.setAttribute("src", FRAME);
    document.body.appendChild(frame);
  }

  // The frame goes, with whatever it still computes: a diagram that takes too long would
  // otherwise hold it for every one after it. The next request makes a new one.
  function dropFrame() {
    if (frame) frame.remove();
    frame = null;
    ready = false;
  }

  function settle(req, result) {
    delete byKey[req.key];
    if (result.url) remember(req.key, result);
    req.waiters.forEach(function (w) { w(result); });
  }

  // The next request that somebody still waits for goes to the frame: a box that has left the page
  // (the preview was typed over) is not drawn.
  function pump() {
    if (!ready || sent) return;
    while (queue.length) {
      var req = queue.shift();
      req.waiters = req.waiters.filter(function (w) { return w.box.isConnected; });
      if (!req.waiters.length) { delete byKey[req.key]; continue; }
      sent = req;
      frame.contentWindow.postMessage({ t: "draw", id: req.id, src: req.src, dark: req.dark, vars: req.vars }, "*");
      clearTimeout(timer);
      timer = setTimeout(function () {
        if (sent !== req) return;
        sent = null;
        settle(req, { err: "timeout" });
        dropFrame();
        if (queue.length) ensureFrame();
      }, TIMEOUT);
      return;
    }
  }

  window.addEventListener("message", function (ev) {
    var d = ev.data;
    if (!frame || ev.source !== frame.contentWindow || !d) return;
    if (d.t === "ready") { ready = true; pump(); return; }
    if (d.t === "fail") { failed = true; ready = false; while (queue.length) settle(queue.shift(), { err: "" }); return; }
    if (!sent || d.id !== sent.id) return;
    var req = sent;
    sent = null;
    clearTimeout(timer);
    // An image is only taken as a drawing of a sane size without script or event handler; the
    // page shows it as an image, where none of that would run, but it is not kept if it has it.
    if (d.t === "svg" && typeof d.svg === "string" && d.svg.length <= MAX_SVG && /^\s*<svg[\s>]/.test(d.svg) &&
        !/<script[\s>\/]|\son[a-z]+\s*=/i.test(d.svg)) {
      var m = /^\s*<svg\b[^>]*\swidth="(\d+)"[^>]*\sheight="(\d+)"/.exec(d.svg);
      settle(req, { url: "data:image/svg+xml;charset=utf-8," + encodeURIComponent(d.svg), w: m ? Number(m[1]) : 0, h: m ? Number(m[2]) : 0 });
    } else {
      settle(req, { err: typeof d.msg === "string" ? d.msg : "" });
    }
    pump();
  });

  function request(src, isDark, vars, box, done) {
    var key = (isDark ? "d" : "l") + "\n" + src;
    if (src.length > MAX_SOURCE) { done({ err: "too large" }); return; }
    if (cache.has(key)) { var hit = cache.get(key); remember(key, hit); done(hit); return; }
    done.box = box;
    var req = byKey[key];
    if (!req) {
      req = byKey[key] = { id: ++nextId, key: key, src: src, dark: isDark, vars: vars, waiters: [] };
      queue.push(req);
    }
    req.waiters.push(done);
    ensureFrame();
    pump();
  }

  function sourceOf(box) {
    var code = box.querySelector("code");
    return code ? code.textContent : "";
  }

  // The box shows the source and why it could not be drawn.
  function fail(box, why) {
    box.setAttribute("data-state", "err");
    var img = box.querySelector("img.mmd-img");
    if (img) img.remove();
    var note = box.querySelector(".mmd-err");
    if (!note) { note = document.createElement("p"); note.className = "mmd-err"; box.appendChild(note); }
    note.textContent = word("nodiagram") + (why ? ": " + why : "");
  }

  function show(box, r) {
    var old = box.querySelector("img.mmd-img"), note = box.querySelector(".mmd-err");
    if (note) note.remove();
    var img = document.createElement("img");
    img.className = "mmd-img";
    img.setAttribute("alt", word("diagram"));
    img.setAttribute("decoding", "async");
    if (r.w > 0 && r.h > 0) { img.setAttribute("width", String(r.w)); img.setAttribute("height", String(r.h)); }
    img.setAttribute("src", r.url);
    if (old) old.replaceWith(img); else box.insertBefore(img, box.firstChild);
    box.setAttribute("data-state", "ok");
    // The boxes of a preview that was typed over are not kept for ever.
    if (boxes.size > 100) boxes.forEach(function (b) { if (!b.isConnected) boxes.delete(b); });
    boxes.add(box);
  }

  function draw(box) {
    var src = sourceOf(box);
    if (!box.getAttribute("data-state")) box.setAttribute("data-state", "wait");
    request(src, dark(), palette(), box, function (r) {
      if (!box.isConnected) return;
      if (r.url) show(box, r); else fail(box, r.err);
    });
  }

  // A scheme change: every diagram that was drawn is drawn again, in the colours of the other one.
  function redraw() {
    boxes.forEach(function (box) {
      if (!box.isConnected) { boxes.delete(box); return; }
      draw(box);
    });
  }
  new MutationObserver(redraw).observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
  if (window.matchMedia) {
    var mq = window.matchMedia("(prefers-color-scheme: dark)");
    if (mq.addEventListener) mq.addEventListener("change", redraw);
  }

  window.GTMermaid = { draw: draw };
})();
