// A small DOM for running the interface scripts under node:test without a
// browser and without a package: enough of Document, Element, events, location,
// history and EventSource for the scripts in ../static. It parses the HTML the
// templates produce (tags, attributes, text, <template> content), matches the
// selectors the scripts use (tag, .class, [attr], [attr="v"], :not(), the
// descendant and child combinators, lists) and keeps no layout, no CSS and no
// focus order. What a script needs beyond that is a gap to fill here, not a
// reason to loosen a test.
import vm from "node:vm";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const VOID = new Set(["area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "source", "track", "wbr"]);
// The names the Go templates write (html/template escapes " as &#34;, ' as &#39;
// and + as &#43;), and any other number.
const ENTITIES = { "&amp;": "&", "&lt;": "<", "&gt;": ">", "&quot;": '"', "&#34;": '"', "&#39;": "'", "&nbsp;": "\u00a0" };
const decode = (s) =>
  s.replace(/&(?:amp|lt|gt|quot|nbsp|#\d+|#x[0-9a-f]+);/gi, (m) => {
    if (ENTITIES[m]) return ENTITIES[m];
    const num = /^&#x/i.test(m) ? parseInt(m.slice(3, -1), 16) : parseInt(m.slice(2, -1), 10);
    return Number.isNaN(num) ? m : String.fromCodePoint(num);
  });
const encodeText = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
const encodeAttr = (s) => s.replace(/&/g, "&amp;").replace(/"/g, "&quot;");
const camel = (name) => name.replace(/-([a-z])/g, (_, c) => c.toUpperCase());
const dashed = (name) => "data-" + name.replace(/[A-Z]/g, (c) => "-" + c.toLowerCase());

class Node {
  constructor() { this.parentNode = null; this.childNodes = []; }
  get parentElement() { return this.parentNode instanceof Element ? this.parentNode : null; }
  remove() {
    const list = this.parentNode && this.parentNode.childNodes;
    if (list && list.includes(this)) list.splice(list.indexOf(this), 1);
    this.parentNode = null;
  }
  get nextSibling() { const p = this.parentNode; return p ? p.childNodes[p.childNodes.indexOf(this) + 1] || null : null; }
}

class Text extends Node {
  constructor(data) { super(); this.data = data; }
  get textContent() { return this.data; }
}

class Fragment extends Node {
  get children() { return this.childNodes.filter((n) => n instanceof Element); }
  get firstElementChild() { return this.children[0] || null; }
  get textContent() { return this.childNodes.map((n) => n.textContent).join(""); }
  querySelectorAll(sel) { return queryAll(this, sel); }
  querySelector(sel) { return queryAll(this, sel)[0] || null; }
  get innerHTML() { return this.childNodes.map(serialize).join(""); }
}

// Every insertion counts. A script that keeps changing the page it watches (an
// observer that rewrites what it observes) would never end under node:test; it
// ends with this error instead. loadPage starts the count again.
const MUTATION_LIMIT = 20000;
let mutations = 0;
function tally() {
  if (++mutations > MUTATION_LIMIT) throw new Error(`endless mutation: more than ${MUTATION_LIMIT} changes of the page; a script keeps rewriting what it reacts to`);
}

function insert(parent, nodes, index) {
  tally();
  const list = nodes.flatMap((n) => (typeof n === "string" ? [new Text(n)] : n instanceof Fragment ? n.childNodes.splice(0) : [n]));
  for (const n of list) n.remove();
  for (const n of list) n.parentNode = parent;
  parent.childNodes.splice(index, 0, ...list);
}

export class Element extends Node {
  constructor(tag, doc) {
    super();
    this.tagName = tag.toUpperCase();
    this.ownerDocument = doc;
    this.attrs = new Map();
    this.listeners = new Map();
    this.content = tag.toLowerCase() === "template" ? new Fragment() : null;
    this.style = {};
    const self = this;
    this.dataset = new Proxy({}, {
      get: (_, k) => (typeof k === "string" && self.attrs.has(dashed(k)) ? self.attrs.get(dashed(k)) : undefined),
      set: (_, k, v) => { self.attrs.set(dashed(k), String(v)); return true; },
      has: (_, k) => self.attrs.has(dashed(k)),
      deleteProperty: (_, k) => { self.attrs.delete(dashed(k)); return true; },
      ownKeys: () => [...self.attrs.keys()].filter((n) => n.startsWith("data-")).map((n) => camel(n.slice(5))),
      getOwnPropertyDescriptor: (_, k) => (self.attrs.has(dashed(k)) ? { enumerable: true, configurable: true, value: self.attrs.get(dashed(k)) } : undefined),
    });
  }
  get localName() { return this.tagName.toLowerCase(); }
  getAttribute(n) { return this.attrs.has(n) ? this.attrs.get(n) : null; }
  setAttribute(n, v) { this.attrs.set(n, String(v)); }
  hasAttribute(n) { return this.attrs.has(n); }
  removeAttribute(n) { this.attrs.delete(n); }
  toggleAttribute(n, force) {
    const on = force === undefined ? !this.attrs.has(n) : !!force;
    if (on) this.attrs.set(n, ""); else this.attrs.delete(n);
    return on;
  }
  get id() { return this.getAttribute("id") || ""; }
  set id(v) { this.setAttribute("id", v); }
  get className() { return this.getAttribute("class") || ""; }
  set className(v) { this.setAttribute("class", v); }
  get classList() {
    const el = this;
    const names = () => el.className.split(/\s+/).filter(Boolean);
    const put = (list) => el.setAttribute("class", list.join(" "));
    return {
      contains: (c) => names().includes(c),
      add: (...c) => put([...new Set([...names(), ...c])]),
      remove: (...c) => put(names().filter((n) => !c.includes(n))),
      toggle(c, force) {
        const on = force === undefined ? !names().includes(c) : !!force;
        if (on) this.add(c); else this.remove(c);
        return on;
      },
    };
  }
  get hidden() { return this.attrs.has("hidden"); }
  set hidden(v) { this.toggleAttribute("hidden", v); }
  get disabled() { return this.attrs.has("disabled"); }
  set disabled(v) { this.toggleAttribute("disabled", v); }
  get href() {
    const raw = this.getAttribute("href");
    if (raw === null) return "";
    try { return new URL(raw, this.ownerDocument.location.href).href; } catch (_) { return raw; }
  }
  set href(v) { this.setAttribute("href", v); }
  get action() { const raw = this.getAttribute("action"); return raw === null ? this.ownerDocument.location.href : new URL(raw, this.ownerDocument.location.href).href; }
  get method() { return this.getAttribute("method") || "get"; }
  get name() { return this.getAttribute("name") || ""; }
  set name(v) { this.setAttribute("name", v); }
  get type() { return this.getAttribute("type") || (this.localName === "input" ? "text" : ""); }
  set type(v) { this.setAttribute("type", v); }
  get value() { return this._value !== undefined ? this._value : this.getAttribute("value") || ""; }
  set value(v) { this._value = String(v); }
  get form() { let n = this.parentElement; while (n && n.localName !== "form") n = n.parentElement; return n; }
  get children() { return this.childNodes.filter((n) => n instanceof Element); }
  get firstElementChild() { return this.children[0] || null; }
  get textContent() { return this.childNodes.map((n) => n.textContent).join(""); }
  set textContent(v) { this.childNodes.forEach((n) => { n.parentNode = null; }); this.childNodes = []; if (v !== "") insert(this, [String(v)], 0); }
  get innerHTML() { return (this.content ? this.content.childNodes : this.childNodes).map(serialize).join(""); }
  set innerHTML(html) {
    tally();
    const target = this.content || this;
    target.childNodes.forEach((n) => { n.parentNode = null; });
    target.childNodes = [];
    insert(target, parse(html, this.ownerDocument), 0);
  }
  append(...n) { insert(this, n, this.childNodes.length); }
  appendChild(n) { insert(this, [n], this.childNodes.length); return n; }
  before(...n) { insert(this.parentNode, n, this.parentNode.childNodes.indexOf(this)); }
  after(...n) { insert(this.parentNode, n, this.parentNode.childNodes.indexOf(this) + 1); }
  contains(n) { for (let x = n; x; x = x.parentNode) if (x === this) return true; return false; }
  matches(sel) { return matchesAny(this, sel); }
  closest(sel) { for (let n = this; n instanceof Element; n = n.parentNode) if (n.matches(sel)) return n; return null; }
  querySelectorAll(sel) { return queryAll(this, sel); }
  querySelector(sel) { return queryAll(this, sel)[0] || null; }
  addEventListener(type, fn) { (this.listeners.get(type) || this.listeners.set(type, []).get(type)).push(fn); }
  removeEventListener(type, fn) {
    const l = this.listeners.get(type) || [];
    const at = l.indexOf(fn);
    if (at >= 0) l.splice(at, 1);
  }
  dispatchEvent(ev) { return this.ownerDocument.dispatch(this, ev); }
  click(init = {}) { return this.dispatchEvent(new Event("click", { bubbles: true, cancelable: true, button: 0, ...init })); }
  // focus and blur fire what a browser fires: blur on the element that gives the
  // focus up, then focus on the one that gets it; neither bubbles, focusin and
  // focusout do.
  focus() {
    const doc = this.ownerDocument;
    const before = doc.activeElement;
    if (before === this) return;
    if (before && before !== doc.body) {
      doc.activeElement = doc.body;
      before.dispatchEvent(new Event("blur"));
      before.dispatchEvent(new Event("focusout", { bubbles: true }));
    }
    doc.activeElement = this;
    this.dispatchEvent(new Event("focus"));
    this.dispatchEvent(new Event("focusin", { bubbles: true }));
  }
  blur() {
    const doc = this.ownerDocument;
    if (doc.activeElement !== this) return;
    doc.activeElement = doc.body;
    this.dispatchEvent(new Event("blur"));
    this.dispatchEvent(new Event("focusout", { bubbles: true }));
  }
  select() { this.selected = true; }
  setSelectionRange(a, b) { this.selectionStart = a; this.selectionEnd = b; }
  requestSubmit() { return this.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); }
  scrollIntoView() {}
}

export class HTMLFormElement extends Element {}
export class HTMLInputElement extends Element {
  get selectionStart() { return this._ss !== undefined ? this._ss : this.value.length; }
  set selectionStart(v) { this._ss = v; }
  get selectionEnd() { return this._se !== undefined ? this._se : this.value.length; }
  set selectionEnd(v) { this._se = v; }
}

export class Event {
  constructor(type, init = {}) {
    this.type = type;
    Object.assign(this, { bubbles: false, cancelable: false, button: 0, ctrlKey: false, metaKey: false, shiftKey: false, altKey: false, isComposing: false, key: "" }, init);
    this.defaultPrevented = false;
  }
  preventDefault() { if (this.cancelable) this.defaultPrevented = true; }
  stopPropagation() { this.stopped = true; }
}

// ---------------------------------------------------------------- HTML

function parse(html, doc) {
  const root = new Fragment();
  const stack = [root];
  const top = () => stack[stack.length - 1];
  const re = /<!--[\s\S]*?-->|<\/([a-zA-Z][\w-]*)\s*>|<([a-zA-Z][\w-]*)((?:\s+[^\s=/>]+(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+))?)*)\s*(\/?)>|([^<]+)|</g;
  let m;
  while ((m = re.exec(html))) {
    if (m[0].startsWith("<!--")) continue;
    if (m[1]) {
      const at = stack.map((n) => n.localName).lastIndexOf(m[1].toLowerCase());
      if (at > 0) stack.length = at;
    } else if (m[2]) {
      const el = doc.createElement(m[2].toLowerCase());
      for (const a of m[3].matchAll(/([^\s=/>]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+)))?/g)) {
        el.setAttribute(a[1], decode(a[2] ?? a[3] ?? a[4] ?? ""));
      }
      const into = top().content || top();
      insert(into, [el], into.childNodes.length);
      if (!m[4] && !VOID.has(el.localName)) stack.push(el);
    } else {
      const into = top().content || top();
      insert(into, [decode(m[5] ?? m[0])], into.childNodes.length);
    }
  }
  return root.childNodes.splice(0);
}

function serialize(n) {
  if (n instanceof Text) return encodeText(n.data);
  const attrs = [...n.attrs].map(([k, v]) => (v === "" ? ` ${k}` : ` ${k}="${encodeAttr(v)}"`)).join("");
  const tag = n.localName;
  return VOID.has(tag) ? `<${tag}${attrs}>` : `<${tag}${attrs}>${n.innerHTML}</${tag}>`;
}

// ---------------------------------------------------------------- selectors

function compounds(sel) {
  return sel.split(/,(?![^(]*\))/).map((part) => {
    const tokens = [];
    let comb = " ";
    for (const piece of part.trim().match(/[>]|(?:[^\s>\[(]|\[[^\]]*\]|\([^)]*\))+/g)) {
      if (piece === ">") { comb = ">"; continue; }
      tokens.push({ comb, test: simple(piece) });
      comb = " ";
    }
    return tokens;
  });
}

function simple(piece) {
  const checks = [];
  let rest = piece;
  const tag = /^[a-zA-Z][\w-]*/.exec(rest);
  if (tag) { checks.push((el) => el.localName === tag[0].toLowerCase()); rest = rest.slice(tag[0].length); }
  const re = /^(?:\.([\w-]+)|\[([\w-]+)(?:="([^"]*)")?\]|:not\(([^)]*)\))/;
  while (rest) {
    const m = re.exec(rest);
    if (!m) throw new Error("selector not supported by the test DOM: " + piece);
    if (m[1]) checks.push((el) => el.classList.contains(m[1]));
    else if (m[2]) checks.push((el) => el.hasAttribute(m[2]) && (m[3] === undefined || el.getAttribute(m[2]) === m[3]));
    else { const inner = simple(m[4]); checks.push((el) => !inner(el)); }
    rest = rest.slice(m[0].length);
  }
  return (el) => checks.every((c) => c(el));
}

function matchChain(el, tokens, i) {
  if (!tokens[i].test(el)) return false;
  if (i === 0) return true;
  if (tokens[i].comb === ">") return el.parentNode instanceof Element && matchChain(el.parentNode, tokens, i - 1);
  for (let p = el.parentNode; p instanceof Element; p = p.parentNode) if (matchChain(p, tokens, i - 1)) return true;
  return false;
}

const matchesAny = (el, sel) => compounds(sel).some((tokens) => matchChain(el, tokens, tokens.length - 1));

function queryAll(root, sel) {
  const list = compounds(sel);
  const out = [];
  const walk = (n) => {
    for (const c of n.childNodes) {
      if (!(c instanceof Element)) continue;
      if (list.some((tokens) => matchChain(c, tokens, tokens.length - 1))) out.push(c);
      walk(c);
    }
  };
  walk(root);
  return out;
}

// ---------------------------------------------------------------- the page

class Document extends Node {
  constructor() {
    super();
    this.listeners = new Map();
    this.readyState = "complete";
    this.cookie = "";
    this.html = this.createElement("html");
    this.body = this.createElement("body");
    this.html.append(this.body);
    this.activeElement = this.body;
    this.documentElement = this.html;
    this.windowListeners = new Map();
  }
  createElement(tag) {
    const t = tag.toLowerCase();
    return new (t === "form" ? HTMLFormElement : t === "input" ? HTMLInputElement : Element)(t, this);
  }
  createTextNode(s) { return new Text(s); }
  getElementById(id) { return queryAll(this.html, `[id="${id}"]`)[0] || null; }
  querySelectorAll(sel) { return queryAll(this.html, sel); }
  querySelector(sel) { return queryAll(this.html, sel)[0] || null; }
  addEventListener(type, fn, capture) { (this.listeners.get(type) || this.listeners.set(type, []).get(type)).push({ fn, capture: !!capture }); }
  removeEventListener(type, fn, capture) {
    const l = this.listeners.get(type) || [];
    const at = l.findIndex((x) => x.fn === fn && x.capture === !!capture);
    if (at >= 0) l.splice(at, 1);
  }
  // dispatch runs the listeners the way a browser does: capture on document,
  // target, bubbling up to the document and the window.
  dispatch(target, ev) {
    ev.target = target;
    const path = [];
    for (let n = target; n; n = n.parentNode) path.push(n);
    const run = (list, node) => { for (const l of [...list]) { if (ev.stopped) return; ev.currentTarget = node; (l.fn || l)(ev); } };
    run((this.listeners.get(ev.type) || []).filter((l) => l.capture), this);
    for (const n of path) {
      if (n === this.html && !ev.bubbles) break;
      if (n !== target && !ev.bubbles) break;
      run((n.listeners ? n.listeners.get(ev.type) || [] : []), n);
    }
    if (ev.bubbles && !ev.stopped) {
      run((this.listeners.get(ev.type) || []).filter((l) => !l.capture), this);
      run(this.windowListeners.get(ev.type) || [], this);
    }
    return !ev.defaultPrevented;
  }
}

// manualTimers is a clock the test turns: setTimeout and clearTimeout that run
// nothing until advance(ms) has passed their time, in the order of their time.
export function manualTimers() {
  let now = 0;
  let nextId = 1;
  const queue = new Map();
  return {
    setTimeout(fn, ms = 0) { const id = nextId++; queue.set(id, { fn, at: now + ms }); return id; },
    clearTimeout(id) { queue.delete(id); },
    advance(ms) {
      const until = now + ms;
      for (;;) {
        const due = [...queue].filter(([, t]) => t.at <= until).sort((a, b) => a[1].at - b[1].at || a[0] - b[0])[0];
        if (!due) break;
        queue.delete(due[0]);
        now = Math.max(now, due[1].at);
        due[1].fn();
      }
      now = until;
    },
    pending: () => queue.size,
  };
}

// What a script may use of the browser and this DOM does not have: using it
// fails at once, naming the gap, instead of a test that passes on a missing
// feature. They are constructors that throw (an accessor would not do: the vm
// answers a bare name with "is not defined" and typeof with "undefined", and the
// script would go on without it). A test that needs one passes its own in hooks.
function missing(win, names) {
  for (const name of names) {
    win[name] = class {
      constructor() { throw new Error(`${name} is not in the test DOM (jstest/dom.mjs): build it there or pass one in hooks`); }
    };
  }
}

class FakeEventSource {
  static instances = [];
  constructor(url) { this.url = url; this.closed = false; this.handlers = new Map(); this.constructor.instances.push(this); }
  addEventListener(type, fn) { (this.handlers.get(type) || this.handlers.set(type, []).get(type)).push(fn); }
  close() { this.closed = true; }
  emit(type, data) { for (const fn of this.handlers.get(type) || []) fn({ data: typeof data === "string" ? data : JSON.stringify(data) }); }
}

// loadPage makes a window for the HTML, in which scripts can be run. The
// location is where the page was "opened"; history and popstate follow it.
export function loadPage(html, { url = "http://localhost/ui/", scripts = [], body = html, hooks = {} } = {}) {
  mutations = 0;
  const doc = new Document();
  // Each page has its own list of the streams it opened.
  const PageEventSource = class extends FakeEventSource { static instances = []; };
  const win = { document: doc, EventSource: PageEventSource, URL, URLSearchParams, setTimeout, clearTimeout, console, Event, KeyboardEvent: Event, MouseEvent: Event };
  win.window = win;
  win.globalThis = win;
  // The page's own animation frames: queued, and run when the test says so (frame()).
  const frames = new Map();
  let frameId = 0;
  win.requestAnimationFrame = (fn) => { frames.set(++frameId, fn); return frameId; };
  win.cancelAnimationFrame = (id) => { frames.delete(id); };
  // Present in node itself, so the page gets the real one.
  win.AbortController = AbortController;
  win.AbortSignal = AbortSignal;
  missing(win, ["MutationObserver", "IntersectionObserver", "ResizeObserver"]);
  let current = new URL(url);
  const entries = [{ url: current.href }];
  let at = 0;
  const popstate = () => { for (const fn of doc.windowListeners.get("popstate") || []) fn({ type: "popstate" }); };
  win.location = new Proxy({}, {
    get: (_, k) => (k === "replace" ? (u) => { current = new URL(u, current); entries[at] = { url: current.href }; } : k === "reload" ? () => {} : current[k]),
    set: (_, k, v) => { if (k === "href") current = new URL(v, current); return true; },
  });
  doc.location = win.location;
  win.history = {
    pushState: (_s, _t, u) => { current = new URL(u, current); entries.splice(at + 1, Infinity, { url: current.href }); at++; },
    replaceState: (_s, _t, u) => { current = new URL(u, current); entries[at] = { url: current.href }; },
    back: () => { if (at > 0) { at--; current = new URL(entries[at].url); popstate(); } },
    forward: () => { if (at < entries.length - 1) { at++; current = new URL(entries[at].url); popstate(); } },
    get length() { return entries.length; },
  };
  win.addEventListener = (type, fn) => { (doc.windowListeners.get(type) || doc.windowListeners.set(type, []).get(type)).push(fn); };
  win.removeEventListener = (type, fn) => {
    const l = doc.windowListeners.get(type) || [];
    const at = l.indexOf(fn);
    if (at >= 0) l.splice(at, 1);
  };
  win.matchMedia = () => ({ matches: false });
  win.navigator = {};
  const store = new Map();
  win.localStorage = { getItem: (k) => (store.has(k) ? store.get(k) : null), setItem: (k, v) => store.set(k, String(v)), removeItem: (k) => store.delete(k) };
  Object.assign(win, { Element, HTMLElement: Element, HTMLFormElement, HTMLInputElement, Node });
  Object.assign(win, hooks);
  doc.body.innerHTML = body;
  const ctx = vm.createContext(win);
  const run = (file) => vm.runInContext(readFileSync(fileURLToPath(new URL("../static/" + file, import.meta.url)), "utf8"), ctx, { filename: file });
  for (const file of scripts) run(file);
  return {
    window: win, document: doc, run,
    // eval runs source text as a script of the page.
    eval: (code) => vm.runInContext(code, ctx),
    EventSource: PageEventSource,
    path: () => current.pathname + current.search,
    // frame runs the animation frames that were asked for; the ones they ask for in turn wait for the next call.
    frame() { const due = [...frames]; frames.clear(); for (const [, fn] of due) fn(0); },
    key(key, init = {}, target = doc.activeElement) { const ev = new Event("keydown", { bubbles: true, cancelable: true, key, ...init }); target.dispatchEvent(ev); return ev; },
  };
}

export { FakeEventSource };
