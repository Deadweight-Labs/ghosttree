// Loads static/ui-md.js in node with a small DOM stand-in and serialises what
// GTMarkdown.render builds. Used by the Go tests (uimd_perf_test.go) and by hand:
//   node uimd_harness.js render <ui-md.js> <file.md>   prints the HTML of the file
"use strict";
const fs = require("fs");

function node(tag) {
  return {
    tag, className: "", attrs: {}, children: [], _text: null, title: "", target: "", rel: "", start: null,
    setAttribute(k, v) { this.attrs[k] = String(v); },
    appendChild(c) {
      if (c.tag === "#frag") { for (const k of c.children) this.children.push(k); c.children = []; } else this.children.push(c);
      return c;
    },
    set textContent(v) { this.children = v === "" ? [] : [{ tag: "#text", text: String(v) }]; },
    get textContent() { return this.children.map(function t(c) { return c.tag === "#text" ? c.text : c.children.map(t).join(""); }).join(""); },
    hasAttribute() { return false; },
  };
}

function load(path) {
  const g = globalThis;
  g.window = g;
  g.document = {
    readyState: "complete",
    createElement: node,
    createTextNode: (text) => ({ tag: "#text", text }),
    createDocumentFragment: () => node("#frag"),
    querySelectorAll: () => [],
    contains: () => false,
    getElementById: () => null,
    querySelector: () => null,
  };
  g.location = { hash: "" };
  g.GTPill = {
    icon: (name) => { const s = node("svg"); s.attrs["data-icon"] = name; return s; },
    ref: (key, o) => { const a = node(o && o.href ? "a" : "span"); a.className = "ref"; a.attrs["data-ref"] = key; if (o && o.href) a.attrs.href = o.href; return a; },
  };
  delete g.GTMarkdown;
  new Function(fs.readFileSync(path, "utf8")).call(g);
  return g.GTMarkdown;
}

function esc(s) { return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;"); }
function html(n) {
  if (n.tag === "#text") return esc(n.text);
  const a = Object.assign({}, n.attrs);
  if (n.className) a.class = n.className;
  if (n.title) a.title = n.title;
  if (n.target) a.target = n.target;
  if (n.rel) a.rel = n.rel;
  if (n.start != null) a.start = n.start;
  const attrs = Object.keys(a).sort().map((k) => " " + k + '="' + esc(String(a[k])) + '"').join("");
  if (n.tag === "#frag") return n.children.map(html).join("");
  return "<" + n.tag + attrs + ">" + n.children.map(html).join("") + "</" + n.tag + ">";
}

module.exports = { load, html };

if (require.main === module && process.argv[2] === "render") {
  const md = load(process.argv[3]);
  const src = fs.readFileSync(process.argv[4], "utf8");
  process.stdout.write(html(md.render(src, {})) + "\n");
}
